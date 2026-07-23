package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"
)

// HookEvent is a lifecycle event that can trigger a shell command or webhook.
type HookEvent string

const (
	HookMessageReceived     HookEvent = "message.received"
	HookMessageSent         HookEvent = "message.sent"
	HookSessionStarted      HookEvent = "session.started"
	HookSessionEnded        HookEvent = "session.ended"
	HookCronTriggered       HookEvent = "cron.triggered"
	HookWebhookTriggered    HookEvent = "webhook.triggered"
	HookPermission          HookEvent = "permission.requested"
	HookTaskQueued          HookEvent = "task.queued"
	HookTaskStarted         HookEvent = "task.started"
	HookTaskSteered         HookEvent = "task.steered"
	HookTaskTakenOver       HookEvent = "task.controller_changed"
	HookTaskInterrupted     HookEvent = "task.interrupted"
	HookTaskCompleted       HookEvent = "task.completed"
	HookInteractionResolved HookEvent = "interaction.resolved"
	HookThreadBound         HookEvent = "thread.bound"
	HookError               HookEvent = "error"
)

// HookEvents lists all lifecycle events, for UIs that offer a picker.
func HookEvents() []HookEvent {
	return []HookEvent{
		HookMessageReceived, HookMessageSent, HookSessionStarted,
		HookSessionEnded, HookCronTriggered, HookWebhookTriggered,
		HookPermission, HookTaskQueued, HookTaskStarted, HookTaskSteered, HookTaskTakenOver,
		HookTaskInterrupted, HookTaskCompleted, HookInteractionResolved,
		HookThreadBound, HookError,
	}
}

// Hook is a single configured lifecycle reaction.
type Hook struct {
	Event   HookEvent
	Type    string // "shell" or "http"
	Command string
	URL     string
}

const (
	hookWorkerCount = 4
	hookQueueSize   = 256
)

type hookJob struct {
	ctx   context.Context
	hook  Hook
	data  map[string]string
	start time.Time
}

// HookRun describes one completed legacy shell/HTTP hook. Observability can
// attach a listener without changing the existing hook configuration API.
type HookRun struct {
	Event     HookEvent
	Type      string
	StartedAt time.Time
	Duration  time.Duration
	Data      map[string]string
	Err       error
}

// HookRunObserver receives completed hook attempts. It must return quickly;
// HookRunner invokes it outside its queue lock.
type HookRunObserver func(HookRun)

// HookRunner fires configured hooks asynchronously through a bounded worker
// pool. Hook overload is fail-open: agent execution is never blocked.
type HookRunner struct {
	log      *slog.Logger
	hooks    []Hook
	queue    chan hookJob
	start    sync.Once
	mu       sync.RWMutex
	observer HookRunObserver
}

// NewHookRunner builds a runner from the configured hooks.
func NewHookRunner(log *slog.Logger, hooks []Hook) *HookRunner {
	if log == nil {
		log = slog.Default()
	}
	r := &HookRunner{log: log, hooks: hooks, queue: make(chan hookJob, hookQueueSize)}
	r.ensureWorkers()
	return r
}

func (r *HookRunner) ensureWorkers() {
	if r == nil {
		return
	}
	r.start.Do(func() {
		for range hookWorkerCount {
			go func() {
				for job := range r.queue {
					r.run(job)
				}
			}()
		}
	})
}

// SetObserver attaches an optional completion listener.
func (r *HookRunner) SetObserver(observer HookRunObserver) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.observer = observer
	r.mu.Unlock()
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
		copyData := cloneHookData(data)
		job := hookJob{ctx: context.WithoutCancel(ctx), hook: h, data: copyData, start: time.Now().UTC()}
		select {
		case r.queue <- job:
		default:
			r.log.Warn("hook queue full; dropping observation action", "event", event, "type", h.Type)
		}
	}
}

func (r *HookRunner) run(job hookJob) {
	err := RunHookAction(job.ctx, job.hook.Type, job.hook.Command, job.hook.URL, job.data)
	if err != nil {
		r.log.Error("hook failed", "event", job.hook.Event, "type", job.hook.Type, "err", err)
	}
	r.mu.RLock()
	observer := r.observer
	r.mu.RUnlock()
	if observer != nil {
		observer(HookRun{
			Event: job.hook.Event, Type: job.hook.Type, StartedAt: job.start,
			Duration: time.Since(job.start), Data: cloneHookData(job.data), Err: err,
		})
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
		body, err := json.Marshal(data)
		if err != nil {
			return fmt.Errorf("shell body: %w", err)
		}
		cmd := exec.CommandContext(ctx, "sh", "-c", command)
		cmd.Env = append(os.Environ(), hookEnv(data)...)
		cmd.Stdin = bytes.NewReader(body)
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

func cloneHookData(data map[string]string) map[string]string {
	out := make(map[string]string, len(data))
	for k, v := range data {
		out[k] = v
	}
	return out
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
