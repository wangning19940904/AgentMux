// Package telegram implements the Telegram platform adapter using the Bot API
// over HTTP long polling — no third-party SDK and no public IP required.
package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/agentnexus/agentnexus/core"
)

func init() {
	core.RegisterPlatform("telegram", func(cfg map[string]any) (core.Platform, error) {
		p := &Platform{client: &http.Client{Timeout: 65 * time.Second}}
		p.token, _ = cfg["token"].(string)
		p.project, _ = cfg["project"].(string)
		if p.token == "" {
			return nil, fmt.Errorf("telegram: token is required")
		}
		if allow, ok := cfg["allow_users"].([]any); ok {
			for _, a := range allow {
				switch v := a.(type) {
				case int64:
					p.allow = append(p.allow, v)
				case float64:
					p.allow = append(p.allow, int64(v))
				}
			}
		}
		return p, nil
	})
}

// Platform is the Telegram adapter.
type Platform struct {
	token   string
	project string
	allow   []int64
	client  *http.Client
	offset  int64
}

// Name returns the registered name.
func (p *Platform) Name() string { return "telegram" }

func (p *Platform) api(method string) string {
	return "https://api.telegram.org/bot" + p.token + "/" + method
}

// Start long-polls getUpdates and forwards inbound text messages.
func (p *Platform) Start(ctx context.Context, inbound chan<- *core.Message) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		updates, err := p.getUpdates(ctx)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(3 * time.Second):
			}
			continue
		}
		for _, u := range updates {
			p.offset = u.UpdateID + 1
			if u.Message == nil || u.Message.Text == "" {
				continue
			}
			if !p.allowed(u.Message.From.ID) {
				continue
			}
			inbound <- &core.Message{
				ChatID:   strconv.FormatInt(u.Message.Chat.ID, 10),
				UserID:   strconv.FormatInt(u.Message.From.ID, 10),
				UserName: u.Message.From.Username,
				Text:     u.Message.Text,
				Platform: "telegram",
				Project:  p.project,
			}
		}
	}
}

func (p *Platform) allowed(userID int64) bool {
	if len(p.allow) == 0 {
		return true
	}
	for _, a := range p.allow {
		if a == userID {
			return true
		}
	}
	return false
}

// Reply sends text back to the originating chat.
func (p *Platform) Reply(ctx context.Context, msg *core.Message, text string) error {
	return p.Send(ctx, msg.ChatID, text)
}

// Send delivers text to a chat id.
func (p *Platform) Send(ctx context.Context, chatID, text string) error {
	if chatID == "" {
		return fmt.Errorf("telegram: empty chat id")
	}
	form := url.Values{}
	form.Set("chat_id", chatID)
	form.Set("text", text)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.api("sendMessage"),
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("telegram send: %s", resp.Status)
	}
	return nil
}

// Stop is a no-op; the poll loop exits on ctx cancellation.
func (p *Platform) Stop(ctx context.Context) error { return nil }

type tgUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		Text string `json:"text"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		From struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		} `json:"from"`
	} `json:"message"`
}

func (p *Platform) getUpdates(ctx context.Context) ([]tgUpdate, error) {
	form := url.Values{}
	form.Set("timeout", "60")
	form.Set("offset", strconv.FormatInt(p.offset, 10))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		p.api("getUpdates")+"?"+form.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		OK     bool       `json:"ok"`
		Result []tgUpdate `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, fmt.Errorf("telegram getUpdates not ok")
	}
	return out.Result, nil
}
