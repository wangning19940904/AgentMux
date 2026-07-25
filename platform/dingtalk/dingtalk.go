// Package dingtalk implements the DingTalk platform adapter using the
// official Stream-mode SDK: a WebSocket long connection receives chatbot
// callbacks (no public IP required) and replies go through the temporary
// session webhook, falling back to the proactive robot API for unsolicited
// sends (cron pushes).
//
// Chat ids are encoded so proactive sends know the target kind:
// "g:<openConversationId>" for group chats and "u:<staffUserId>" for 1:1.
package dingtalk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"
	dtclient "github.com/open-dingtalk/dingtalk-stream-sdk-go/client"
	"github.com/wangning19940904/AgentMux/core"
)

func init() {
	core.RegisterPlatform("dingtalk", func(cfg map[string]any) (core.Platform, error) {
		p := &Platform{
			httpClient: &http.Client{Timeout: 30 * time.Second},
			webhooks:   map[string]sessionWebhook{},
		}
		p.clientID, _ = cfg["client_id"].(string)
		p.clientSecret, _ = cfg["client_secret"].(string)
		p.robotCode, _ = cfg["robot_code"].(string)
		if p.robotCode == "" {
			p.robotCode = p.clientID
		}
		if p.clientID == "" || p.clientSecret == "" {
			return nil, fmt.Errorf("dingtalk: client_id and client_secret are required")
		}
		return p, nil
	})
}

// Platform is the DingTalk Stream-mode adapter.
type Platform struct {
	clientID     string
	clientSecret string
	robotCode    string
	httpClient   *http.Client

	mu       sync.Mutex
	webhooks map[string]sessionWebhook // chatID -> temporary reply webhook
	token    string
	tokenExp time.Time
}

type sessionWebhook struct {
	url     string
	expires time.Time
}

// Name returns the registered name.
func (p *Platform) Name() string { return "dingtalk" }

// Start hooks this adapter onto the shared stream for its client_id and
// blocks until ctx is cancelled.
func (p *Platform) Start(ctx context.Context, inbound chan<- *core.Message) error {
	st := getStream(p.clientID, p.clientSecret)
	st.setSink(func(data *chatbot.BotCallbackDataModel) {
		p.onCallback(ctx, data, inbound)
	})
	if err := st.start(); err != nil {
		st.setSink(nil)
		return err
	}
	<-ctx.Done()
	st.setSink(nil)
	return nil
}

func (p *Platform) onCallback(ctx context.Context, data *chatbot.BotCallbackDataModel, inbound chan<- *core.Message) {
	text := strings.TrimSpace(data.Text.Content)
	if text == "" {
		return
	}
	chatID := "u:" + data.SenderStaffId
	if data.ConversationType == "2" {
		chatID = "g:" + data.ConversationId
	}
	if data.SessionWebhook != "" {
		expires := time.Now().Add(30 * time.Minute)
		if data.SessionWebhookExpiredTime > 0 {
			expires = time.UnixMilli(data.SessionWebhookExpiredTime)
		}
		p.mu.Lock()
		p.webhooks[chatID] = sessionWebhook{url: data.SessionWebhook, expires: expires}
		p.mu.Unlock()
	}
	msg := &core.Message{
		ID:        data.MsgId,
		ChatID:    chatID,
		UserID:    data.SenderStaffId,
		UserName:  data.SenderNick,
		Text:      text,
		Timestamp: time.Now(),
		Platform:  "dingtalk",
	}
	select {
	case inbound <- msg:
	case <-ctx.Done():
	}
}

// Reply prefers the temporary session webhook of the originating chat and
// falls back to the proactive robot API.
func (p *Platform) Reply(ctx context.Context, msg *core.Message, text string) error {
	p.mu.Lock()
	wh, ok := p.webhooks[msg.ChatID]
	p.mu.Unlock()
	if ok && time.Now().Before(wh.expires) {
		if err := p.replyViaWebhook(ctx, wh.url, text); err == nil {
			return nil
		}
	}
	return p.Send(ctx, msg.ChatID, text)
}

func (p *Platform) replyViaWebhook(ctx context.Context, url, text string) error {
	body, _ := json.Marshal(map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]string{"title": "AgentMux", "text": text},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("dingtalk webhook reply: %s", resp.Status)
	}
	return nil
}

// Send delivers an unsolicited message via the proactive robot API. chatID is
// "g:<openConversationId>", "u:<staffUserId>" or a bare openConversationId.
func (p *Platform) Send(ctx context.Context, chatID, text string) error {
	if chatID == "" {
		return fmt.Errorf("dingtalk: empty chat id")
	}
	token, err := p.accessToken(ctx)
	if err != nil {
		return err
	}
	msgParam, _ := json.Marshal(map[string]string{"title": "AgentMux", "text": text})
	var apiURL string
	var body map[string]any
	switch {
	case strings.HasPrefix(chatID, "u:"):
		apiURL = "https://api.dingtalk.com/v1.0/robot/oToMessages/batchSend"
		body = map[string]any{
			"robotCode": p.robotCode,
			"userIds":   []string{strings.TrimPrefix(chatID, "u:")},
			"msgKey":    "sampleMarkdown",
			"msgParam":  string(msgParam),
		}
	default:
		apiURL = "https://api.dingtalk.com/v1.0/robot/groupMessages/send"
		body = map[string]any{
			"robotCode":          p.robotCode,
			"openConversationId": strings.TrimPrefix(chatID, "g:"),
			"msgKey":             "sampleMarkdown",
			"msgParam":           string(msgParam),
		}
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-acs-dingtalk-access-token", token)
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("dingtalk send: %s: %s", resp.Status, string(respBody))
	}
	return nil
}

// Stop detaches the sink; the shared stream stays connected for the process
// lifetime (the SDK cannot cleanly stop its internal reconnect loop).
func (p *Platform) Stop(ctx context.Context) error {
	getStream(p.clientID, p.clientSecret).setSink(nil)
	return nil
}

func (p *Platform) accessToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	if p.token != "" && time.Now().Before(p.tokenExp) {
		token := p.token
		p.mu.Unlock()
		return token, nil
	}
	p.mu.Unlock()

	body, _ := json.Marshal(map[string]string{"appKey": p.clientID, "appSecret": p.clientSecret})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.dingtalk.com/v1.0/oauth2/accessToken", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken string `json:"accessToken"`
		ExpireIn    int64  `json:"expireIn"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("dingtalk token: %w", err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("dingtalk token: empty response (status %s)", resp.Status)
	}
	p.mu.Lock()
	p.token = out.AccessToken
	p.tokenExp = time.Now().Add(time.Duration(out.ExpireIn)*time.Second - time.Minute)
	p.mu.Unlock()
	return out.AccessToken, nil
}

// --- shared stream ---
//
// The Stream SDK auto-reconnects internally and exposes no way to stop that
// loop, so closing a client would leave a zombie connection competing for
// callbacks. Instead one stream per client_id lives for the process lifetime;
// attach/detach only swaps the message sink.

var (
	streamsMu sync.Mutex
	streams   = map[string]*stream{}
)

type stream struct {
	client *dtclient.StreamClient

	mu      sync.Mutex
	sink    func(*chatbot.BotCallbackDataModel)
	started bool
}

func getStream(clientID, clientSecret string) *stream {
	streamsMu.Lock()
	defer streamsMu.Unlock()
	if s, ok := streams[clientID]; ok {
		return s
	}
	s := &stream{}
	s.client = dtclient.NewStreamClient(
		dtclient.WithAppCredential(dtclient.NewAppCredentialConfig(clientID, clientSecret)),
	)
	s.client.RegisterChatBotCallbackRouter(func(_ context.Context, data *chatbot.BotCallbackDataModel) ([]byte, error) {
		s.mu.Lock()
		sink := s.sink
		s.mu.Unlock()
		if sink != nil {
			sink(data)
		} else {
			slog.Debug("dingtalk: message dropped, no active channel")
		}
		return []byte("{}"), nil
	})
	streams[clientID] = s
	return s
}

func (s *stream) start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil
	}
	if err := s.client.Start(context.Background()); err != nil {
		return err
	}
	s.started = true
	return nil
}

func (s *stream) setSink(sink func(*chatbot.BotCallbackDataModel)) {
	s.mu.Lock()
	s.sink = sink
	s.mu.Unlock()
}
