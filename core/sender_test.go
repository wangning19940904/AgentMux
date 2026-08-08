package core

import (
	"context"
	"sync"
	"testing"
	"time"
)

type senderConversationStore struct {
	mu      sync.Mutex
	item    Conversation
	touches int
}

type blockingConversationAgent struct {
	started  chan struct{}
	canceled chan struct{}
	once     sync.Once
}

func (a *blockingConversationAgent) Name() string { return "blocking" }
func (a *blockingConversationAgent) StartSession(context.Context, string) (AgentSession, error) {
	return &blockingConversationSession{agent: a}, nil
}
func (a *blockingConversationAgent) ListSessions(context.Context) ([]string, error) { return nil, nil }
func (a *blockingConversationAgent) Stop(context.Context) error                     { return nil }

type blockingConversationSession struct {
	agent *blockingConversationAgent
}

func (s *blockingConversationSession) ID() string { return "blocking-session" }
func (s *blockingConversationSession) Send(ctx context.Context, _ string) (<-chan *Event, error) {
	out := make(chan *Event)
	s.agent.once.Do(func() { close(s.agent.started) })
	go func() {
		<-ctx.Done()
		close(s.agent.canceled)
		close(out)
	}()
	return out, nil
}
func (s *blockingConversationSession) RespondPermission(context.Context, bool) error { return nil }
func (s *blockingConversationSession) Close(context.Context) error                   { return nil }

func (s *senderConversationStore) GetOrCreateConversation(_ context.Context, seed Conversation) (*Conversation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.item.ID == "" {
		s.item = seed
		s.item.ID = "conversation-console"
		return &s.item, true, nil
	}
	return &s.item, false, nil
}

func (s *senderConversationStore) UpdateConversationSession(_ context.Context, _ string, nativeSessionID, workDir string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.item.NativeSessionID = nativeSessionID
	s.item.WorkDir = workDir
	return nil
}

func (s *senderConversationStore) TouchConversation(context.Context, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.touches++
	return nil
}

func (s *senderConversationStore) EndConversation(context.Context, string) error { return nil }

func (s *senderConversationStore) ListConversations(_ context.Context, scope string, _ bool) ([]Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.item.ID == "" || s.item.Scope != scope {
		return nil, nil
	}
	return []Conversation{s.item}, nil
}

func TestSendToConversationContinuesAgentWithoutPublishingToChannel(t *testing.T) {
	engine := NewEngine(nil, nil)
	conversations := &senderConversationStore{item: Conversation{
		ID: "conversation-console", Scope: "channel:channel-console",
		ConversationKey: "chat:room", ChatID: "room", ChatType: "group",
		AgentID: "agent-console", WorkDir: t.TempDir(),
	}}
	engine.SetConversationStore(conversations)
	platform := newFakePlatform("console-test")
	restore := stubPlatformFactory(t, "console-test", platform)
	defer restore()
	agent := &fakeAgent{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.AttachChannel(ctx, Channel{
		ID: "channel-console", Name: "Console", Type: "console-test",
		AgentID: "agent-console", Enabled: true, UpdatedAt: time.Now(),
	}, agent, conversations.item.WorkDir, WorkspaceInitOptions{
		AgentID: "agent-console", WorkDir: conversations.item.WorkDir,
	}); err != nil {
		t.Fatal(err)
	}
	defer engine.DetachChannel("channel-console")

	answer, err := engine.SendToConversation(ctx, "channel-console", "conversation-console", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if answer != "echo: hello" {
		t.Fatalf("answer = %q", answer)
	}
	platform.mu.Lock()
	defer platform.mu.Unlock()
	if len(platform.replies) != 0 || len(platform.sends) != 0 {
		t.Fatalf("console message leaked to channel: replies=%v sends=%v", platform.replies, platform.sends)
	}
	conversations.mu.Lock()
	defer conversations.mu.Unlock()
	if conversations.touches != 1 {
		t.Fatalf("touches = %d, want 1", conversations.touches)
	}
}

func TestConversationRuntimeControllerStopsConsoleTurn(t *testing.T) {
	engine := NewEngine(nil, nil)
	conversations := &senderConversationStore{item: Conversation{
		ID: "conversation-stop", Scope: "channel:channel-stop",
		ConversationKey: "chat:stop", ChatID: "stop", ChatType: "group",
		AgentID: "agent-stop", WorkDir: t.TempDir(),
	}}
	engine.SetConversationStore(conversations)
	platform := newFakePlatform("console-stop-test")
	restore := stubPlatformFactory(t, "console-stop-test", platform)
	defer restore()
	agent := &blockingConversationAgent{started: make(chan struct{}), canceled: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.AttachChannel(ctx, Channel{
		ID: "channel-stop", Name: "Console stop", Type: "console-stop-test",
		AgentID: "agent-stop", Enabled: true, UpdatedAt: time.Now(),
	}, agent, conversations.item.WorkDir, WorkspaceInitOptions{
		AgentID: "agent-stop", WorkDir: conversations.item.WorkDir,
	}); err != nil {
		t.Fatal(err)
	}
	defer engine.DetachChannel("channel-stop")

	done := make(chan error, 1)
	go func() {
		_, err := engine.SendToConversation(context.Background(), "channel-stop", "conversation-stop", "hang")
		done <- err
	}()
	select {
	case <-agent.started:
	case <-time.After(2 * time.Second):
		t.Fatal("console turn did not start")
	}

	state, err := engine.ConversationRuntimeState(context.Background(), "channel-stop", "chat:stop")
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != string(ChannelTaskRunning) || !state.CanStop {
		t.Fatalf("running state = %+v", state)
	}
	stopping, err := engine.StopConversation(context.Background(), "channel-stop", "chat:stop", "")
	if err != nil {
		t.Fatal(err)
	}
	if stopping.Status != ConversationStatusStopping || stopping.CanStop {
		t.Fatalf("stopping state = %+v", stopping)
	}
	select {
	case <-agent.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("console turn context was not cancelled")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stopped console turn returned no error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stopped console turn did not return")
	}
	state, err = engine.ConversationRuntimeState(context.Background(), "channel-stop", "chat:stop")
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != ConversationStatusIdle || state.CanStop {
		t.Fatalf("final state = %+v", state)
	}
}
