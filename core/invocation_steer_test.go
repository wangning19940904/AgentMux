package core

import (
	"context"
	"errors"
	"testing"
)

type steerSession struct {
	AgentSession
	turn    string
	steered []string
}

func (s *steerSession) Steer(_ context.Context, text string) error {
	s.steered = append(s.steered, text)
	return nil
}
func (s *steerSession) Interrupt(context.Context) error { return nil }
func (s *steerSession) ResolveInteraction(context.Context, string, AgentInteractionResponse) error {
	return nil
}
func (s *steerSession) ActiveTurnID() string { return s.turn }

func TestSteerInvocationReachesOnlyARunningTurn(t *testing.T) {
	ctx := context.Background()
	engine := &Engine{}
	connect := &ConnectService{eng: engine}
	if err := connect.SteerInvocation(ctx, "agent-1", "conv-1", "change course"); !errors.Is(err, ErrInvocationIdle) {
		t.Fatalf("no invocation: %v", err)
	}
	session := &steerSession{}
	engine.trackInvocationSession("agent:agent-1", "api:conv-1", session)
	if err := connect.SteerInvocation(ctx, "agent-1", "conv-1", "change course"); !errors.Is(err, ErrInvocationIdle) {
		t.Fatalf("between turns: %v", err)
	}
	session.turn = "turn-1"
	if err := connect.SteerInvocation(ctx, "agent-1", "conv-1", "change course"); err != nil || len(session.steered) != 1 {
		t.Fatalf("running turn: err=%v steered=%v", err, session.steered)
	}
	if err := connect.SteerInvocation(ctx, "agent-1", "conv-2", "change course"); !errors.Is(err, ErrInvocationIdle) {
		t.Fatalf("another conversation: %v", err)
	}
	engine.trackInvocationSession("agent:agent-1", "api:conv-1", nil)
	if err := connect.SteerInvocation(ctx, "agent-1", "conv-1", "change course"); !errors.Is(err, ErrInvocationIdle) {
		t.Fatalf("after the invocation: %v", err)
	}
}
