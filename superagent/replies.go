package superagent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wangning19940904/AgentMux/core"
)

const (
	maxChatReplyRunes  = 8_000
	maxOtherReplyRunes = 60_000
	defaultGitHubAPI   = "https://api.github.com"
	feishuOpenBase     = "https://open.feishu.cn"
	larkOpenBase       = "https://open.larksuite.com"
)

// ReplySender writes to a Source's Channel. Target is the opaque address the
// Source stored on the Trigger.
type ReplySender interface {
	Send(ctx context.Context, source core.SuperAgentSource, target []byte, text string) error
}

// ChatSender is the Engine's outbound chat surface.
type ChatSender interface {
	ReplyToChat(ctx context.Context, channelID, chatID, messageID, text string) error
}

// Replies is the production ReplySender.
type Replies struct {
	Chats     ChatSender
	HTTP      *http.Client
	SendEmail func(ctx context.Context, source core.SuperAgentSource, to string, message []byte) error
}

func (r *Replies) Send(ctx context.Context, source core.SuperAgentSource, target []byte, text string) error {
	switch source.Kind {
	case core.SuperAgentSourceFeishuMessage, core.SuperAgentSourceFeishuDoc:
		var reply feishuReply
		if err := json.Unmarshal(target, &reply); err != nil {
			return err
		}
		if reply.ChatID == "" {
			return nil // A document Source without a notify chat records results only.
		}
		return r.Chats.ReplyToChat(ctx, reply.ChannelID, reply.ChatID, reply.MessageID, truncateRunes(text, maxChatReplyRunes))
	case core.SuperAgentSourceGitHub:
		var reply githubReply
		if err := json.Unmarshal(target, &reply); err != nil {
			return err
		}
		return r.commentOnGitHub(ctx, source, reply, truncateRunes(text, maxOtherReplyRunes))
	case core.SuperAgentSourceEmail:
		var reply emailReply
		if err := json.Unmarshal(target, &reply); err != nil {
			return err
		}
		return r.replyByEmail(ctx, source, reply, truncateRunes(text, maxOtherReplyRunes))
	default:
		return fmt.Errorf("source kind %q cannot reply", source.Kind)
	}
}

func (r *Replies) httpClient() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (r *Replies) commentOnGitHub(ctx context.Context, source core.SuperAgentSource, reply githubReply, text string) error {
	token := strings.TrimSpace(source.Config["github_token"])
	if token == "" {
		return errors.New("github_token is not configured, so this source cannot reply")
	}
	base := strings.TrimRight(firstNonEmpty(source.Config["api_base"], defaultGitHubAPI), "/")
	body, _ := json.Marshal(map[string]string{"body": text})
	target := fmt.Sprintf("%s/repos/%s/issues/%d/comments", base, reply.Repo, reply.Number)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Content-Type", "application/json")
	response, err := r.httpClient().Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return fmt.Errorf("GitHub comment failed: %s: %s", response.Status, strings.TrimSpace(string(detail)))
	}
	return nil
}

func (r *Replies) replyByEmail(ctx context.Context, source core.SuperAgentSource, reply emailReply, text string) error {
	from := firstNonEmpty(source.Config["from_address"], source.Config["username"])
	message := buildEmail(from, reply, text)
	if r.SendEmail != nil {
		return r.SendEmail(ctx, source, reply.To, message)
	}
	return sendSMTP(ctx, source, from, reply.To, message)
}

// buildEmail strips line breaks from every header value: subjects and message
// ids come from the incoming mail, and a raw newline would let its sender inject
// headers or recipients into the reply. Auto-Submitted stops other auto-responders.
func buildEmail(from string, reply emailReply, text string) []byte {
	clean := func(value string) string { return strings.NewReplacer("\r", " ", "\n", " ").Replace(value) }
	var message bytes.Buffer
	domain := "agentmux.local"
	if _, after, found := strings.Cut(from, "@"); found {
		domain = clean(after)
	}
	headers := [][2]string{
		{"From", clean(from)},
		{"To", clean(reply.To)},
		{"Subject", mime.QEncoding.Encode("utf-8", clean(reply.Subject))},
		{"Date", time.Now().UTC().Format(time.RFC1123Z)},
		{"Message-ID", "<" + uuid.NewString() + "@" + domain + ">"},
		{"Auto-Submitted", "auto-replied"},
		{"MIME-Version", "1.0"},
		{"Content-Type", `text/plain; charset="utf-8"`},
		{"Content-Transfer-Encoding", "quoted-printable"},
	}
	if reply.MessageID != "" {
		headers = append(headers, [2]string{"In-Reply-To", clean(reply.MessageID)})
	}
	if reply.References != "" {
		headers = append(headers, [2]string{"References", clean(reply.References)})
	}
	for _, header := range headers {
		fmt.Fprintf(&message, "%s: %s\r\n", header[0], header[1])
	}
	message.WriteString("\r\n")
	writer := quotedprintable.NewWriter(&message)
	_, _ = writer.Write([]byte(text))
	_ = writer.Close()
	return message.Bytes()
}

// sendSMTP requires an encrypted connection: port 465 uses implicit TLS and
// every other port must offer STARTTLS, or the password is never sent.
func sendSMTP(ctx context.Context, source core.SuperAgentSource, from, to string, message []byte) error {
	host, address, err := smtpAddress(source)
	if err != nil {
		return err
	}
	config := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	dialer := &net.Dialer{Timeout: emailDialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
	}
	implicitTLS := strings.HasSuffix(address, ":465")
	if implicitTLS {
		secure := tls.Client(conn, config)
		if err := secure.HandshakeContext(ctx); err != nil {
			return err
		}
		conn = secure
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer client.Close()
	if !implicitTLS {
		if err := client.StartTLS(config); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}
	if err := client.Auth(smtp.PlainAuth("", source.Config["username"], source.Config["password"], host)); err != nil {
		return fmt.Errorf("smtp auth: %w", err)
	}
	if err := client.Mail(from); err != nil {
		return err
	}
	if err := client.Rcpt(to); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := writer.Write(message); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}

// transferDecoder undoes a body's Content-Transfer-Encoding.
func transferDecoder(encoding string, body io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, newlineStripper{body})
	case "quoted-printable":
		return quotedprintable.NewReader(body)
	default:
		return body
	}
}

type newlineStripper struct{ reader io.Reader }

func (s newlineStripper) Read(data []byte) (int, error) {
	count, err := s.reader.Read(data)
	kept := 0
	for _, value := range data[:count] {
		if value != '\r' && value != '\n' {
			data[kept] = value
			kept++
		}
	}
	return kept, err
}

// ChannelLookup resolves a channel's credentials for Feishu API calls.
type ChannelLookup func(ctx context.Context, id string) (*core.Channel, error)

// FeishuAPI calls Feishu as a channel's app. It is the production
// DocumentSubscriber and FeishuDirectory.
type FeishuAPI struct {
	Channels ChannelLookup
	HTTP     *http.Client
	Base     string
}

func (f *FeishuAPI) client() *http.Client {
	if f.HTTP != nil {
		return f.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (f *FeishuAPI) tenantToken(ctx context.Context, channelID string) (string, string, error) {
	channel, err := f.Channels(ctx, channelID)
	if err != nil {
		return "", "", err
	}
	if channel == nil {
		return "", "", fmt.Errorf("channel %q not found", channelID)
	}
	if channel.Type != "feishu" && channel.Type != "lark" {
		return "", "", fmt.Errorf("channel %q is not a Feishu channel", channelID)
	}
	base := f.Base
	if base == "" {
		base = feishuOpenBase
		if channel.Type == "lark" {
			base = larkOpenBase
		}
	}
	payload, _ := json.Marshal(map[string]string{"app_id": channel.Config["app_id"], "app_secret": channel.Config["app_secret"]})
	var result struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
	}
	if err := f.call(ctx, http.MethodPost, base+"/open-apis/auth/v3/tenant_access_token/internal", "", payload, &result); err != nil {
		return "", "", err
	}
	if result.Code != 0 {
		return "", "", fmt.Errorf("tenant token: %d %s", result.Code, result.Msg)
	}
	return base, result.TenantAccessToken, nil
}

func (f *FeishuAPI) call(ctx context.Context, method, target, token string, payload []byte, out any) error {
	request, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := f.client().Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s: %s", response.Status, strconv.Quote(string(body)))
	}
	return nil
}

func (f *FeishuAPI) Subscribe(ctx context.Context, channelID string, document documentRef) error {
	base, token, err := f.tenantToken(ctx, channelID)
	if err != nil {
		return err
	}
	target := fmt.Sprintf("%s/open-apis/drive/v1/files/%s/subscribe?file_type=%s", base, url.PathEscape(document.Token), url.QueryEscape(document.FileType))
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := f.call(ctx, http.MethodPost, target, token, nil, &result); err != nil {
		return err
	}
	if result.Code != 0 {
		return fmt.Errorf("%d %s", result.Code, result.Msg)
	}
	return nil
}

func (f *FeishuAPI) ResolveWikiNode(ctx context.Context, channelID, wikiToken string) (documentRef, error) {
	base, token, err := f.tenantToken(ctx, channelID)
	if err != nil {
		return documentRef{}, err
	}
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Node struct {
				ObjToken string `json:"obj_token"`
				ObjType  string `json:"obj_type"`
			} `json:"node"`
		} `json:"data"`
	}
	target := base + "/open-apis/wiki/v2/spaces/get_node?token=" + url.QueryEscape(wikiToken)
	if err := f.call(ctx, http.MethodGet, target, token, nil, &result); err != nil {
		return documentRef{}, err
	}
	if result.Code != 0 {
		return documentRef{}, fmt.Errorf("%d %s", result.Code, result.Msg)
	}
	return documentRef{Token: result.Data.Node.ObjToken, FileType: result.Data.Node.ObjType}, nil
}
