package superagent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// turnKeeper stands in for this machine's AgentMux invocation path around a
// framework that keeps turns, as a detached Codex app-server does: a turn
// runs apart from the invocation that follows it, so it outlives an AgentMux
// restart and a later process rejoins it. The keeper itself plays the
// app-server, which is why both processes share it.
type turnKeeper struct {
	mu          sync.Mutex
	inputs      []string
	keep        []bool
	turns       map[string]*heldTurn
	interrupted []string
	answers     chan string
}

type heldTurn struct {
	id, session string
	done        chan struct{}
	answer      string
	interrupted bool
}

func newTurnKeeper() *turnKeeper {
	return &turnKeeper{turns: map[string]*heldTurn{}, answers: make(chan string, 1)}
}

func (k *turnKeeper) Invoke(ctx context.Context, req core.InvocationRequest) (core.InvocationResult, error) {
	return k.InvokeStream(ctx, req, nil)
}

func (k *turnKeeper) InvokeStream(ctx context.Context, req core.InvocationRequest, sink core.InvocationEventSink) (core.InvocationResult, error) {
	k.mu.Lock()
	k.inputs = append(k.inputs, req.Input)
	k.keep = append(k.keep, req.KeepTurn)
	turn := &heldTurn{id: fmt.Sprintf("turn_%d", len(k.inputs)), session: "thr-" + req.ConversationID, done: make(chan struct{})}
	k.turns[turn.id] = turn
	k.mu.Unlock()
	go func() {
		select {
		case answer := <-k.answers:
			k.mu.Lock()
			if !turn.interrupted {
				turn.answer = answer
				close(turn.done)
			}
			k.mu.Unlock()
		case <-turn.done:
		}
	}()
	emit := func(event core.InvocationStreamEvent) {
		if sink != nil {
			event.ConversationID, event.SessionID = req.ConversationID, turn.session
			_ = sink(event)
		}
	}
	emit(core.InvocationStreamEvent{Type: "started"})
	emit(core.InvocationStreamEvent{Type: "model_request", TurnID: turn.id, Metadata: map[string]string{core.EventMetadataKeptTurn: "true"}})
	emit(core.InvocationStreamEvent{Type: "tool_use", TurnID: turn.id, ToolName: "Bash", ToolCallID: "call_1", ToolInput: "make test"})
	return k.follow(ctx, req.ConversationID, turn, emit)
}

func (k *turnKeeper) RejoinInvocation(ctx context.Context, rejoin core.InvocationRejoin, sink core.InvocationEventSink) (core.InvocationResult, error) {
	k.mu.Lock()
	turn := k.turns[rejoin.Turn.TurnID]
	k.mu.Unlock()
	if turn == nil || turn.interrupted || turn.session != rejoin.Turn.SessionID {
		return core.InvocationResult{}, core.ErrInvocationTurnGone
	}
	emit := func(event core.InvocationStreamEvent) {
		event.ConversationID, event.SessionID = rejoin.ConversationID, turn.session
		_ = sink(event)
	}
	emit(core.InvocationStreamEvent{Type: "started"})
	return k.follow(ctx, rejoin.ConversationID, turn, emit)
}

// follow waits for the turn's outcome. A caller that goes away because its
// AgentMux is stopping leaves the turn running; one stopped for any other
// reason interrupts it.
func (k *turnKeeper) follow(ctx context.Context, conversationID string, turn *heldTurn, emit func(core.InvocationStreamEvent)) (core.InvocationResult, error) {
	select {
	case <-turn.done:
		result := core.InvocationResult{ConversationID: conversationID, SessionID: turn.session, Answer: turn.answer}
		emit(core.InvocationStreamEvent{Type: "completed", Final: true, Result: &result})
		return result, nil
	case <-ctx.Done():
		if !core.TurnOwnerEnded(ctx) {
			k.mu.Lock()
			turn.interrupted = true
			k.interrupted = append(k.interrupted, turn.id)
			close(turn.done)
			k.mu.Unlock()
		}
		return core.InvocationResult{}, ctx.Err()
	}
}

func (k *turnKeeper) started() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.inputs)
}

// restart stops service as an update would and starts a new process on the
// same store with a new Fleet around the same keeper.
func restartService(t *testing.T, h *harness, first *Service, stopFirst context.CancelFunc, keeper *turnKeeper) *Service {
	t.Helper()
	stopFirst()
	first.Stop()
	second := New(Options{Store: h.store, Invoker: h.router, Workers: &Fleet{Invoker: keeper, Agents: h.store}, Replies: h.replies, StateDir: h.service.state})
	h.service = second
	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	if err := second.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(second.Stop)
	return second
}

func startLocalKeeping(t *testing.T) (*harness, *turnKeeper, *Service, context.CancelFunc) {
	t.Helper()
	h := newHarness(t)
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("local"), nil }
	keeper := newTurnKeeper()
	first := New(Options{Store: h.store, Invoker: h.router, Workers: &Fleet{Invoker: keeper, Agents: h.store}, Replies: h.replies, StateDir: h.service.state})
	h.service = first
	ctx, stop := context.WithCancel(context.Background())
	if err := first.Start(ctx); err != nil {
		t.Fatal(err)
	}
	h.ingest("alice", "t1", "work", true)
	waitFor(t, "the local turn to start and be recorded", func() bool {
		tickets := h.store.allTickets()
		return keeper.started() == 1 && len(tickets) == 1 && tickets[0].Turn != nil
	})
	return h, keeper, first, stop
}

func TestLocalTurnContinuesAcrossARestartMidTurn(t *testing.T) {
	h, keeper, first, stopFirst := startLocalKeeping(t)
	ticket := h.store.allTickets()[0]
	turn := ticket.Turn
	if turn.SessionID != "thr-"+ticket.ConversationID || turn.TurnID != "turn_1" || turn.HostID != "" || turn.InvocationID != "" || turn.Delivered != 1 {
		t.Fatalf("turn = %+v", turn)
	}
	if !keeper.keep[0] {
		t.Fatal("a Super Agent asks its local worker to keep the turn")
	}

	// AgentMux stops mid-turn, as for an update; the turn runs on.
	second := restartService(t, h, first, stopFirst, keeper)
	waitFor(t, "the rejoined turn to be running", func() bool {
		second.mu.Lock()
		defer second.mu.Unlock()
		return len(second.running) == 1
	})
	if status := h.store.allTickets()[0].Status; status != core.SuperAgentTicketRunning {
		t.Fatalf("a rejoined ticket stays running, got %q", status)
	}
	keeper.answers <- "final answer"
	waitFor(t, "the ticket to finish", func() bool {
		return h.store.allTickets()[0].Status == core.SuperAgentTicketReady
	})

	ticket = h.store.allTickets()[0]
	if keeper.started() != 1 || len(keeper.interrupted) != 0 {
		t.Fatalf("the turn must continue, not start over: inputs=%q interrupted=%q", keeper.inputs, keeper.interrupted)
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
	if log := readTicketFile(t, second, ticket, "progress.md"); strings.Count(log, "make test") != 1 {
		t.Fatalf("progress log = %q", log)
	}
}

func TestRestartFallsBackWhenTheLocalTurnIsGone(t *testing.T) {
	h, keeper, first, stopFirst := startLocalKeeping(t)
	// The app-server went away with AgentMux, as after a reboot.
	keeper.mu.Lock()
	for _, turn := range keeper.turns {
		turn.interrupted = true
		close(turn.done)
	}
	keeper.turns = map[string]*heldTurn{}
	keeper.mu.Unlock()

	restartService(t, h, first, stopFirst, keeper)
	waitFor(t, "the turn to start over", func() bool { return keeper.started() == 2 })
	keeper.answers <- "redone"
	waitFor(t, "the ticket to finish", func() bool {
		return h.store.allTickets()[0].Status == core.SuperAgentTicketReady
	})
	keeper.mu.Lock()
	defer keeper.mu.Unlock()
	if !strings.Contains(keeper.inputs[1], "restarted") || !strings.Contains(keeper.inputs[1], "work") {
		t.Fatalf("the second turn gets the recovery note and the work: %q", keeper.inputs[1])
	}
}

func TestStoppingATicketInterruptsItsKeptLocalTurn(t *testing.T) {
	h, keeper, first, stopFirst := startLocalKeeping(t)
	defer first.Stop()
	defer stopFirst()
	if err := first.StopTicket(context.Background(), h.store.allTickets()[0].ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the turn to be interrupted", func() bool {
		keeper.mu.Lock()
		defer keeper.mu.Unlock()
		return len(keeper.interrupted) == 1
	})
	if ticket := h.store.allTickets()[0]; ticket.Status != core.SuperAgentTicketStopped || ticket.Turn != nil {
		t.Fatalf("ticket = %+v", ticket)
	}
	time.Sleep(20 * time.Millisecond)
}
