// Package liveturns keeps Agent invocations that outlive the request that
// started them. A caller that asks to detach gets the turn's events as usual,
// but leaving does not stop the turn: the events stay here, numbered, and the
// caller, or a new process of it after a restart, rejoins by invocation id
// and picks up where it left off. Only an explicit cancel, or this process
// stopping, ends a detached turn early.
package liveturns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

const (
	// Retention is how long a finished turn's events stay for its caller to
	// collect.
	Retention = 30 * time.Minute
	// maxEvents bounds one turn's log. Output events are snapshots, so the
	// oldest can go without losing what a rejoining caller needs.
	maxEvents = 4096
	// SeqHeader carries, on a rejoin, the sequence number of the last event
	// logged when the caller joined: events up to it are replayed.
	SeqHeader = "X-AgentMux-Invocation-Seq"
	keepalive = 15 * time.Second
)

// ErrNotFound means no turn has the id: it never existed, it expired, or the
// process that ran it restarted.
var ErrNotFound = errors.New("invocation not found")

// Invoke runs one turn, sending its events to sink. It is
// core.StreamingInvoker.InvokeStream with the request bound.
type Invoke func(ctx context.Context, sink core.InvocationEventSink) (core.InvocationResult, error)

// Registry holds the detached turns of one process.
type Registry struct {
	mu     sync.Mutex
	turns  map[string]*turn
	closed bool
	now    func() time.Time
}

type record struct {
	seq   int64
	event core.InvocationStreamEvent
}

type turn struct {
	cancel context.CancelFunc

	mu      sync.Mutex
	id      string
	events  []record
	next    int64
	done    bool
	ended   time.Time
	changed chan struct{}
}

func New() *Registry {
	return &Registry{turns: map[string]*turn{}, now: time.Now}
}

// Close cancels every running turn. Their callers rejoin to a 404 and fall
// back to their own recovery.
func (r *Registry) Close() {
	r.mu.Lock()
	r.closed = true
	turns := r.turns
	r.turns = map[string]*turn{}
	r.mu.Unlock()
	for _, t := range turns {
		t.cancel()
	}
}

// Start runs invoke detached from parent's cancellation, keeping parent's
// values. It returns the turn's id once the turn has one, or the error that
// ended it before it started, such as a busy conversation.
func (r *Registry) Start(parent context.Context, invoke Invoke) (string, error) {
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return "", fmt.Errorf("%w: the server is stopping", core.ErrInvocationRuntime)
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	t := &turn{cancel: cancel, changed: make(chan struct{})}
	ready := make(chan error, 1)
	var once sync.Once
	register := func(id string) {
		once.Do(func() {
			t.mu.Lock()
			t.id = id
			t.mu.Unlock()
			r.add(id, t)
			ready <- nil
		})
	}
	go func() {
		defer cancel()
		terminal := false
		result, err := invoke(ctx, func(event core.InvocationStreamEvent) error {
			if event.Type == "completed" || event.Type == "error" {
				terminal = true
			}
			t.append(event)
			if event.InvocationID != "" {
				register(event.InvocationID)
			}
			return nil
		})
		switch {
		case terminal:
		case err == nil:
			completed := result
			t.append(core.InvocationStreamEvent{
				Type: "completed", InvocationID: result.ID, ConversationID: result.ConversationID,
				SessionID: result.SessionID, Final: true, DurationMS: result.DurationMS, Result: &completed,
			})
			if result.ID != "" {
				register(result.ID)
			}
		case t.logged() > 0:
			t.append(core.InvocationStreamEvent{
				Type: "error", InvocationID: result.ID, ConversationID: result.ConversationID,
				SessionID: result.SessionID, Error: err.Error(), DurationMS: result.DurationMS,
			})
		}
		t.finish(r.now())
		once.Do(func() {
			if err == nil {
				err = fmt.Errorf("%w: the invocation has no id", core.ErrInvocationRuntime)
			}
			ready <- err
		})
	}()
	if err := <-ready; err != nil {
		return "", err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.id, nil
}

func (r *Registry) add(id string, t *turn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		t.cancel()
		return
	}
	r.sweep()
	r.turns[id] = t
}

// sweep forgets turns finished longer than Retention ago. r.mu is held.
func (r *Registry) sweep() {
	now := r.now()
	for id, t := range r.turns {
		t.mu.Lock()
		expired := t.done && now.Sub(t.ended) > Retention
		t.mu.Unlock()
		if expired {
			delete(r.turns, id)
		}
	}
}

func (r *Registry) get(id string) *turn {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweep()
	return r.turns[id]
}

// Cancel stops a running turn. It reports false for an unknown id; a turn
// that already ended is left as it is.
func (r *Registry) Cancel(id string) bool {
	t := r.get(id)
	if t == nil {
		return false
	}
	t.cancel()
	return true
}

func (t *turn) append(event core.InvocationStreamEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.next++
	t.events = append(t.events, record{seq: t.next, event: event})
	if len(t.events) > maxEvents {
		t.events = append([]record(nil), t.events[len(t.events)-maxEvents*3/4:]...)
	}
	close(t.changed)
	t.changed = make(chan struct{})
}

func (t *turn) logged() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.next
}

func (t *turn) finish(at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.done, t.ended = true, at
	close(t.changed)
	t.changed = make(chan struct{})
}

// after returns the events after seq, whether the turn has ended, and a
// channel closed at the next change.
func (t *turn) after(seq int64) ([]record, bool, <-chan struct{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []record
	for _, rec := range t.events {
		if rec.seq > seq {
			out = append(out, rec)
		}
	}
	return out, t.done, t.changed
}

// Serve streams a turn's events after seq as Server-Sent Events, each with
// its sequence number as the SSE id, until the turn ends or the client
// leaves. The client leaving does not affect the turn.
func (r *Registry) Serve(w http.ResponseWriter, req *http.Request, id string, after int64) {
	t := r.get(id)
	if t == nil {
		writeError(w, http.StatusNotFound, ErrNotFound.Error())
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is unsupported by this HTTP server")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set(SeqHeader, strconv.FormatInt(t.logged(), 10))
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	ticker := time.NewTicker(keepalive)
	defer ticker.Stop()
	for {
		records, done, changed := t.after(after)
		for _, rec := range records {
			if err := WriteEvent(w, rec.seq, rec.event); err != nil {
				return
			}
			after = rec.seq
		}
		flusher.Flush()
		if done {
			return
		}
		select {
		case <-changed:
		case <-ticker.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-req.Context().Done():
			return
		}
	}
}

// WriteEvent writes one SSE frame. A seq of zero writes no id.
func WriteEvent(w io.Writer, seq int64, event core.InvocationStreamEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if seq > 0 {
		if _, err := fmt.Fprintf(w, "id: %d\n", seq); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventName(event.Type), payload)
	return err
}

func eventName(value string) string {
	if value == "" {
		return "event"
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' {
			continue
		}
		return "event"
	}
	return value
}

func writeError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
