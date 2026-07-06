// Package discord implements the Discord platform adapter using the Gateway
// WebSocket via discordgo — no public IP required. The bot answers direct
// messages always and guild messages only when mentioned (unless
// reply_all=true). Requires the Message Content intent to be enabled in the
// Discord developer portal.
package discord

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/agentnexus/agentnexus/core"
	"github.com/bwmarrin/discordgo"
)

// discordMaxLen keeps messages under Discord's 2000-char hard limit.
const discordMaxLen = 1900

func init() {
	core.RegisterPlatform("discord", func(cfg map[string]any) (core.Platform, error) {
		p := &Platform{}
		p.token, _ = cfg["token"].(string)
		if v, ok := cfg["reply_all"].(bool); ok {
			p.replyAll = v
		} else if v, ok := cfg["reply_all"].(string); ok {
			p.replyAll = v == "true" || v == "1"
		}
		if p.token == "" {
			return nil, fmt.Errorf("discord: token is required")
		}
		return p, nil
	})
}

// Platform is the Discord Gateway adapter.
type Platform struct {
	token    string
	replyAll bool

	mu      sync.Mutex
	session *discordgo.Session
}

// Name returns the registered name.
func (p *Platform) Name() string { return "discord" }

// Start opens the Gateway connection and forwards inbound messages until ctx
// is cancelled.
func (p *Platform) Start(ctx context.Context, inbound chan<- *core.Message) error {
	session, err := discordgo.New("Bot " + p.token)
	if err != nil {
		return fmt.Errorf("discord: %w", err)
	}
	session.Identify.Intents = discordgo.IntentsGuildMessages |
		discordgo.IntentsDirectMessages | discordgo.IntentMessageContent

	session.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		if m.Author == nil || m.Author.Bot || (s.State.User != nil && m.Author.ID == s.State.User.ID) {
			return
		}
		text := strings.TrimSpace(m.Content)
		isDM := m.GuildID == ""
		if !isDM && !p.replyAll {
			mentioned := false
			if s.State.User != nil {
				for _, u := range m.Mentions {
					if u.ID == s.State.User.ID {
						mentioned = true
						break
					}
				}
				text = stripBotMention(text, s.State.User.ID)
			}
			if !mentioned {
				return
			}
		}
		if text == "" {
			return
		}
		msg := &core.Message{
			ID:        m.ID,
			ChatID:    m.ChannelID,
			UserID:    m.Author.ID,
			UserName:  m.Author.Username,
			Text:      text,
			Timestamp: time.Now(),
			Platform:  "discord",
		}
		select {
		case inbound <- msg:
		case <-ctx.Done():
		}
	})

	if err := session.Open(); err != nil {
		return fmt.Errorf("discord open: %w", err)
	}
	p.mu.Lock()
	p.session = session
	p.mu.Unlock()

	<-ctx.Done()
	return session.Close()
}

// Reply sends text back to the originating channel.
func (p *Platform) Reply(ctx context.Context, msg *core.Message, text string) error {
	return p.Send(ctx, msg.ChatID, text)
}

// Send delivers text to a channel id, splitting to respect Discord's length
// limit.
func (p *Platform) Send(ctx context.Context, chatID, text string) error {
	if chatID == "" {
		return fmt.Errorf("discord: empty chat id")
	}
	p.mu.Lock()
	session := p.session
	p.mu.Unlock()
	if session == nil {
		return fmt.Errorf("discord: not connected")
	}
	for _, chunk := range splitMessage(text, discordMaxLen) {
		if _, err := session.ChannelMessageSend(chatID, chunk, discordgo.WithContext(ctx)); err != nil {
			return fmt.Errorf("discord send: %w", err)
		}
	}
	return nil
}

// Stop is a no-op; the Start goroutine closes the session on ctx cancellation.
func (p *Platform) Stop(ctx context.Context) error { return nil }

func stripBotMention(text, botID string) string {
	for _, tag := range []string{"<@" + botID + ">", "<@!" + botID + ">"} {
		text = strings.ReplaceAll(text, tag, "")
	}
	return strings.TrimSpace(text)
}

// splitMessage cuts text into chunks of at most limit runes, preferring
// newline boundaries.
func splitMessage(text string, limit int) []string {
	if text == "" {
		return nil
	}
	var out []string
	runes := []rune(text)
	for len(runes) > limit {
		cut := limit
		for i := limit; i > limit/2; i-- {
			if runes[i-1] == '\n' {
				cut = i
				break
			}
		}
		out = append(out, strings.TrimRight(string(runes[:cut]), "\n"))
		runes = runes[cut:]
	}
	if len(runes) > 0 {
		out = append(out, string(runes))
	}
	return out
}
