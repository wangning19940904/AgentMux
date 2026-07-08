package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/agentnexus/agentnexus/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

// larkClient wraps the official Lark SDK: a WebSocket client for inbound events
// and an API client for outbound messages.
type larkClient struct {
	platform  string
	domain    string
	appID     string
	appSecret string
	api       *lark.Client
	ws        *larkws.Client
	cancel    context.CancelFunc
}

func newLarkClient(platform, domain, appID, appSecret string) (clientAPI, error) {
	return &larkClient{
		platform:  platform,
		domain:    domain,
		appID:     appID,
		appSecret: appSecret,
		api:       lark.NewClient(appID, appSecret, lark.WithOpenBaseUrl(domain)),
	}, nil
}

func (c *larkClient) Listen(ctx context.Context, project string, inbound chan<- *core.Message) error {
	handler := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(_ context.Context, event *larkim.P2MessageReceiveV1) error {
			if event == nil || event.Event == nil || event.Event.Message == nil {
				return nil
			}
			msg := event.Event.Message
			if msg.MessageType == nil || msg.Content == nil {
				return nil
			}
			text := extractText(*msg.MessageType, *msg.Content)
			if text == "" {
				return nil
			}
			messageID := ""
			if msg.MessageId != nil {
				messageID = *msg.MessageId
			}
			chatID := ""
			if msg.ChatId != nil {
				chatID = *msg.ChatId
			}
			userID := ""
			if event.Event.Sender != nil && event.Event.Sender.SenderId != nil &&
				event.Event.Sender.SenderId.OpenId != nil {
				userID = *event.Event.Sender.SenderId.OpenId
			}
			inbound <- &core.Message{
				ID:       messageID,
				ChatID:   chatID,
				UserID:   userID,
				Text:     text,
				Platform: c.platform,
				Project:  project,
			}
			return nil
		})

	c.ws = larkws.NewClient(c.appID, c.appSecret, larkws.WithDomain(c.domain), larkws.WithEventHandler(handler))
	wsCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	// Start blocks; run until context cancelled.
	errCh := make(chan error, 1)
	go func() { errCh <- c.ws.Start(wsCtx) }()
	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

func (c *larkClient) SendText(ctx context.Context, chatID, text string) error {
	content, _ := json.Marshal(map[string]string{"text": text})
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType(larkim.MsgTypeText).
			Content(string(content)).
			Build()).
		Build()
	resp, err := c.api.Im.Message.Create(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("%s send failed: %s", c.platform, resp.Msg)
	}
	return nil
}

func (c *larkClient) SendCard(ctx context.Context, chatID, text string, done, failed bool) (string, error) {
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType(larkim.MsgTypeInteractive).
			Content(buildCard(text, done, failed)).
			Build()).
		Build()
	resp, err := c.api.Im.Message.Create(ctx, req)
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("%s send card failed: %s", c.platform, resp.Msg)
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return "", fmt.Errorf("%s send card: missing message id", c.platform)
	}
	return *resp.Data.MessageId, nil
}

func (c *larkClient) UpdateCard(ctx context.Context, messageID, text string, done, failed bool) error {
	req := larkim.NewPatchMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewPatchMessageReqBodyBuilder().
			Content(buildCard(text, done, failed)).
			Build()).
		Build()
	resp, err := c.api.Im.Message.Patch(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("%s update card failed: %s", c.platform, resp.Msg)
	}
	return nil
}

func (c *larkClient) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	return nil
}

// buildCard renders text into a Feishu interactive card JSON payload. While a
// turn is streaming (done=false) a subtle "typing" note is appended; the final
// update drops it, and failures switch the header to a red error style.
func buildCard(text string, done, failed bool) string {
	if text == "" {
		text = " "
	}
	elements := []map[string]any{
		{
			"tag":     "markdown",
			"content": text,
		},
	}
	if !done {
		elements = append(elements, map[string]any{
			"tag": "note",
			"elements": []map[string]any{
				{"tag": "plain_text", "content": "正在输入…"},
			},
		})
	}

	template := "blue"
	title := "AgentNexus"
	if done {
		template = "green"
	}
	if failed {
		template = "red"
		title = "AgentNexus · 出错"
	}

	card := map[string]any{
		"config": map[string]any{"wide_screen_mode": true},
		"header": map[string]any{
			"template": template,
			"title": map[string]any{
				"tag":     "plain_text",
				"content": title,
			},
		},
		"elements": elements,
	}
	b, err := json.Marshal(card)
	if err != nil {
		return `{"config":{"wide_screen_mode":true},"elements":[{"tag":"markdown","content":" "}]}`
	}
	return string(b)
}

// extractText pulls plain text out of a Feishu message content payload.
func extractText(msgType, content string) string {
	if msgType != "text" {
		return ""
	}
	var c struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(content), &c); err != nil {
		return ""
	}
	return strings.TrimSpace(c.Text)
}
