// Package slack implements the Slack platform adapter using Socket Mode: a
// WebSocket connection receives Events API payloads (no public IP required)
// and replies go through chat.postMessage. Requires a bot token (xoxb-) with
// chat:write plus an app-level token (xapp-) with connections:write, and the
// app must subscribe to app_mention and message.im events.
package slack

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/agentnexus/agentnexus/core"
	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

func init() {
	core.RegisterPlatform("slack", func(cfg map[string]any) (core.Platform, error) {
		p := &Platform{}
		p.botToken, _ = cfg["bot_token"].(string)
		p.appToken, _ = cfg["app_token"].(string)
		if p.botToken == "" || p.appToken == "" {
			return nil, fmt.Errorf("slack: bot_token and app_token are required")
		}
		return p, nil
	})
}

// Platform is the Slack Socket Mode adapter.
type Platform struct {
	botToken string
	appToken string

	mu     sync.Mutex
	client *slackapi.Client
	seen   map[string]bool // message ts dedup (app_mention + message overlap)
}

// Name returns the registered name.
func (p *Platform) Name() string { return "slack" }

// Start opens the Socket Mode connection and forwards inbound messages until
// ctx is cancelled.
func (p *Platform) Start(ctx context.Context, inbound chan<- *core.Message) error {
	client := slackapi.New(p.botToken, slackapi.OptionAppLevelToken(p.appToken))
	socket := socketmode.New(client)
	p.mu.Lock()
	p.client = client
	p.seen = map[string]bool{}
	p.mu.Unlock()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-socket.Events:
				if !ok {
					return
				}
				p.handleEvent(ctx, socket, evt, inbound)
			}
		}
	}()

	err := socket.RunContext(ctx)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func (p *Platform) handleEvent(ctx context.Context, socket *socketmode.Client, evt socketmode.Event, inbound chan<- *core.Message) {
	if evt.Type != socketmode.EventTypeEventsAPI {
		return
	}
	data, ok := evt.Data.(slackevents.EventsAPIEvent)
	if !ok {
		return
	}
	if evt.Request != nil {
		socket.Ack(*evt.Request)
	}
	if data.Type != slackevents.CallbackEvent {
		return
	}

	var msg *core.Message
	switch ev := data.InnerEvent.Data.(type) {
	case *slackevents.AppMentionEvent:
		if ev.BotID != "" || ev.User == "" || p.duplicate(ev.TimeStamp) {
			return
		}
		text := stripMention(ev.Text)
		if text == "" {
			return
		}
		msg = &core.Message{
			ID: ev.TimeStamp, ChatID: ev.Channel, UserID: ev.User,
			Text: text, Platform: "slack",
		}
	case *slackevents.MessageEvent:
		// Direct messages only; channel messages are handled via app_mention
		// so the bot does not answer every channel conversation.
		if ev.ChannelType != "im" || ev.BotID != "" || ev.User == "" || ev.Text == "" {
			return
		}
		if p.duplicate(ev.TimeStamp) {
			return
		}
		msg = &core.Message{
			ID: ev.TimeStamp, ChatID: ev.Channel, UserID: ev.User,
			Text: ev.Text, Platform: "slack",
		}
	default:
		return
	}

	select {
	case inbound <- msg:
	case <-ctx.Done():
	}
}

func (p *Platform) duplicate(ts string) bool {
	if ts == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.seen[ts] {
		return true
	}
	if len(p.seen) > 2048 {
		p.seen = map[string]bool{}
	}
	p.seen[ts] = true
	return false
}

// Reply sends text back to the originating channel.
func (p *Platform) Reply(ctx context.Context, msg *core.Message, text string) error {
	return p.Send(ctx, msg.ChatID, text)
}

// Send delivers text to a channel or DM id.
func (p *Platform) Send(ctx context.Context, chatID, text string) error {
	if chatID == "" {
		return fmt.Errorf("slack: empty chat id")
	}
	p.mu.Lock()
	client := p.client
	p.mu.Unlock()
	if client == nil {
		client = slackapi.New(p.botToken, slackapi.OptionAppLevelToken(p.appToken))
	}
	_, _, err := client.PostMessageContext(ctx, chatID, slackapi.MsgOptionText(text, false))
	if err != nil {
		return fmt.Errorf("slack send: %w", err)
	}
	return nil
}

// Stop is a no-op; RunContext exits on ctx cancellation.
func (p *Platform) Stop(ctx context.Context) error { return nil }

// stripMention removes the leading <@BOTID> from an app_mention text.
func stripMention(text string) string {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "<@") {
		if idx := strings.Index(text, ">"); idx != -1 {
			return strings.TrimSpace(text[idx+1:])
		}
	}
	return text
}
