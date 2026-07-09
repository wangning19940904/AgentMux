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
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func init() {
	core.RegisterPlatform("feishu", func(cfg map[string]any) (core.Platform, error) {
		return newPlatform("feishu", lark.FeishuBaseUrl, cfg)
	})
	core.RegisterPlatform("lark", func(cfg map[string]any) (core.Platform, error) {
		return newPlatform("lark", lark.LarkBaseUrl, cfg)
	})
}

// Platform is the Feishu adapter.
type Platform struct {
	name      string
	domain    string
	appID     string
	appSecret string
	project   string

	client clientAPI
}

func newPlatform(name, domain string, cfg map[string]any) (*Platform, error) {
	p := &Platform{name: name, domain: domain}
	p.appID, _ = cfg["app_id"].(string)
	p.appSecret, _ = cfg["app_secret"].(string)
	p.project, _ = cfg["project"].(string)
	if p.appID == "" || p.appSecret == "" {
		return nil, fmt.Errorf("%s: app_id and app_secret are required", name)
	}
	return p, nil
}

// Name returns the registered name.
func (p *Platform) Name() string { return p.name }

// Start opens the long connection and forwards inbound messages.
func (p *Platform) Start(ctx context.Context, inbound chan<- *core.Message) error {
	if p.client == nil {
		c, err := newLarkClient(p.name, p.domain, p.appID, p.appSecret)
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
		return fmt.Errorf("%s: client not started", p.name)
	}
	_, err := p.client.SendText(ctx, msg.ChatID, text)
	return err
}

// Send delivers an unsolicited message to a chat.
func (p *Platform) Send(ctx context.Context, chatID, text string) error {
	if p.client == nil {
		return fmt.Errorf("%s: client not started", p.name)
	}
	_, err := p.client.SendText(ctx, chatID, text)
	return err
}

// BeginMessageReply opens a streaming plain-text reply for the chat that
// originated msg. The first Update posts a text message; later Updates edit it.
func (p *Platform) BeginMessageReply(ctx context.Context, msg *core.Message) (core.ReplyStream, error) {
	if p.client == nil {
		return nil, fmt.Errorf("%s: client not started", p.name)
	}
	return &textStream{client: p.client, chatID: msg.ChatID}, nil
}

// BeginReply opens a streaming interactive-card reply for the chat that
// originated msg. It implements core.StreamReplier so the engine renders a
// whole agent turn as one in-place updating card.
func (p *Platform) BeginReply(ctx context.Context, msg *core.Message) (core.ReplyStream, error) {
	if p.client == nil {
		return nil, fmt.Errorf("%s: client not started", p.name)
	}
	return &cardStream{client: p.client, chatID: msg.ChatID}, nil
}

// AddReaction marks the inbound message while the agent is working.
func (p *Platform) AddReaction(ctx context.Context, msg *core.Message, emojiType string) (string, error) {
	if p.client == nil {
		return "", fmt.Errorf("%s: client not started", p.name)
	}
	return p.client.AddReaction(ctx, msg.ID, emojiType)
}

// DeleteReaction removes a mark previously added by AddReaction.
func (p *Platform) DeleteReaction(ctx context.Context, msg *core.Message, reactionID string) error {
	if p.client == nil {
		return fmt.Errorf("%s: client not started", p.name)
	}
	return p.client.DeleteReaction(ctx, msg.ID, reactionID)
}

// textStream is a live Feishu text message: the first Update posts the
// message, later Updates edit it in place.
type textStream struct {
	client    clientAPI
	chatID    string
	messageID string
}

func (s *textStream) Update(ctx context.Context, text string, done, failed bool) error {
	if s.messageID == "" {
		id, err := s.client.SendText(ctx, s.chatID, text)
		if err != nil {
			return err
		}
		s.messageID = id
		return nil
	}
	return s.client.UpdateText(ctx, s.messageID, text)
}

func (s *textStream) Close(ctx context.Context) error { return nil }

// cardStream is a live Feishu card. It prefers the native CardKit streaming
// path (a card entity whose text element is updated with a real typewriter
// effect); if creating that entity fails (e.g. missing cardkit:card:write
// permission) it degrades to the legacy path of posting a card message and
// patching it in place.
type cardStream struct {
	client clientAPI
	chatID string

	// native streaming path state
	cardID   string
	sequence int
	fellBack bool

	// legacy patch path state
	messageID string
}

func (s *cardStream) Update(ctx context.Context, text string, done, failed bool) error {
	if !s.fellBack && s.cardID == "" {
		// First update: try to open a native streaming card.
		id, err := s.client.BeginStreamCard(ctx, s.chatID)
		if err != nil {
			s.fellBack = true
		} else {
			s.cardID = id
		}
	}

	if s.cardID != "" {
		return s.updateNative(ctx, text, done, failed)
	}
	return s.updateLegacy(ctx, text, done, failed)
}

func (s *cardStream) updateNative(ctx context.Context, text string, done, failed bool) error {
	s.sequence++
	if done {
		return s.client.FinishStreamCard(ctx, s.cardID, text, s.sequence, failed)
	}
	return s.client.StreamCardText(ctx, s.cardID, text, s.sequence)
}

func (s *cardStream) updateLegacy(ctx context.Context, text string, done, failed bool) error {
	if s.messageID == "" {
		id, err := s.client.SendCard(ctx, s.chatID, text, done, failed)
		if err != nil {
			return err
		}
		s.messageID = id
		return nil
	}
	return s.client.UpdateCard(ctx, s.messageID, text, done, failed)
}

func (s *cardStream) Close(ctx context.Context) error { return nil }

// Stop closes the connection.
func (p *Platform) Stop(ctx context.Context) error {
	if p.client == nil {
		return nil
	}
	return p.client.Close()
}
