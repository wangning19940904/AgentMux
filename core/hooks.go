package core

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"os/exec"
	"time"
)

// HookEvent is a lifecycle event that can trigger a shell command or webhook.
type HookEvent string

const (
	HookMessageReceived HookEvent = "message.received"
	HookMessageSent     HookEvent = "message.sent"
	HookSessionStarted  HookEvent = "session.started"
	HookSessionEnded    HookEvent = "session.ended"
	HookCronTriggered   HookEvent = "cron.triggered"
	HookPermission      HookEvent = "permission.requested"
	HookError           HookEvent = "error"
)

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
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	switch h.Type {
	case "shell":
		cmd := exec.CommandContext(ctx, "sh", "-c", h.Command)
		cmd.Env = hookEnv(data)
		if out, err := cmd.CombinedOutput(); err != nil {
			r.log.Error("hook shell failed", "event", h.Event, "err", err, "out", string(out))
		}
	case "http":
		var body bytes.Buffer
		body.WriteString("{")
		first := true
		for k, v := range data {
			if !first {
				body.WriteString(",")
			}
			body.WriteString(`"` + k + `":"` + v + `"`)
			first = false
		}
		body.WriteString("}")
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, &body)
		if err != nil {
			r.log.Error("hook http build", "err", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			r.log.Error("hook http failed", "event", h.Event, "err", err)
			return
		}
		_ = resp.Body.Close()
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
