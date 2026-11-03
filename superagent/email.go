package superagent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	imapclient "github.com/emersion/go-imap/client"
	"github.com/wangning19940904/AgentMux/core"
)

const (
	emailPollInterval   = time.Minute
	emailBatchSize      = 50
	emailDialTimeout    = 30 * time.Second
	maxEmailBytes       = 2 << 20
	defaultIMAPPort     = "993"
	defaultMailbox      = "INBOX"
	emailConnectTimeout = 45 * time.Second
)

// MailClient reads new messages from a mailbox. It returns raw RFC 5322 bytes
// and leaves the mailbox read-only, so an operator's mail flags stay untouched.
type MailClient interface {
	Fetch(ctx context.Context, source core.SuperAgentSource, position MailPosition, limit int) (MailBatch, error)
}

// MailPosition is an IMAP Source's read position. A changed UIDVALIDITY means
// the old UIDs no longer identify the same messages.
type MailPosition struct {
	UIDValidity uint32 `json:"uid_validity"`
	LastUID     uint32 `json:"last_uid"`
}

// MailBatch holds new messages in UID order and the position after the mailbox
// was selected. On the first read Baseline is true: a new Source starts from
// now and never works through the mailbox's history.
type MailBatch struct {
	Position MailPosition
	Baseline bool
	Messages []MailMessage
}

type MailMessage struct {
	UID uint32
	Raw []byte
}

// emailReply addresses a reply in an email thread.
type emailReply struct {
	To         string `json:"to"`
	Subject    string `json:"subject"`
	MessageID  string `json:"message_id,omitempty"`
	References string `json:"references,omitempty"`
}

func (s *Service) startPollers(ctx context.Context) {
	if s.mail == nil {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(emailPollInterval)
		defer ticker.Stop()
		for {
			for _, claim := range s.claims.ofKind(core.SuperAgentSourceEmail) {
				pollCtx, cancel := context.WithTimeout(ctx, emailPollInterval)
				err := s.pollEmail(pollCtx, claim)
				cancel()
				if ctx.Err() != nil {
					return
				}
				s.recordSourceError(claim.source.ID, err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// pollEmail advances the read position message by message, so a failure part
// way through a batch resumes at the first message it did not store.
func (s *Service) pollEmail(ctx context.Context, claim sourceClaim) error {
	raw, err := s.store.GetSuperAgentSourceState(ctx, claim.source.ID)
	if err != nil {
		return err
	}
	var position MailPosition
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &position); err != nil {
			return fmt.Errorf("decode mailbox position: %w", err)
		}
	}
	batch, err := s.mail.Fetch(ctx, claim.source, position, emailBatchSize)
	if err != nil {
		return err
	}
	save := func(next MailPosition) error {
		data, err := json.Marshal(next)
		if err != nil {
			return err
		}
		return s.store.SetSuperAgentSourceState(ctx, claim.agent.ID, claim.source.ID, string(data))
	}
	if batch.Baseline {
		return save(batch.Position)
	}
	next := MailPosition{UIDValidity: batch.Position.UIDValidity, LastUID: position.LastUID}
	if position.UIDValidity != batch.Position.UIDValidity {
		next.LastUID = 0
	}
	for _, message := range batch.Messages {
		trigger, ok, parseErr := parseEmail(claim.source, message.Raw)
		if parseErr != nil {
			s.log.Warn("skip unreadable email", "source", claim.source.ID, "uid", message.UID, "err", parseErr)
		} else if ok {
			if _, err := s.Ingest(ctx, claim.agent, claim.source, trigger); err != nil {
				return err
			}
		}
		next.LastUID = message.UID
		if err := save(next); err != nil {
			return err
		}
	}
	return save(next)
}

// parseEmail turns one message into a Trigger, or drops it. The From header is
// trivially forged, so by default a sender only counts when the receiving server
// recorded that the message authenticated for that domain.
func parseEmail(source core.SuperAgentSource, raw []byte) (core.SuperAgentTrigger, bool, error) {
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return core.SuperAgentTrigger{}, false, err
	}
	from, err := mail.ParseAddress(message.Header.Get("From"))
	if err != nil {
		return core.SuperAgentTrigger{}, false, fmt.Errorf("parse From: %w", err)
	}
	sender := strings.ToLower(from.Address)
	own := strings.ToLower(firstNonEmpty(source.Config["from_address"], source.Config["username"]))
	if sender == own || isAutomatedEmail(message.Header) {
		return core.SuperAgentTrigger{}, false, nil
	}
	if source.Config["trust_from_header"] != "true" && !emailAuthenticated(message.Header, sender) {
		return core.SuperAgentTrigger{}, false, nil
	}
	subject := decodeHeader(message.Header.Get("Subject"))
	text, err := emailText(message.Header.Get("Content-Type"), message.Header.Get("Content-Transfer-Encoding"), message.Body)
	if err != nil {
		return core.SuperAgentTrigger{}, false, err
	}
	messageID := strings.TrimSpace(message.Header.Get("Message-Id"))
	references := strings.Fields(message.Header.Get("References"))
	root := messageID
	if len(references) > 0 {
		root = references[0]
	} else if inReplyTo := strings.TrimSpace(message.Header.Get("In-Reply-To")); inReplyTo != "" {
		root = inReplyTo
	}
	replySubject := subject
	if !strings.HasPrefix(strings.ToLower(replySubject), "re:") {
		replySubject = "Re: " + replySubject
	}
	reply, _ := json.Marshal(emailReply{
		To: from.Address, Subject: replySubject, MessageID: messageID,
		References: strings.TrimSpace(strings.Join(append(references, messageID), " ")),
	})
	dedupe := messageID
	if dedupe == "" {
		dedupe = sender + "|" + subject + "|" + message.Header.Get("Date")
	}
	return core.SuperAgentTrigger{
		DedupeKey: dedupe, ThreadKey: "email:" + firstNonEmpty(root, dedupe),
		Actor: sender, ActorName: from.Name, Addressed: true,
		Title: subject, Text: subject + "\n\n" + text, Reply: reply,
	}, true, nil
}

// isAutomatedEmail recognizes auto-replies and bulk mail, which also keeps two
// mailboxes from answering each other forever.
func isAutomatedEmail(header mail.Header) bool {
	if submitted := strings.ToLower(strings.TrimSpace(header.Get("Auto-Submitted"))); submitted != "" && submitted != "no" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(header.Get("Precedence"))) {
	case "bulk", "junk", "list", "auto_reply":
		return true
	}
	return header.Get("List-Id") != "" || header.Get("X-Autoreply") != "" || header.Get("X-Autorespond") != ""
}

var (
	authDMARCPass = regexp.MustCompile(`(?i)\bdmarc=pass\b`)
	authDKIMPass  = regexp.MustCompile(`(?i)\bdkim=pass\b[^;]*\bheader\.(?:d|i)=@?([^\s;]+)`)
)

// emailAuthenticated reads only the topmost Authentication-Results header: the
// receiving server prepends it, and anything below it could come from the sender.
func emailAuthenticated(header mail.Header, sender string) bool {
	values := header["Authentication-Results"]
	if len(values) == 0 {
		return false
	}
	results := values[0]
	if authDMARCPass.MatchString(results) {
		return true
	}
	_, domain, found := strings.Cut(sender, "@")
	if !found {
		return false
	}
	for _, match := range authDKIMPass.FindAllStringSubmatch(results, -1) {
		if strings.EqualFold(strings.TrimSuffix(match[1], "."), domain) {
			return true
		}
	}
	return false
}

func decodeHeader(value string) string {
	decoded, err := new(mime.WordDecoder).DecodeHeader(value)
	if err != nil {
		return value
	}
	return decoded
}

func emailText(contentType, transferEncoding string, body io.Reader) (string, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = "text/plain"
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		reader := multipart.NewReader(body, params["boundary"])
		fallback := ""
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				return fallback, nil
			}
			if err != nil {
				return "", err
			}
			text, err := emailText(part.Header.Get("Content-Type"), part.Header.Get("Content-Transfer-Encoding"), part)
			if err != nil {
				return "", err
			}
			partType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
			if partType == "text/plain" && strings.TrimSpace(text) != "" {
				return text, nil
			}
			if fallback == "" {
				fallback = text
			}
		}
	}
	if !strings.HasPrefix(mediaType, "text/") {
		return "", nil
	}
	data, err := io.ReadAll(transferDecoder(transferEncoding, io.LimitReader(body, maxEmailBytes)))
	if err != nil {
		return "", err
	}
	return strings.ToValidUTF8(string(data), "�"), nil
}

// IMAPClient is the production MailClient. Connections are short lived: a poll
// connects, reads and logs out, so an idle Source holds no server resources.
type IMAPClient struct {
	// Dial overrides the TLS connection, for tests against a local server.
	Dial func(ctx context.Context, address string) (net.Conn, error)
}

func (c *IMAPClient) Fetch(ctx context.Context, source core.SuperAgentSource, position MailPosition, limit int) (MailBatch, error) {
	session, err := c.connect(ctx, source)
	if err != nil {
		return MailBatch{}, err
	}
	defer func() { _ = session.Logout() }()
	stop := context.AfterFunc(ctx, func() { _ = session.Terminate() })
	defer stop()
	if err := session.Login(source.Config["username"], source.Config["password"]); err != nil {
		return MailBatch{}, fmt.Errorf("imap login: %w", err)
	}
	status, err := session.Select(firstNonEmpty(source.Config["mailbox"], defaultMailbox), true)
	if err != nil {
		return MailBatch{}, fmt.Errorf("imap select: %w", err)
	}
	batch := MailBatch{Position: MailPosition{UIDValidity: status.UidValidity}}
	if position.UIDValidity == 0 || position.UIDValidity != status.UidValidity {
		batch.Baseline = true
		batch.Position.LastUID = 0
		if status.UidNext > 0 {
			batch.Position.LastUID = status.UidNext - 1
		}
		return batch, nil
	}
	batch.Position.LastUID = position.LastUID
	if status.Messages == 0 {
		return batch, nil
	}
	criteria := imap.NewSearchCriteria()
	criteria.Uid = new(imap.SeqSet)
	criteria.Uid.AddRange(position.LastUID+1, 0)
	uids, err := session.UidSearch(criteria)
	if err != nil {
		return MailBatch{}, fmt.Errorf("imap search: %w", err)
	}
	wanted := new(imap.SeqSet)
	count := 0
	for _, uid := range uids {
		// "last:*" always matches the newest message, even one already read.
		if uid > position.LastUID && count < limit {
			wanted.AddNum(uid)
			count++
		}
	}
	if count == 0 {
		return batch, nil
	}
	section := &imap.BodySectionName{Peek: true}
	messages := make(chan *imap.Message, count)
	done := make(chan error, 1)
	go func() {
		done <- session.UidFetch(wanted, []imap.FetchItem{imap.FetchUid, section.FetchItem()}, messages)
	}()
	for message := range messages {
		body := message.GetBody(section)
		if body == nil {
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(body, maxEmailBytes))
		if err != nil {
			return MailBatch{}, err
		}
		batch.Messages = append(batch.Messages, MailMessage{UID: message.Uid, Raw: raw})
	}
	if err := <-done; err != nil {
		return MailBatch{}, fmt.Errorf("imap fetch: %w", err)
	}
	sortMailByUID(batch.Messages)
	return batch, nil
}

func (c *IMAPClient) connect(ctx context.Context, source core.SuperAgentSource) (*imapclient.Client, error) {
	host := strings.TrimSpace(source.Config["imap_host"])
	address := net.JoinHostPort(host, firstNonEmpty(source.Config["imap_port"], defaultIMAPPort))
	connectCtx, cancel := context.WithTimeout(ctx, emailConnectTimeout)
	defer cancel()
	var conn net.Conn
	var err error
	if c.Dial != nil {
		conn, err = c.Dial(connectCtx, address)
	} else {
		dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: emailDialTimeout}, Config: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}}
		conn, err = dialer.DialContext(connectCtx, "tcp", address)
	}
	if err != nil {
		return nil, fmt.Errorf("imap connect: %w", err)
	}
	session, err := imapclient.New(conn)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("imap greeting: %w", err)
	}
	return session, nil
}

func sortMailByUID(messages []MailMessage) {
	for i := 1; i < len(messages); i++ {
		for j := i; j > 0 && messages[j].UID < messages[j-1].UID; j-- {
			messages[j], messages[j-1] = messages[j-1], messages[j]
		}
	}
}

var errNoSMTP = errors.New("smtp_host is not configured, so this source cannot reply")

func smtpAddress(source core.SuperAgentSource) (string, string, error) {
	host := strings.TrimSpace(source.Config["smtp_host"])
	if host == "" {
		return "", "", errNoSMTP
	}
	port := firstNonEmpty(source.Config["smtp_port"], "465")
	if _, err := strconv.Atoi(port); err != nil {
		return "", "", fmt.Errorf("invalid smtp_port %q", port)
	}
	return host, net.JoinHostPort(host, port), nil
}
