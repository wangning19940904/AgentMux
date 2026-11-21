package core

import (
	"context"
	"strings"
	"testing"
)

type decisionGuard struct {
	decision GuardDecision
	request  *GuardRequest
}

func (g *decisionGuard) Name() string { return "test" }
func (g *decisionGuard) Evaluate(_ context.Context, request *GuardRequest) (GuardDecision, error) {
	g.request = request
	return g.decision, nil
}

type guardInteractiveSession struct{ response AgentInteractionResponse }

func (s *guardInteractiveSession) ID() string { return "guard-session" }
func (s *guardInteractiveSession) Send(context.Context, string) (<-chan *Event, error) {
	return nil, nil
}
func (s *guardInteractiveSession) RespondPermission(context.Context, bool) error { return nil }
func (s *guardInteractiveSession) Close(context.Context) error                   { return nil }
func (s *guardInteractiveSession) Steer(context.Context, string) error           { return nil }
func (s *guardInteractiveSession) Interrupt(context.Context) error               { return nil }
func (s *guardInteractiveSession) ActiveTurnID() string                          { return "turn" }
func (s *guardInteractiveSession) ResolveInteraction(_ context.Context, _ string, response AgentInteractionResponse) error {
	s.response = response
	return nil
}

func TestGuardResolvesNativePermission(t *testing.T) {
	for _, test := range []struct {
		decision GuardDecision
		handled  bool
		response string
	}{
		{decision: GuardAllow, handled: true, response: "accept"},
		{decision: GuardDeny, handled: true, response: "decline"},
		{decision: GuardAsk, handled: false},
	} {
		guard := &decisionGuard{decision: test.decision}
		engine := NewEngine(nil, NewHookRunner(nil, nil))
		engine.SetGuard(guard)
		session := &guardInteractiveSession{}
		event := &Event{Type: EventPermission, Interaction: &AgentInteraction{
			ID: "interaction-1", Kind: AgentInteractionCommandApproval, Command: "go test ./...", Cwd: "/repo",
		}}
		handled := engine.resolveGuardInteraction(context.Background(), session, event, map[string]string{
			"agent_id": "agent-1", "runtime_id": "codex",
		})
		if handled != test.handled || session.response.Decision != test.response {
			t.Fatalf("decision %q: handled=%v response=%+v", test.decision, handled, session.response)
		}
		if guard.request.Tool != "shell" || guard.request.Action != "execute" || guard.request.AgentID != "agent-1" {
			t.Fatalf("guard request = %+v", guard.request)
		}
	}
}

type guardStreamRecorder struct{ updates []string }

func (s *guardStreamRecorder) Update(_ context.Context, text string, _, _ bool) error {
	s.updates = append(s.updates, text)
	return nil
}

func (s *guardStreamRecorder) Close(context.Context) error { return nil }

// Streaming channel turns (Feishu/Lark stream_message and stream_card) must
// apply Guard rules before a permission request becomes an approval card.
func TestStreamingReplyAppliesGuard(t *testing.T) {
	for _, test := range []struct {
		decision GuardDecision
		response string
	}{
		{decision: GuardAllow, response: "accept"},
		{decision: GuardDeny, response: "decline"},
	} {
		guard := &decisionGuard{decision: test.decision}
		engine := NewEngine(nil, NewHookRunner(nil, nil))
		engine.SetGuard(guard)
		session := &guardInteractiveSession{}
		event := &Event{Type: EventPermission, Interaction: &AgentInteraction{
			ID: "interaction-1", Kind: AgentInteractionCommandApproval, Command: "git push", Cwd: "/repo",
		}}
		events := make(chan *Event, 1)
		events <- event
		close(events)
		stream := &guardStreamRecorder{}
		engine.driveReplyStream(context.Background(), session, stream, nil, events, map[string]string{
			"agent_id": "agent-1", "runtime_id": "codex", "channel_id": "channel-1", "conversation_key": "chat-1",
		})
		if guard.request == nil || guard.request.Tool != "shell" || guard.request.Args["command"] != "git push" {
			t.Fatalf("decision %q: guard request = %+v", test.decision, guard.request)
		}
		if session.response.Decision != test.response || event.Metadata["guard_decision"] != string(test.decision) {
			t.Fatalf("decision %q: response=%+v metadata=%v", test.decision, session.response, event.Metadata)
		}
		for _, update := range stream.updates {
			if strings.Contains(update, "等待审批") {
				t.Fatalf("decision %q: stream showed a pending approval: %q", test.decision, update)
			}
		}
	}
}
