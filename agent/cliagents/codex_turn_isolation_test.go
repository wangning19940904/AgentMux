package cliagents

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// Exercise the real shared reader and RPC transport, including notifications
// arriving before the turn/start reply and after a cancelled consumer exits.
type turnTestServer struct {
	client *codexAppClient
	out    *io.PipeWriter
	mu     sync.Mutex
}

func newTurnTestServer(t *testing.T, handle func(*turnTestServer, map[string]any)) *turnTestServer {
	t.Helper()
	requestReader, requestWriter := io.Pipe()
	responseReader, responseWriter := io.Pipe()
	f := &turnTestServer{out: responseWriter}
	f.client = &codexAppClient{
		stdin: requestWriter, reader: bufio.NewReader(responseReader), nextID: 1,
		pending: map[int]chan codexRPCResponse{}, sessions: map[string]*codexSession{}, done: make(chan struct{}),
	}
	done := make(chan struct{})
	go f.client.readLoop()
	go func() {
		defer close(done)
		decoder := json.NewDecoder(requestReader)
		for {
			var request map[string]any
			if decoder.Decode(&request) != nil {
				return
			}
			handle(f, request)
		}
	}()
	t.Cleanup(func() {
		_ = f.client.close()
		_ = requestReader.Close()
		_ = responseReader.Close()
		_ = responseWriter.Close()
		<-done
	})
	return f
}

func (f *turnTestServer) send(message map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = json.NewEncoder(f.out).Encode(message)
}

func (f *turnTestServer) reply(request map[string]any, result map[string]any) {
	f.send(map[string]any{"id": request["id"], "result": result})
}

func (f *turnTestServer) event(method, turnID string, extra map[string]any) {
	params := map[string]any{"threadId": "thread", "turnId": turnID}
	for key, value := range extra {
		params[key] = value
	}
	f.send(map[string]any{"method": method, "params": params})
}

func TestAppServerCancelledTurnCannotPoisonNextTurn(t *testing.T) {
	for _, runtimeID := range []string{"codex", "traecli"} {
		t.Run(runtimeID, func(t *testing.T) {
			starts := 0
			declined := make(chan bool, 1)
			f := newTurnTestServer(t, func(f *turnTestServer, request map[string]any) {
				switch request["method"] {
				case "turn/start":
					starts++
					if starts == 1 {
						f.reply(request, map[string]any{"turn": map[string]any{"id": "old"}})
						f.event("item/agentMessage/delta", "old", map[string]any{"delta": "old output"})
						return
					}
					// These are late events from the stopped turn. They arrive
					// before the new RPC reply, so admission cannot know its ID yet.
					f.event("turn/completed", "old", map[string]any{"turn": map[string]any{"id": "old", "status": "interrupted"}})
					f.event("error", "old", map[string]any{"error": map[string]any{"message": "old failure"}})
					f.send(map[string]any{"id": 900, "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": "thread", "turnId": "old"}})
					// More than the original 256-slot inbox, before the RPC
					// response. A blocking route would deadlock the shared reader.
					for range 1024 {
						f.event("item/agentMessage/delta", "new", map[string]any{"delta": "x"})
					}
					f.reply(request, map[string]any{"turn": map[string]any{"id": "new"}})
					f.event("turn/completed", "new", map[string]any{"turn": map[string]any{"id": "new", "status": "completed"}})
				case "turn/interrupt", "model/list":
					f.reply(request, map[string]any{})
				default:
					if id, ok := rpcID(request); ok && id == 900 {
						result, _ := request["result"].(map[string]any)
						declined <- result["decision"] == "decline"
					}
				}
			})
			s := &codexSession{agent: &codexAgent{runtimeID: runtimeID}, client: f.client, threadID: "thread"}
			if err := f.client.register("thread", s); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			firstCtx, stop := context.WithCancel(ctx)
			defer stop()
			first, err := s.Send(firstCtx, "first")
			if err != nil {
				t.Fatal(err)
			}
			cancelled := false
			for event := range first {
				if event.Type == core.EventOutput {
					stop()
				}
				cancelled = cancelled || errors.Is(event.Err, context.Canceled)
			}
			if !cancelled {
				t.Fatal("first turn was not cancelled")
			}
			// Late notifications for an idle session must not fill its inbox.
			for range codexInboxLimit + 1 {
				f.client.routeServerMessage(map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{"threadId": "thread", "turnId": "old", "delta": "ignored"}})
			}
			if _, err := f.client.call(ctx, "model/list", nil); err != nil {
				t.Fatalf("shared reader blocked after cancellation: %v", err)
			}
			second, err := s.Send(ctx, "second")
			if err != nil {
				t.Fatal(err)
			}
			var final string
			for event := range second {
				if event.Err != nil || event.Type == core.EventPermission || event.TurnID == "old" {
					t.Fatalf("old turn leaked into new turn: %+v", event)
				}
				if event.Final {
					final = event.Text
				}
			}
			if final != strings.Repeat("x", 1024) {
				t.Fatalf("new turn lost output: got %d characters", len(final))
			}
			select {
			case ok := <-declined:
				if !ok {
					t.Fatal("stale approval request was not declined")
				}
			case <-ctx.Done():
				t.Fatal("stale approval request was left pending")
			}
		})
	}
}

func TestAppServerInboxOverflowDoesNotBlockRPC(t *testing.T) {
	f := newTurnTestServer(t, func(f *turnTestServer, request map[string]any) {
		if request["method"] == "turn/start" {
			for range codexInboxLimit + 1 {
				f.event("item/agentMessage/delta", "new", map[string]any{"delta": "x"})
			}
			f.reply(request, map[string]any{"turn": map[string]any{"id": "new"}})
		} else {
			f.reply(request, map[string]any{})
		}
	})
	s := &codexSession{agent: &codexAgent{}, client: f.client, threadID: "thread"}
	if err := f.client.register("thread", s); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, err := s.Send(ctx, "burst")
	if err != nil {
		t.Fatal(err)
	}
	var failure error
	for event := range events {
		if event.Err != nil {
			failure = event.Err
		}
	}
	if failure == nil || !strings.Contains(failure.Error(), "notification backlog exceeded") {
		t.Fatalf("overflow error = %v", failure)
	}
	if _, err := f.client.call(ctx, "model/list", nil); err != nil {
		t.Fatalf("overflow blocked shared RPC: %v", err)
	}
}

func TestAppServerStopWithBlockedOutputReleasesTurn(t *testing.T) {
	interrupted := make(chan struct{}, 1)
	f := newTurnTestServer(t, func(f *turnTestServer, request map[string]any) {
		if request["method"] == "turn/start" {
			f.reply(request, map[string]any{"turn": map[string]any{"id": "active"}})
		} else {
			f.reply(request, map[string]any{})
			if request["method"] == "turn/interrupt" {
				interrupted <- struct{}{}
			}
		}
	})
	s := &codexSession{agent: &codexAgent{}, client: f.client, threadID: "thread"}
	if err := f.client.register("thread", s); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Simulate a channel renderer that has stopped consuming output.
	out := make(chan *core.Event)
	done := make(chan struct{})
	go func() {
		s.runTurn(ctx, core.AgentTurnInput{Text: "hello"}, out)
		close(done)
	}()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for s.ActiveTurnID() == "" {
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("turn never started")
		}
	}
	cancel()
	select {
	case <-done:
	case <-deadline.C:
		t.Fatal("cancelled turn remained blocked writing output")
	}
	select {
	case <-interrupted:
	case <-deadline.C:
		t.Fatal("backend turn was not interrupted")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeTurn || s.inbox != nil {
		t.Fatal("cancelled turn retained its inbox")
	}
}
