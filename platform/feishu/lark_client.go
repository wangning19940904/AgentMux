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
			msg := event.Event.Message
			text := extractText(*msg.MessageType, *msg.Content)
			if text == "" {
				return nil
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

func (c *larkClient) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	return nil
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
