package liveturns

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// blockingTurn emits started and one output, then waits for release or
// cancellation.
func blockingTurn(release <-chan string, cancelled chan<- struct{}) Invoke {
	return func(ctx context.Context, sink core.InvocationEventSink) (core.InvocationResult, error) {
		_ = sink(core.InvocationStreamEvent{Type: "started", InvocationID: "inv_1"})
		_ = sink(core.InvocationStreamEvent{Type: "output", InvocationID: "inv_1", Text: "working"})
		select {
		case answer := <-release:
			result := core.InvocationResult{ID: "inv_1", Answer: answer}
			_ = sink(core.InvocationStreamEvent{Type: "completed", InvocationID: "inv_1", Final: true, Result: &result})
			return result, nil
		case <-ctx.Done():
			close(cancelled)
			return core.InvocationResult{ID: "inv_1"}, ctx.Err()
		}
	}
}

type frame struct {
	seq   int64
	event core.InvocationStreamEvent
}

// readFrames reads SSE frames until the body ends or stop says so.
func readFrames(t *testing.T, response *http.Response, stop func(frame) bool) []frame {
	t.Helper()
	reader := bufio.NewReader(response.Body)
	var frames []frame
	var current frame
	for {
		line, err := reader.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, "id: "):
			current.seq, _ = strconv.ParseInt(strings.TrimPrefix(line, "id: "), 10, 64)
		case strings.HasPrefix(line, "data: "):
			if jsonErr := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &current.event); jsonErr != nil {
				t.Fatal(jsonErr)
			}
			frames = append(frames, current)
			if stop != nil && stop(current) {
				return frames
			}
			current = frame{}
		}
		if err != nil {
			return frames
		}
	}
}

func TestDetachedTurnOutlivesItsCallerAndIsRejoinedBySequence(t *testing.T) {
	registry := New()
	defer registry.Close()
	release := make(chan string)
	cancelled := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /start", func(w http.ResponseWriter, r *http.Request) {
		id, err := registry.Start(r.Context(), blockingTurn(release, cancelled))
		if err != nil {
			t.Error(err)
			return
		}
		registry.Serve(w, r, id, 0)
	})
	mux.HandleFunc("GET /events/{id}", func(w http.ResponseWriter, r *http.Request) {
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		registry.Serve(w, r, r.PathValue("id"), after)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	// The caller reads the first event and leaves.
	ctx, leave := context.WithCancel(context.Background())
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/start", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	first := readFrames(t, response, func(frame) bool { return true })
	leave()
	response.Body.Close()
	if len(first) != 1 || first[0].seq != 1 || first[0].event.InvocationID != "inv_1" {
		t.Fatalf("first frames = %+v", first)
	}
	select {
	case <-cancelled:
		t.Fatal("the turn must not end when its caller leaves")
	case <-time.After(100 * time.Millisecond):
	}

	// A new caller rejoins after the event it saw.
	rejoined, err := http.Get(server.URL + "/events/inv_1?after=1")
	if err != nil {
		t.Fatal(err)
	}
	defer rejoined.Body.Close()
	if got := rejoined.Header.Get(SeqHeader); got != "2" {
		t.Fatalf("%s = %q, want the last logged event", SeqHeader, got)
	}
	go func() { release <- "all done" }()
	frames := readFrames(t, rejoined, nil)
	if len(frames) != 2 || frames[0].seq != 2 || frames[0].event.Text != "working" ||
		frames[1].event.Type != "completed" || frames[1].event.Result.Answer != "all done" {
		t.Fatalf("rejoined frames = %+v", frames)
	}

	// The outcome stays for a caller that comes back after the end.
	late, err := http.Get(server.URL + "/events/inv_1?after=2")
	if err != nil {
		t.Fatal(err)
	}
	defer late.Body.Close()
	if frames := readFrames(t, late, nil); len(frames) != 1 || frames[0].event.Type != "completed" {
		t.Fatalf("late frames = %+v", frames)
	}
}

func TestDetachedTurnCancelAndUnknownIDs(t *testing.T) {
	registry := New()
	release := make(chan string)
	cancelled := make(chan struct{})
	id, err := registry.Start(context.Background(), blockingTurn(release, cancelled))
	if err != nil || id != "inv_1" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if registry.Cancel("inv_unknown") {
		t.Fatal("an unknown id cannot be cancelled")
	}
	if !registry.Cancel(id) {
		t.Fatal("cancel the running turn")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("cancel did not reach the turn")
	}
	recorder := httptest.NewRecorder()
	registry.Serve(recorder, httptest.NewRequest(http.MethodGet, "/", nil), "inv_unknown", 0)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown id status = %d", recorder.Code)
	}
	// A cancelled turn ends with an error event a rejoining caller sees.
	recorder = httptest.NewRecorder()
	registry.Serve(recorder, httptest.NewRequest(http.MethodGet, "/", nil), id, 2)
	if !strings.Contains(recorder.Body.String(), `"type":"error"`) {
		t.Fatalf("after cancel: %s", recorder.Body.String())
	}

	// Close stops what is still running and forgets it.
	second := make(chan struct{})
	if _, err := registry.Start(context.Background(), blockingTurn(release, second)); err != nil {
		t.Fatal(err)
	}
	registry.Close()
	select {
	case <-second:
	case <-time.After(time.Second):
		t.Fatal("close did not cancel the running turn")
	}
	if _, err := registry.Start(context.Background(), blockingTurn(release, make(chan struct{}))); !errors.Is(err, core.ErrInvocationRuntime) {
		t.Fatalf("start after close: %v", err)
	}
}

func TestStartReturnsAnErrorBeforeTheTurnStarts(t *testing.T) {
	registry := New()
	defer registry.Close()
	_, err := registry.Start(context.Background(), func(context.Context, core.InvocationEventSink) (core.InvocationResult, error) {
		return core.InvocationResult{}, core.ErrInvocationBusy
	})
	if !errors.Is(err, core.ErrInvocationBusy) {
		t.Fatalf("err = %v", err)
	}
}

func TestFinishedTurnsExpire(t *testing.T) {
	registry := New()
	now := time.Now()
	registry.now = func() time.Time { return now }
	release := make(chan string, 1)
	release <- "done"
	id, err := registry.Start(context.Background(), blockingTurn(release, make(chan struct{})))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for !registry.get(id).isDone() {
		if time.Now().After(deadline) {
			t.Fatal("turn did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	now = now.Add(Retention + time.Minute)
	if registry.get(id) != nil {
		t.Fatal("a finished turn is forgotten after Retention")
	}
}

func (t *turn) isDone() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.done
}
