package core

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordingBrainHook struct {
	mu       sync.Mutex
	brief    string
	briefed  []string
	finished []string
}

func (h *recordingBrainHook) BrainBrief(_ context.Context, agentID string, msg *Message) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.briefed = append(h.briefed, agentID+"|"+msg.Text)
	return h.brief
}

func (h *recordingBrainHook) BrainFinished(_ context.Context, agentID string, _ *Message, answer string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.finished = append(h.finished, agentID+"|"+answer)
}

// scriptedAgent answers every turn with the same events and records what it
// was sent.
type scriptedAgent struct {
	mu     sync.Mutex
	events []*Event
	turns  []string
}

func (a *scriptedAgent) Name() string { return "scripted" }
func (a *scriptedAgent) StartSession(context.Context, string) (AgentSession, error) {
	return &recordingScriptedSession{agent: a}, nil
}
func (a *scriptedAgent) ListSessions(context.Context) ([]string, error) { return nil, nil }
func (a *scriptedAgent) Stop(context.Context) error                     { return nil }

type recordingScriptedSession struct{ agent *scriptedAgent }

func (s *recordingScriptedSession) ID() string { return "scripted" }
func (s *recordingScriptedSession) Send(_ context.Context, text string) (<-chan *Event, error) {
	s.agent.mu.Lock()
	s.agent.turns = append(s.agent.turns, text)
	events := s.agent.events
	s.agent.mu.Unlock()
	out := make(chan *Event, len(events))
	for _, ev := range events {
		out <- ev
	}
	close(out)
	return out, nil
}
func (s *recordingScriptedSession) RespondPermission(context.Context, bool) error { return nil }
func (s *recordingScriptedSession) Close(context.Context) error                   { return nil }

const brainAnswer = "I started a worker for the login bug.\n\n<agentmux-action>\n{\"action\":\"accept\",\"worker_id\":\"auto:codex\"}\n</agentmux-action>"

func runBrainTurn(t *testing.T, hook *recordingBrainHook) (*streamingPlatform, *scriptedAgent) {
	t.Helper()
	eng := NewEngine(nil, NewHookRunner(nil, nil))
	eng.SetBrainHook(hook)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = eng.Start(ctx) }()
	plat := newStreamingPlatform("fake")
	restore := stubPlatformFactory(t, "fake-brain", plat)
	t.Cleanup(restore)
	agent := &scriptedAgent{events: []*Event{
		{Type: EventOutput, Text: "I started a worker for the login bug.\n\n<agentmux-ac"},
		{Type: EventFinal, Text: brainAnswer, Final: true},
	}}
	ch := Channel{ID: "c1", Name: "ops", Type: "fake-brain", AgentID: "agent-1", Enabled: true, UpdatedAt: time.Now()}
	if err := eng.AttachChannel(ctx, ch, agent, ""); err != nil {
		t.Fatal(err)
	}
	plat.push(&Message{ID: "m1", ChatID: "chat-1", UserID: "u1", Text: "fix the login bug", Platform: "fake"})
	waitFor(t, "final reply", func() bool {
		plat.mu.Lock()
		defer plat.mu.Unlock()
		return plat.messageDoneCalls == 1
	})
	return plat, agent
}

func TestBriefedTurnHidesActionsAndHandsBackTheAnswer(t *testing.T) {
	hook := &recordingBrainHook{brief: "AGENTMUX CONTEXT: you can start workers"}
	plat, agent := runBrainTurn(t, hook)
	agent.mu.Lock()
	sent := agent.turns[0]
	agent.mu.Unlock()
	if !strings.HasPrefix(sent, "AGENTMUX CONTEXT: you can start workers\n\n") || !strings.HasSuffix(sent, "fix the login bug") {
		t.Fatalf("the brain was sent %q", sent)
	}
	plat.mu.Lock()
	updates, done := append([]string(nil), plat.messageUpdates...), plat.messageDoneText
	plat.mu.Unlock()
	if done != "I started a worker for the login bug." {
		t.Fatalf("the chat's final message = %q", done)
	}
	for _, update := range updates {
		if strings.Contains(update, "<agent") {
			t.Fatalf("an update showed the action block: %q", update)
		}
	}
	waitFor(t, "finished answer", func() bool {
		hook.mu.Lock()
		defer hook.mu.Unlock()
		return len(hook.finished) == 1
	})
	if hook.finished[0] != "agent-1|"+brainAnswer {
		t.Fatalf("hook got %q", hook.finished[0])
	}
}

func TestUnbriefedTurnIsUnchanged(t *testing.T) {
	hook := &recordingBrainHook{}
	plat, agent := runBrainTurn(t, hook)
	agent.mu.Lock()
	sent := agent.turns[0]
	agent.mu.Unlock()
	if sent != "fix the login bug" {
		t.Fatalf("the agent was sent %q", sent)
	}
	plat.mu.Lock()
	done := plat.messageDoneText
	plat.mu.Unlock()
	if done != brainAnswer {
		t.Fatalf("an agent without a brief had its answer changed: %q", done)
	}
	hook.mu.Lock()
	defer hook.mu.Unlock()
	if len(hook.briefed) != 1 || hook.briefed[0] != "agent-1|fix the login bug" || len(hook.finished) != 0 {
		t.Fatalf("hook calls: briefed %q finished %q", hook.briefed, hook.finished)
	}
}

func TestBrainVisibleText(t *testing.T) {
	cases := []struct {
		text      string
		streaming bool
		want      string
	}{
		{"plain answer", false, "plain answer"},
		{"done.\n\n<agentmux-action>{}</agentmux-action>", false, "done."},
		{"done.\n<agentmux-action", false, "done."},
		{"done. <agentmux-ac", true, "done."},
		{"a < b", true, "a < b"},
		{"ends with <a", true, "ends with"},
		{"ends with <a", false, "ends with <a"},
	}
	for _, c := range cases {
		if got := brainVisibleText(c.text, c.streaming); got != c.want {
			t.Errorf("brainVisibleText(%q, %v) = %q, want %q", c.text, c.streaming, got, c.want)
		}
	}
}
