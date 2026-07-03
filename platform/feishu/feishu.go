// Package feishu implements the Feishu/Lark platform adapter. It connects via
// Feishu's long-connection (WebSocket) so no public IP is required, receives
// message events, and replies through the IM API.
//
// This adapter is structured for the official larksuite/oapi-sdk-go but keeps
// the SDK wiring behind a small client interface so the core build does not
// require network credentials. The concrete SDK client is constructed lazily
// in Start when app_id/app_secret are present.
package feishu

import (
	"context"
	"fmt"

	"github.com/agentnexus/agentnexus/core"
)

func init() {
	core.RegisterPlatform("feishu", func(cfg map[string]any) (core.Platform, error) {
		p := &Platform{}
		p.appID, _ = cfg["app_id"].(string)
		p.appSecret, _ = cfg["app_secret"].(string)
		p.project, _ = cfg["project"].(string)
		if p.appID == "" || p.appSecret == "" {
			return nil, fmt.Errorf("feishu: app_id and app_secret are required")
		}
		return p, nil
	})
}

// Platform is the Feishu adapter.
type Platform struct {
	appID     string
	appSecret string
	project   string

	client clientAPI
}

// Name returns the registered name.
func (p *Platform) Name() string { return "feishu" }

// Start opens the long connection and forwards inbound messages.
func (p *Platform) Start(ctx context.Context, inbound chan<- *core.Message) error {
	if p.client == nil {
		c, err := newLarkClient(p.appID, p.appSecret)
		if err != nil {
			return err
		}
		p.client = c
	}
	return p.client.Listen(ctx, p.project, inbound)
}

// Reply responds to the chat that originated msg.
func (p *Platform) Reply(ctx context.Context, msg *core.Message, text string) error {
	if p.client == nil {
		return fmt.Errorf("feishu: client not started")
	}
	return p.client.SendText(ctx, msg.ChatID, text)
}

// Send delivers an unsolicited message to a chat.
func (p *Platform) Send(ctx context.Context, chatID, text string) error {
	if p.client == nil {
		return fmt.Errorf("feishu: client not started")
	}
	return p.client.SendText(ctx, chatID, text)
}

// Stop closes the connection.
func (p *Platform) Stop(ctx context.Context) error {
	if p.client == nil {
		return nil
	}
	return p.client.Close()
}
