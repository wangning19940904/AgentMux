package feishu

import (
	"context"
	"encoding/json"
	"fmt"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/wangning19940904/AgentMux/core"
)

// statusCardClient posts and patches a card whose JSON the caller rendered.
// It is optional so lightweight test clients need not implement it.
type statusCardClient interface {
	SendRawCard(ctx context.Context, chatID, content string) (messageID string, err error)
	ReplyRawCard(ctx context.Context, messageID, content string) (replyMessageID string, err error)
	PatchRawCard(ctx context.Context, messageID, content string) error
}

// PostStatus posts a status message that UpdateStatus edits later, in msg's
// thread when it has one. It implements core.StatusMessenger.
func (p *Platform) PostStatus(ctx context.Context, msg *core.Message, text string, card bool) (string, error) {
	if p.client == nil {
		return "", fmt.Errorf("%s: client not started", p.name)
	}
	replyTo := threadReplyMessageID(msg)
	if card {
		client, ok := p.client.(statusCardClient)
		if !ok {
			return "", core.ErrStatusUnsupported
		}
		content := buildStatusCard(text, false, false)
		if replyTo != "" {
			return client.ReplyRawCard(ctx, replyTo, content)
		}
		return client.SendRawCard(ctx, msg.ChatID, content)
	}
	if replyTo != "" {
		if client, ok := p.client.(threadReplyClient); ok {
			return client.ReplyText(ctx, replyTo, text)
		}
	}
	return p.client.SendText(ctx, msg.ChatID, text)
}

// UpdateStatus replaces the content of a message PostStatus posted.
func (p *Platform) UpdateStatus(ctx context.Context, messageID, text string, card, done, failed bool) error {
	if p.client == nil {
		return fmt.Errorf("%s: client not started", p.name)
	}
	if !card {
		return p.client.UpdateText(ctx, messageID, text)
	}
	client, ok := p.client.(statusCardClient)
	if !ok {
		return core.ErrStatusUnsupported
	}
	return client.PatchRawCard(ctx, messageID, buildStatusCard(text, done, failed))
}

// buildStatusCard is a compact card: the text as markdown under a one-line
// header whose colour follows the work. update_multi makes it a shared card,
// which Feishu requires before it lets the sender patch it for everyone.
func buildStatusCard(text string, done, failed bool) string {
	if text == "" {
		text = " "
	} else {
		text = linkifyFeishuMarkdown(text)
	}
	template, title := "blue", "AgentMux · 进行中"
	switch {
	case failed:
		template, title = "red", "AgentMux · 未完成"
	case done:
		template, title = "green", "AgentMux · 已结束"
	}
	card := map[string]any{
		"config": map[string]any{"wide_screen_mode": true, "update_multi": true},
		"header": map[string]any{
			"template": template,
			"title":    map[string]any{"tag": "plain_text", "content": title},
		},
		"elements": []map[string]any{{"tag": "markdown", "content": text}},
	}
	b, err := json.Marshal(card)
	if err != nil {
		return `{"config":{"wide_screen_mode":true,"update_multi":true},"elements":[{"tag":"markdown","content":" "}]}`
	}
	return string(b)
}

func (c *larkClient) SendRawCard(ctx context.Context, chatID, content string) (string, error) {
	return c.sendMessage(ctx, chatID, larkim.MsgTypeInteractive, content)
}

func (c *larkClient) ReplyRawCard(ctx context.Context, messageID, content string) (string, error) {
	return c.replyMessage(ctx, messageID, larkim.MsgTypeInteractive, content)
}

func (c *larkClient) PatchRawCard(ctx context.Context, messageID, content string) error {
	req := larkim.NewPatchMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewPatchMessageReqBodyBuilder().Content(content).Build()).
		Build()
	resp, err := c.api.Im.Message.Patch(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("%s update status card failed: %s", c.platform, resp.Msg)
	}
	return nil
}
