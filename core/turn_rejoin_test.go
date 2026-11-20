package core

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// keptTurnAgent keeps one turn that a later process can rejoin.
type keptTurnAgent struct {
	mu      sync.Mutex
	configs []map[string]any
	rejoins []AgentTurnRef
}

func (a *keptTurnAgent) Name() string { return "kept-turn" }
func (a *keptTurnAgent) StartSession(context.Context, string) (AgentSession, error) {
	return &invocationTestSession{id: "thr_1"}, nil
}
func (a *keptTurnAgent) ListSessions(context.Context) ([]string, error) { return nil, nil }
func (a *keptTurnAgent) Stop(context.Context) error                     { return nil }
func (a *keptTurnAgent) RejoinTurn(_ context.Context, _ string, turn AgentTurnRef) (AgentSession, <-chan *Event, error) {
	a.mu.Lock()
	a.rejoins = append(a.rejoins, turn)
	a.mu.Unlock()
	if turn.TurnID != "turn_1" {
		return nil, nil, ErrInvocationTurnGone
	}
	events := make(chan *Event, 2)
	events <- &Event{Type: EventToolUse, TurnID: "turn_1", ToolName: "Bash", ToolInput: "make test"}
	events <- &Event{Type: EventFinal, TurnID: "turn_1", Text: "finished", Final: true}
	close(events)
	return &invocationTestSession{id: turn.SessionID}, events, nil
}

func TestRejoinInvocationFollowsAKeptTurn(t *testing.T) {
	const runtimeID = "invocation-kept-turn-test"
	agent := &keptTurnAgent{}
	RegisterAgent(runtimeID, func(cfg map[string]any) (Agent, error) {
		agent.mu.Lock()
		agent.configs = append(agent.configs, cfg)
		agent.mu.Unlock()
		return agent, nil
	})
	store := &fakeStore{agents: map[string]AgentInstance{
		"agent-1": {ID: "agent-1", Name: "Worker", RuntimeID: runtimeID, WorkDir: t.TempDir(), Enabled: true},
	}}
	service := NewConnectService(nil, NewEngine(nil, NewHookRunner(nil, nil)), store)

	// A kept turn is asked for with KeepTurn.
	if _, err := service.Invoke(context.Background(), InvocationRequest{AgentID: "agent-1", Input: "work", KeepTurn: true}); err != nil {
		t.Fatal(err)
	}
	if keep, _ := agent.configs[0][AgentConfigKeepTurns].(bool); !keep {
		t.Fatalf("config = %v", agent.configs[0])
	}

	var events []InvocationStreamEvent
	result, err := service.RejoinInvocation(context.Background(), InvocationRejoin{
		AgentID: "agent-1", ConversationID: "conv-1", Turn: AgentTurnRef{SessionID: "thr_1", TurnID: "turn_1"},
	}, func(event InvocationStreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil || result.Answer != "finished" || result.ConversationID != "conv-1" || result.SessionID != "thr_1" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var types []string
	for _, event := range events {
		types = append(types, event.Type)
		if event.ConversationID != "conv-1" || event.InvocationID != result.ID {
			t.Fatalf("event = %+v", event)
		}
	}
	if got := len(types); got != 4 || types[0] != "started" || types[1] != string(EventToolUse) || types[3] != "completed" {
		t.Fatalf("events = %q", types)
	}
	if keep, _ := agent.configs[1][AgentConfigKeepTurns].(bool); !keep {
		t.Fatalf("a rejoin creates the agent to keep turns: %v", agent.configs[1])
	}

	// A turn that is not running is reported before any event.
	events = nil
	_, err = service.RejoinInvocation(context.Background(), InvocationRejoin{
		AgentID: "agent-1", ConversationID: "conv-1", Turn: AgentTurnRef{SessionID: "thr_1", TurnID: "turn_2"},
	}, func(event InvocationStreamEvent) error {
		events = append(events, event)
		return nil
	})
	if !errors.Is(err, ErrInvocationTurnGone) || len(events) != 0 {
		t.Fatalf("err=%v events=%+v", err, events)
	}
}

func TestRejoinInvocationOfAnAgentThatKeepsNoTurns(t *testing.T) {
	const runtimeID = "invocation-no-kept-turns-test"
	RegisterAgent(runtimeID, func(map[string]any) (Agent, error) { return &invocationTestAgent{}, nil })
	store := &fakeStore{agents: map[string]AgentInstance{
		"agent-1": {ID: "agent-1", Name: "Worker", RuntimeID: runtimeID, WorkDir: t.TempDir(), Enabled: true},
	}}
	service := NewConnectService(nil, NewEngine(nil, NewHookRunner(nil, nil)), store)
	_, err := service.RejoinInvocation(context.Background(), InvocationRejoin{
		AgentID: "agent-1", ConversationID: "conv-1", Turn: AgentTurnRef{SessionID: "s", TurnID: "t"},
	}, nil)
	if !errors.Is(err, ErrInvocationTurnGone) {
		t.Fatalf("err = %v", err)
	}
}

func TestTurnOwnerEnded(t *testing.T) {
	owner, stop := context.WithCancel(context.Background())
	turn, stopTurn := context.WithCancel(owner)
	defer stopTurn()
	ctx := WithTurnDetach(turn, owner)
	if TurnOwnerEnded(ctx) || TurnOwnerEnded(context.Background()) {
		t.Fatal("the owner runs")
	}
	stop()
	if !TurnOwnerEnded(ctx) {
		t.Fatal("the owner ended")
	}
}
