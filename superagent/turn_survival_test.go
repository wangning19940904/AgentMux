package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/liveturns"
	"github.com/wangning19940904/AgentMux/remote"
)

// remoteDaemon stands in for another machine's AgentMux: it serves the
// invocation API with detached turns, as server does, around a fake Agent
// whose turns wait for an answer.
type remoteDaemon struct {
	t       *testing.T
	turns   *liveturns.Registry
	answers chan string

	mu       sync.Mutex
	inputs   []string
	ctxs     []context.Context
	cancels  int
	detached []bool
	// breakAfterStart drops the first stream right after the turn starts.
	breakAfterStart bool
}

func newRemoteDaemon(t *testing.T) *remoteDaemon {
	d := &remoteDaemon{t: t, turns: liveturns.New(), answers: make(chan string, 1)}
	t.Cleanup(d.turns.Close)
	return d
}

func (d *remoteDaemon) turn(req core.InvocationRequest) liveturns.Invoke {
	return func(ctx context.Context, sink core.InvocationEventSink) (core.InvocationResult, error) {
		d.mu.Lock()
		d.inputs = append(d.inputs, req.Input)
		d.ctxs = append(d.ctxs, ctx)
		id := fmt.Sprintf("inv_%d", len(d.inputs))
		d.mu.Unlock()
		_ = sink(core.InvocationStreamEvent{Type: "started", InvocationID: id, ConversationID: req.ConversationID})
		_ = sink(core.InvocationStreamEvent{Type: "tool_use", InvocationID: id, ToolName: "Bash", ToolCallID: "call_1", ToolInput: "make test"})
		select {
		case answer := <-d.answers:
			result := core.InvocationResult{ID: id, ConversationID: req.ConversationID, Answer: answer}
			_ = sink(core.InvocationStreamEvent{Type: "completed", InvocationID: id, Final: true, Result: &result})
			return result, nil
		case <-ctx.Done():
			return core.InvocationResult{ID: id}, ctx.Err()
		}
	}
}

func (d *remoteDaemon) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/agent-instances", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]core.AgentInstance{{ID: "remote-coder", Name: "Remote coder", Enabled: true, RuntimeID: "codex"}})
	})
	mux.HandleFunc("POST /api/v1/invocations/stream", func(w http.ResponseWriter, r *http.Request) {
		var req core.InvocationRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		d.mu.Lock()
		d.detached = append(d.detached, r.URL.Query().Get("detach") == "1")
		drop := d.breakAfterStart
		d.breakAfterStart = false
		d.mu.Unlock()
		id, err := d.turns.Start(r.Context(), d.turn(req))
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if drop {
			// Send the first events, then lose the connection.
			ctx, cancel := context.WithTimeout(r.Context(), 200*time.Millisecond)
			defer cancel()
			d.turns.Serve(w, r.WithContext(ctx), id, 0)
			panic(http.ErrAbortHandler)
		}
		d.turns.Serve(w, r, id, 0)
	})
	mux.HandleFunc("GET /api/v1/invocations/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		d.turns.Serve(w, r, r.PathValue("id"), after)
	})
	mux.HandleFunc("POST /api/v1/invocations/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		d.cancels++
		d.mu.Unlock()
		if !d.turns.Cancel(r.PathValue("id")) {
			http.Error(w, `{"error":"invocation not found"}`, http.StatusNotFound)
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not here", http.StatusNotFound)
	})
	return mux
}

func (d *remoteDaemon) started() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.inputs)
}

func (d *remoteDaemon) turnCtx(index int) context.Context {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ctxs[index]
}

// fleetTo returns a Fleet whose host "host-1" is the daemon.
func fleetTo(t *testing.T, handler http.Handler) *Fleet {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &Fleet{Agents: newMemoryStore(), Hosts: &fakeDialer{address: server.Listener.Addr().String(), host: remote.Host{ID: "host-1", Name: "devbox", RemoteAddr: "127.0.0.1:8765"}}}
}

var remoteWorker = core.SuperAgentWorker{ID: "remote", HostID: "host-1", AgentID: "remote-coder"}

func waitFor(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRemoteTurnContinuesAcrossARestartMidTurn(t *testing.T) {
	h := newHarness(t)
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("remote"), nil }
	daemon := newRemoteDaemon(t)
	handler := daemon.handler()

	first := New(Options{Store: h.store, Invoker: h.router, Workers: fleetTo(t, handler), Replies: h.replies, StateDir: h.service.state})
	h.service = first
	firstCtx, stopFirst := context.WithCancel(context.Background())
	if err := first.Start(firstCtx); err != nil {
		t.Fatal(err)
	}
	h.ingest("alice", "t1", "work", true)
	waitFor(t, "the remote turn to start and be recorded", func() bool {
		tickets := h.store.allTickets()
		return daemon.started() == 1 && len(tickets) == 1 && tickets[0].Turn != nil
	})
	if turn := h.store.allTickets()[0].Turn; turn.InvocationID != "inv_1" || turn.HostID != "host-1" || turn.Delivered != 1 {
		t.Fatalf("turn = %+v", turn)
	}

	// AgentMux stops mid-turn, as for an update.
	stopFirst()
	first.Stop()
	ticket := h.store.allTickets()[0]
	if ticket.Status != core.SuperAgentTicketRunning || ticket.Turn == nil {
		t.Fatalf("after the stop, ticket = %+v", ticket)
	}
	time.Sleep(50 * time.Millisecond)
	if err := daemon.turnCtx(0).Err(); err != nil {
		t.Fatalf("the remote turn must keep running while AgentMux is away: %v", err)
	}

	// A new process starts, with a new Fleet, and rejoins the same turn.
	second := New(Options{Store: h.store, Invoker: h.router, Workers: fleetTo(t, handler), Replies: h.replies, StateDir: h.service.state})
	h.service = second
	secondCtx, stopSecond := context.WithCancel(context.Background())
	defer stopSecond()
	if err := second.Start(secondCtx); err != nil {
		t.Fatal(err)
	}
	defer second.Stop()
	waitFor(t, "the rejoined turn to be running", func() bool {
		second.mu.Lock()
		defer second.mu.Unlock()
		return len(second.running) == 1
	})
	if status := h.store.allTickets()[0].Status; status != core.SuperAgentTicketRunning {
		t.Fatalf("a rejoined ticket stays running, got %q", status)
	}
	daemon.answers <- "final answer"
	waitFor(t, "the ticket to finish", func() bool {
		return h.store.allTickets()[0].Status == core.SuperAgentTicketReady
	})

	ticket = h.store.allTickets()[0]
	if daemon.started() != 1 {
		t.Fatalf("the turn must continue, not start over: remote inputs = %q", daemon.inputs)
	}
	if ticket.Output != "final answer" || ticket.Turn != nil || len(ticket.Inputs) != 0 {
		t.Fatalf("ticket = %+v", ticket)
	}
	waitFor(t, "the answer to reach the thread", func() bool {
		return strings.Contains(strings.Join(h.replies.sent(), "\n"), "final answer")
	})
	for _, text := range h.replies.sent() {
		if strings.Contains(text, "restarted") {
			t.Fatalf("nobody should hear about the restart: %q", h.replies.sent())
		}
	}
	if !daemon.detached[0] {
		t.Fatal("the hub must ask the remote to detach the turn")
	}
	// The tool call before the restart is in the progress log once.
	if log := readTicketFile(t, second, ticket, "progress.md"); strings.Count(log, "make test") != 1 {
		t.Fatalf("progress log = %q", log)
	}
}

func TestRestartFallsBackWhenTheRemoteLostTheTurn(t *testing.T) {
	h := newHarness(t)
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("remote"), nil }
	daemon := newRemoteDaemon(t)
	handler := daemon.handler()
	first := New(Options{Store: h.store, Invoker: h.router, Workers: fleetTo(t, handler), Replies: h.replies, StateDir: h.service.state})
	h.service = first
	firstCtx, stopFirst := context.WithCancel(context.Background())
	if err := first.Start(firstCtx); err != nil {
		t.Fatal(err)
	}
	h.ingest("alice", "t1", "work", true)
	waitFor(t, "the remote turn to be recorded", func() bool {
		tickets := h.store.allTickets()
		return len(tickets) == 1 && tickets[0].Turn != nil
	})
	stopFirst()
	first.Stop()
	// The remote machine restarted too: its turns are gone.
	daemon.turns.Close()
	daemon.turns = liveturns.New()

	second := New(Options{Store: h.store, Invoker: h.router, Workers: fleetTo(t, daemon.handler()), Replies: h.replies, StateDir: h.service.state})
	h.service = second
	secondCtx, stopSecond := context.WithCancel(context.Background())
	defer stopSecond()
	if err := second.Start(secondCtx); err != nil {
		t.Fatal(err)
	}
	defer second.Stop()
	waitFor(t, "the turn to start over", func() bool { return daemon.started() == 2 })
	daemon.answers <- "redone"
	waitFor(t, "the ticket to finish", func() bool {
		return h.store.allTickets()[0].Status == core.SuperAgentTicketReady
	})
	daemon.mu.Lock()
	defer daemon.mu.Unlock()
	if !strings.Contains(daemon.inputs[1], "restarted") || !strings.Contains(daemon.inputs[1], "work") {
		t.Fatalf("the second turn gets the recovery note and the work: %q", daemon.inputs[1])
	}
}

func TestRemoteTurnIsRejoinedWhenItsStreamBreaks(t *testing.T) {
	daemon := newRemoteDaemon(t)
	daemon.breakAfterStart = true
	fleet := fleetTo(t, daemon.handler())
	var started []string
	var progress []string
	var mu sync.Mutex
	go func() {
		waitFor(t, "the stream to break", func() bool {
			daemon.mu.Lock()
			defer daemon.mu.Unlock()
			return !daemon.breakAfterStart && len(daemon.inputs) == 1
		})
		time.Sleep(300 * time.Millisecond)
		daemon.answers <- "made it"
	}()
	answer, err := fleet.Run(context.Background(), remoteWorker, "conv-1", "work", RunOptions{
		Started: func(id string) { started = append(started, id) },
		Progress: func(event core.InvocationStreamEvent) {
			mu.Lock()
			progress = append(progress, event.Type)
			mu.Unlock()
		},
	})
	if err != nil || answer != "made it" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	if daemon.started() != 1 || len(started) != 1 || started[0] != "inv_1" {
		t.Fatalf("one turn, recorded once: inputs=%q started=%q", daemon.inputs, started)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(progress, ",") != "started,tool_use,completed" {
		t.Fatalf("each event is reported once: %q", progress)
	}
}

func TestEndingARemoteTurnCancelsItUnlessAgentMuxIsStopping(t *testing.T) {
	for _, stopping := range []bool{false, true} {
		t.Run(fmt.Sprintf("stopping=%v", stopping), func(t *testing.T) {
			daemon := newRemoteDaemon(t)
			fleet := fleetTo(t, daemon.handler())
			daemonCtx, stopDaemon := context.WithCancel(context.Background())
			defer stopDaemon()
			runCtx, stopRun := context.WithCancel(daemonCtx)
			go func() {
				waitFor(t, "the turn to start", func() bool { return daemon.started() == 1 })
				if stopping {
					stopDaemon()
				} else {
					stopRun()
				}
			}()
			_, err := fleet.Run(runCtx, remoteWorker, "conv-1", "work", RunOptions{Detach: daemonCtx})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v", err)
			}
			daemon.mu.Lock()
			cancels := daemon.cancels
			daemon.mu.Unlock()
			if stopping {
				time.Sleep(50 * time.Millisecond)
				if cancels != 0 || daemon.turnCtx(0).Err() != nil {
					t.Fatal("a turn must outlive AgentMux stopping")
				}
				return
			}
			if cancels != 1 {
				t.Fatalf("a stopped ticket cancels its remote turn; cancels = %d", cancels)
			}
			waitFor(t, "the remote turn to end", func() bool { return daemon.turnCtx(0).Err() != nil })
		})
	}
}

func TestResumeReportsATurnTheRemoteNoLongerHas(t *testing.T) {
	daemon := newRemoteDaemon(t)
	fleet := fleetTo(t, daemon.handler())
	if _, err := fleet.Resume(context.Background(), remoteWorker, "inv_9", RunOptions{}); !errors.Is(err, ErrTurnGone) {
		t.Fatalf("err = %v", err)
	}
	// An older relay or daemon refuses the route instead.
	old := fleetTo(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"only the worker API is relayed"}`, http.StatusForbidden)
	}))
	if _, err := old.Resume(context.Background(), remoteWorker, "inv_9", RunOptions{}); !errors.Is(err, ErrTurnGone) {
		t.Fatalf("old daemon err = %v", err)
	}
}

func TestRelayAllowsRejoiningAndCancellingInvocations(t *testing.T) {
	for _, allowed := range []string{"GET /api/v1/invocations/inv_1/events", "POST /api/v1/invocations/inv_1/cancel"} {
		method, path, _ := strings.Cut(allowed, " ")
		if !RelayAllowed(method, path) {
			t.Errorf("%s must be relayed", allowed)
		}
	}
	for _, refused := range []string{"POST /api/v1/invocations/inv_1/events", "GET /api/v1/invocations/inv_1/cancel", "GET /api/v1/invocations/a/b/events", "GET /api/v1/invocations//events"} {
		method, path, _ := strings.Cut(refused, " ")
		if RelayAllowed(method, path) {
			t.Errorf("%s must not be relayed", refused)
		}
	}
}

func readTicketFile(t *testing.T, s *Service, ticket core.SuperAgentTicket, name string) string {
	t.Helper()
	dir := s.existingTicketDir(ticket)
	if dir == "" {
		t.Fatal("the ticket has no directory")
	}
	data, _ := os.ReadFile(filepath.Join(dir, name))
	return string(data)
}

func TestAStoppedOrRestartedTicketForgetsItsEarlierTurn(t *testing.T) {
	h := newHarness(t)
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("remote"), nil }
	turns := make(chan *core.SuperAgentTicketTurn, 2)
	h.workers.run = func(ctx context.Context, _ workerCall) (string, error) {
		ticket := h.store.allTickets()[0]
		turns <- ticket.Turn
		<-ctx.Done()
		return "", ctx.Err()
	}
	h.ingest("alice", "t1", "work", true)
	ctx := context.Background()
	waitFor(t, "the first turn", func() bool { h.service.Pass(ctx); return len(turns) == 1 })
	<-turns
	id := h.store.allTickets()[0].ID
	// The worker named its turn, as a remote one does.
	if err := h.service.mutateTicket(ctx, id, func(ticket *core.SuperAgentTicket) error {
		ticket.Turn = &core.SuperAgentTicketTurn{InvocationID: "inv_old", HostID: "host-1"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.service.StopTicket(ctx, id); err != nil {
		t.Fatal(err)
	}
	if turn := h.store.allTickets()[0].Turn; turn != nil {
		t.Fatalf("a stopped ticket keeps no turn to rejoin: %+v", turn)
	}
	// Even a turn record left behind is dropped when the next turn begins.
	h.service.wg.Wait()
	if err := h.service.mutateTicket(ctx, id, func(ticket *core.SuperAgentTicket) error {
		ticket.Status = core.SuperAgentTicketQueued
		ticket.Inputs = []string{"again"}
		ticket.Turn = &core.SuperAgentTicketTurn{InvocationID: "inv_old", HostID: "host-1"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the second turn", func() bool { h.service.Pass(ctx); return len(turns) == 1 })
	if turn := <-turns; turn != nil {
		t.Fatalf("a new turn starts without the old one's id: %+v", turn)
	}
	_ = h.service.StopTicket(ctx, id)
}
