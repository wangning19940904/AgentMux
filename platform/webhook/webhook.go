// Package webhook implements a generic webhook-based platform adapter. It runs
// a small HTTP endpoint that accepts inbound JSON ({"chat_id","user_id","text"})
// and delivers replies by POSTing JSON to a configured outbound URL. This
// provides a no-SDK integration path for platforms reachable via webhooks
// (custom bridges, Slack/Discord incoming webhooks, internal tools) so the set
// of supported platforms is open-ended.
package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func init() {
	core.RegisterPlatform("webhook", func(cfg map[string]any) (core.Platform, error) {
		p := &Platform{client: &http.Client{Timeout: 15 * time.Second}}
		p.listen, _ = cfg["listen"].(string)
		p.outbound, _ = cfg["outbound_url"].(string)
		p.project, _ = cfg["project"].(string)
		if p.listen == "" {
			p.listen = "127.0.0.1:8799"
		}
		return p, nil
	})
}

// Platform is the generic webhook adapter.
type Platform struct {
	listen   string
	outbound string
	project  string
	client   *http.Client
	srv      *http.Server
}

// Name returns the registered name.
func (p *Platform) Name() string { return "webhook" }

// Start runs the inbound HTTP endpoint.
func (p *Platform) Start(ctx context.Context, inbound chan<- *core.Message) error {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /inbound", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ChatID string `json:"chat_id"`
			UserID string `json:"user_id"`
			Text   string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		inbound <- &core.Message{
			ChatID: in.ChatID, UserID: in.UserID, Text: in.Text,
			Platform: "webhook", Project: p.project,
		}
		w.WriteHeader(http.StatusAccepted)
	})
	p.srv = &http.Server{Addr: p.listen, Handler: mux}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = p.srv.Shutdown(shutCtx)
	}()
	if err := p.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Reply posts the response to the outbound URL with the originating chat id.
func (p *Platform) Reply(ctx context.Context, msg *core.Message, text string) error {
	return p.Send(ctx, msg.ChatID, text)
}

// Send posts a JSON payload to the configured outbound URL.
func (p *Platform) Send(ctx context.Context, chatID, text string) error {
	if p.outbound == "" {
		return fmt.Errorf("webhook: no outbound_url configured")
	}
	body, _ := json.Marshal(map[string]string{"chat_id": chatID, "text": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.outbound, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook send: %s", resp.Status)
	}
	return nil
}

// Stop shuts down the endpoint.
func (p *Platform) Stop(ctx context.Context) error {
	if p.srv == nil {
		return nil
	}
	return p.srv.Close()
}
