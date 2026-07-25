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
