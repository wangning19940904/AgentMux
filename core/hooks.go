package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"time"
)

// HookEvent is a lifecycle event that can trigger a shell command or webhook.
type HookEvent string

const (
	HookMessageReceived  HookEvent = "message.received"
	HookMessageSent      HookEvent = "message.sent"
	HookSessionStarted   HookEvent = "session.started"
	HookSessionEnded     HookEvent = "session.ended"
	HookCronTriggered    HookEvent = "cron.triggered"
	HookWebhookTriggered HookEvent = "webhook.triggered"
	HookPermission       HookEvent = "permission.requested"
	HookError            HookEvent = "error"
)

// HookEvents lists all lifecycle events, for UIs that offer a picker.
func HookEvents() []HookEvent {
	return []HookEvent{
		HookMessageReceived, HookMessageSent, HookSessionStarted,
		HookSessionEnded, HookCronTriggered, HookWebhookTriggered,
		HookPermission, HookError,
	}
}

// Hook is a single configured lifecycle reaction.
type Hook struct {
	Event   HookEvent
	Type    string // "shell" or "http"
	Command string
	URL     string
}

// HookRunner fires configured hooks asynchronously.
type HookRunner struct {
	log   *slog.Logger
	hooks []Hook
}

// NewHookRunner builds a runner from the configured hooks.
func NewHookRunner(log *slog.Logger, hooks []Hook) *HookRunner {
	if log == nil {
		log = slog.Default()
	}
	return &HookRunner{log: log, hooks: hooks}
}

// Fire dispatches all hooks matching event. Non-blocking.
func (r *HookRunner) Fire(ctx context.Context, event HookEvent, data map[string]string) {
	if r == nil {
		return
	}
	for _, h := range r.hooks {
		if h.Event != event {
			continue
		}
		h := h
		go r.run(ctx, h, data)
	}
}

func (r *HookRunner) run(ctx context.Context, h Hook, data map[string]string) {
	if err := RunHookAction(ctx, h.Type, h.Command, h.URL, data); err != nil {
		r.log.Error("hook failed", "event", h.Event, "type", h.Type, "err", err)
	}
}

// RunHookAction executes one hook-style action: a shell command with HOOK_*
// env vars, or an HTTP POST with a JSON body. Shared by config.toml hooks and
// store-managed event triggers.
func RunHookAction(ctx context.Context, typ, command, url string, data map[string]string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	switch typ {
	case ActionShell:
		cmd := exec.CommandContext(ctx, "sh", "-c", command)
		cmd.Env = hookEnv(data)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("shell: %w (output: %s)", err, string(out))
		}
		return nil
	case ActionHTTP:
		body, err := json.Marshal(data)
		if err != nil {
			return fmt.Errorf("http body: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("http build: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if ev, ok := data["event"]; ok {
			req.Header.Set("X-Hook-Event", ev)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("http post: %w", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("http post: %s", resp.Status)
		}
		return nil
	default:
		return fmt.Errorf("unknown hook action type %q", typ)
	}
}

func hookEnv(data map[string]string) []string {
	env := []string{}
	for k, v := range data {
		env = append(env, "HOOK_"+upper(k)+"="+v)
	}
	return env
}

func upper(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'a' && b[i] <= 'z' {
			b[i] -= 32
		}
		if b[i] == '.' {
			b[i] = '_'
		}
	}
	return string(b)
}
