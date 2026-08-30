package core

import (
	"context"
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
