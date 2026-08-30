package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type invocationTestAgent struct {
	mu       sync.Mutex
	sessions int
}

func (a *invocationTestAgent) Name() string { return "invocation-test" }
func (a *invocationTestAgent) StartSession(context.Context, string) (AgentSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessions++
	return &invocationTestSession{id: fmt.Sprintf("session-%d", a.sessions)}, nil
}
func (a *invocationTestAgent) ListSessions(context.Context) ([]string, error) { return nil, nil }
func (a *invocationTestAgent) Stop(context.Context) error                     { return nil }

type invocationTestSession struct{ id string }

func (s *invocationTestSession) ID() string { return s.id }
func (s *invocationTestSession) Send(_ context.Context, text string) (<-chan *Event, error) {
	events := make(chan *Event, 1)
	events <- &Event{Type: EventFinal, Text: "answer: " + text, Final: true}
	close(events)
	return events, nil
}
func (s *invocationTestSession) RespondPermission(context.Context, bool) error { return nil }
func (s *invocationTestSession) Close(context.Context) error                   { return nil }

type blockingInvocationAgent struct{ started chan struct{} }

func (a *blockingInvocationAgent) Name() string { return "blocking-invocation" }
func (a *blockingInvocationAgent) StartSession(context.Context, string) (AgentSession, error) {
	return &blockingInvocationSession{started: a.started}, nil
}
func (a *blockingInvocationAgent) ListSessions(context.Context) ([]string, error) { return nil, nil }
func (a *blockingInvocationAgent) Stop(context.Context) error                     { return nil }

type blockingInvocationSession struct {
	started chan struct{}
	once    sync.Once
}

func (s *blockingInvocationSession) ID() string { return "blocking-session" }
func (s *blockingInvocationSession) Send(ctx context.Context, _ string) (<-chan *Event, error) {
	events := make(chan *Event)
	s.once.Do(func() { close(s.started) })
	go func() {
		<-ctx.Done()
		close(events)
	}()
	return events, nil
}
func (s *blockingInvocationSession) RespondPermission(context.Context, bool) error { return nil }
func (s *blockingInvocationSession) Close(context.Context) error                   { return nil }

type streamingInvocationAgent struct{ events []*Event }

func (a *streamingInvocationAgent) Name() string { return "streaming-invocation" }
func (a *streamingInvocationAgent) StartSession(context.Context, string) (AgentSession, error) {
	return &streamingInvocationSession{events: a.events}, nil
}
func (a *streamingInvocationAgent) ListSessions(context.Context) ([]string, error) { return nil, nil }
func (a *streamingInvocationAgent) Stop(context.Context) error                     { return nil }

type streamingInvocationSession struct{ events []*Event }

func (s *streamingInvocationSession) ID() string { return "streaming-session" }
func (s *streamingInvocationSession) Send(context.Context, string) (<-chan *Event, error) {
	events := make(chan *Event, len(s.events))
	for _, event := range s.events {
		events <- event
	}
	close(events)
	return events, nil
}
func (s *streamingInvocationSession) RespondPermission(context.Context, bool) error { return nil }
func (s *streamingInvocationSession) Close(context.Context) error                   { return nil }

type richInvocationAgent struct{ session *richInvocationSession }

func (a *richInvocationAgent) Name() string { return "rich-invocation" }
func (a *richInvocationAgent) StartSession(context.Context, string) (AgentSession, error) {
	return a.session, nil
}
func (a *richInvocationAgent) ListSessions(context.Context) ([]string, error) { return nil, nil }
func (a *richInvocationAgent) Stop(context.Context) error                     { return nil }

type richInvocationSession struct {
	input      AgentTurnInput
	pathExists bool
}

func (s *richInvocationSession) ID() string { return "rich-session" }
func (s *richInvocationSession) Send(ctx context.Context, text string) (<-chan *Event, error) {
	return s.SendInput(ctx, AgentTurnInput{Text: text})
}
func (s *richInvocationSession) SendInput(_ context.Context, input AgentTurnInput) (<-chan *Event, error) {
	s.input = input
	if len(input.Attachments) > 0 && input.Attachments[0].Path != "" {
		_, err := os.Stat(input.Attachments[0].Path)
		s.pathExists = err == nil
	}
	events := make(chan *Event, 1)
	events <- &Event{Type: EventFinal, Text: `{"ok":true}`, Final: true}
	close(events)
	return events, nil
}
func (s *richInvocationSession) RespondPermission(context.Context, bool) error { return nil }
func (s *richInvocationSession) Close(context.Context) error                   { return nil }

func TestInvokeRequiresPostgresBackedAgent(t *testing.T) {
	const runtimeID = "invocation-postgres-agent-test"
	engine := NewEngine(nil, NewHookRunner(nil, nil))
	agent := &invocationTestAgent{}
	RegisterAgent(runtimeID, func(map[string]any) (Agent, error) { return agent, nil })
	store := &fakeStore{agents: map[string]AgentInstance{
		"agent-1": {ID: "agent-1", Name: "Managed", RuntimeID: runtimeID, WorkDir: t.TempDir(), Enabled: true},
	}}
	service := NewConnectService(nil, engine, store)

	result, err := service.Invoke(context.Background(), InvocationRequest{
		AgentID: "agent-1", ConversationID: "order-42", Input: "first",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AgentID != "agent-1" || result.ConversationID != "order-42" || result.Answer != "answer: first" {
		t.Fatalf("result = %+v", result)
	}
}

func TestInvokeManagedAgent(t *testing.T) {
	const runtimeID = "invocation-managed-test"
	agent := &invocationTestAgent{}
	RegisterAgent(runtimeID, func(map[string]any) (Agent, error) { return agent, nil })
	store := &fakeStore{agents: map[string]AgentInstance{
		"agent-1": {ID: "agent-1", Name: "Managed", RuntimeID: runtimeID, WorkDir: t.TempDir(), Enabled: true},
	}}
	engine := NewEngine(nil, NewHookRunner(nil, nil))
	service := NewConnectService(nil, engine, store)

	result, err := service.Invoke(context.Background(), InvocationRequest{AgentID: "agent-1", Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if result.AgentID != "agent-1" || !strings.HasPrefix(result.ConversationID, "conv_") || result.Answer != "answer: hello" {
		t.Fatalf("result = %+v", result)
	}
}

func TestInvokeMaterializesRichAttachmentsAndPassesOutputSchema(t *testing.T) {
	const runtimeID = "invocation-rich-test"
	session := &richInvocationSession{}
	RegisterAgent(runtimeID, func(map[string]any) (Agent, error) { return &richInvocationAgent{session: session}, nil })
	engine := NewEngine(nil, NewHookRunner(nil, nil))
	store := &fakeStore{agents: map[string]AgentInstance{
		"agent-rich": {ID: "agent-rich", Name: "Rich", RuntimeID: runtimeID, WorkDir: t.TempDir(), Enabled: true},
	}}
	service := NewConnectService(nil, engine, store)
	result, err := service.Invoke(context.Background(), InvocationRequest{
		AgentID: "agent-rich", Input: "inspect",
		Attachments:  []AgentAttachment{{Kind: "image", Name: "screen.png", MIMEType: "image/png", Data: []byte("png")}},
		OutputSchema: map[string]any{"type": "object"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Answer != `{"ok":true}` || !session.pathExists || len(session.input.Attachments) != 1 {
		t.Fatalf("result=%+v input=%+v pathExists=%t", result, session.input, session.pathExists)
	}
	if session.input.OutputSchema["type"] != "object" || session.input.Attachments[0].Data != nil {
		t.Fatalf("rich input = %+v", session.input)
	}
	if _, err := os.Stat(session.input.Attachments[0].Path); !os.IsNotExist(err) {
		t.Fatalf("temporary attachment still exists: %v", err)
	}
}

func TestInvokeRejectsConcurrentTurnForSameConversation(t *testing.T) {
	const runtimeID = "invocation-blocking-test"
	engine := NewEngine(nil, NewHookRunner(nil, nil))
	started := make(chan struct{})
	RegisterAgent(runtimeID, func(map[string]any) (Agent, error) { return &blockingInvocationAgent{started: started}, nil })
	store := &fakeStore{agents: map[string]AgentInstance{
		"agent-blocking": {ID: "agent-blocking", Name: "Blocking", RuntimeID: runtimeID, WorkDir: t.TempDir(), Enabled: true},
	}}
	service := NewConnectService(nil, engine, store)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := service.Invoke(ctx, InvocationRequest{AgentID: "agent-blocking", ConversationID: "same", Input: "wait"})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first invocation did not start")
	}
	_, err := service.Invoke(context.Background(), InvocationRequest{AgentID: "agent-blocking", ConversationID: "same", Input: "again"})
	if !errors.Is(err, ErrInvocationBusy) {
		t.Fatalf("error = %v, want ErrInvocationBusy", err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("first invocation error = %v, want context canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first invocation did not stop")
	}
}

func TestInvokeValidatesTargetAndInput(t *testing.T) {
	service := NewConnectService(nil, NewEngine(nil, NewHookRunner(nil, nil)), &fakeStore{})
	for _, req := range []InvocationRequest{
		{},
		{AgentID: "a", Input: "   "},
		{AgentID: "a", ConversationID: "bad\nkey", Input: "hello"},
	} {
		if _, err := service.Invoke(context.Background(), req); !errors.Is(err, ErrInvalidInvocation) {
			t.Fatalf("request %+v: error = %v", req, err)
		}
	}
}

func TestInvokeStreamForwardsAgentEventsAndCompletion(t *testing.T) {
	const runtimeID = "invocation-stream-test"
	engine := NewEngine(nil, NewHookRunner(nil, nil))
	agent := &streamingInvocationAgent{events: []*Event{
		{Type: EventThinking, Text: "checking"},
		{Type: EventToolUse, ToolName: "shell", ToolInput: "go test ./..."},
		{Type: EventModelResponse, Usage: &TurnUsage{InputTokens: 5, OutputTokens: 3, TotalTokens: 8}},
		{Type: EventOutput, Text: "almost"},
		{Type: EventFinal, Text: "done", Final: true},
	}}
	RegisterAgent(runtimeID, func(map[string]any) (Agent, error) { return agent, nil })
	store := &fakeStore{agents: map[string]AgentInstance{
		"agent-stream": {ID: "agent-stream", Name: "Stream", RuntimeID: runtimeID, WorkDir: t.TempDir(), Enabled: true},
	}}
	service := NewConnectService(nil, engine, store)
	var events []InvocationStreamEvent
	result, err := service.InvokeStream(context.Background(), InvocationRequest{
		AgentID: "agent-stream", ConversationID: "stream-1", Input: "test",
	}, func(event InvocationStreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Answer != "done" {
		t.Fatalf("result = %+v", result)
	}
	wantTypes := []string{"started", "thinking", "tool_use", "model_response", "output", "final", "completed"}
	if len(events) != len(wantTypes) {
		t.Fatalf("events = %+v", events)
	}
	for index, want := range wantTypes {
		if events[index].Type != want {
			t.Fatalf("event %d type = %q, want %q", index, events[index].Type, want)
		}
		if events[index].InvocationID == "" || events[index].ConversationID != "stream-1" {
			t.Fatalf("event %d missing correlation: %+v", index, events[index])
		}
	}
	if events[2].ToolName != "shell" || events[2].ToolInput != "go test ./..." {
		t.Fatalf("tool event = %+v", events[2])
	}
	if result.Usage == nil || result.Usage.TotalTokens != 8 {
		t.Fatalf("result usage = %+v", result.Usage)
	}
	if events[len(events)-1].Result == nil || events[len(events)-1].Result.Answer != "done" {
		t.Fatalf("completed event = %+v", events[len(events)-1])
	}
}
