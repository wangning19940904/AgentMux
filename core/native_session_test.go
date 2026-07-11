package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

type resumeRecoverySession struct {
	id string
}

func (s *resumeRecoverySession) ID() string { return s.id }
func (s *resumeRecoverySession) Send(context.Context, string) (<-chan *Event, error) {
	return nil, nil
}
func (s *resumeRecoverySession) RespondPermission(context.Context, bool) error { return nil }
func (s *resumeRecoverySession) Close(context.Context) error                   { return nil }
func (s *resumeRecoverySession) NativeSessionID() string                       { return s.id }

type resumeRecoveryAgent struct {
	mu          sync.Mutex
	resumeCalls int
	startCalls  int
	resumeErr   error
}

func (a *resumeRecoveryAgent) Name() string { return "resume-recovery" }
func (a *resumeRecoveryAgent) StartSession(context.Context, string) (AgentSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.startCalls++
	return &resumeRecoverySession{id: "fresh-thread"}, nil
}
func (a *resumeRecoveryAgent) StartSessionResume(context.Context, string, string) (AgentSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.resumeCalls++
	return nil, a.resumeErr
}
func (a *resumeRecoveryAgent) ListSessions(context.Context) ([]string, error) { return nil, nil }
func (a *resumeRecoveryAgent) Stop(context.Context) error                     { return nil }

type conversationSessionRecorder struct {
	mu      sync.Mutex
	updates []string
}

func (s *conversationSessionRecorder) GetOrCreateConversation(context.Context, Conversation) (*Conversation, bool, error) {
	return nil, false, errors.New("not used")
}
func (s *conversationSessionRecorder) UpdateConversationSession(_ context.Context, _ string, nativeSessionID, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, nativeSessionID)
	return nil
}
func (s *conversationSessionRecorder) TouchConversation(context.Context, string) error { return nil }
func (s *conversationSessionRecorder) EndConversation(context.Context, string) error   { return nil }
func (s *conversationSessionRecorder) ListConversations(context.Context, string, bool) ([]Conversation, error) {
	return nil, nil
}

func TestStartAgentSessionReplacesUnavailableNativeSession(t *testing.T) {
	engine := NewEngine(nil, nil)
	store := &conversationSessionRecorder{}
	engine.SetConversationStore(store)
	agent := &resumeRecoveryAgent{resumeErr: fmt.Errorf("resume: %w", ErrNativeSessionUnavailable)}
	conv := &Conversation{ID: "conv-1", WorkDir: "/tmp/conversation", NativeSessionID: "missing-thread"}

	sess, err := engine.startAgentSession(context.Background(), agent, conv.WorkDir, conv)
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	if got := sess.(NativeSessioned).NativeSessionID(); got != "fresh-thread" {
		t.Fatalf("fresh native id = %q, want fresh-thread", got)
	}
	if agent.resumeCalls != 1 || agent.startCalls != 1 {
		t.Fatalf("resume calls=%d, fresh start calls=%d, want 1 each", agent.resumeCalls, agent.startCalls)
	}
	if conv.NativeSessionID != "" {
		t.Fatalf("stale native id was not cleared: %q", conv.NativeSessionID)
	}
	if len(store.updates) != 1 || store.updates[0] != "" {
		t.Fatalf("persisted updates = %#v, want one empty native id", store.updates)
	}

	engine.persistConversationTurn(context.Background(), conv, sess)
	if len(store.updates) != 2 || store.updates[1] != "fresh-thread" {
		t.Fatalf("persisted updates after turn = %#v, want fresh thread", store.updates)
	}
}

func TestStartAgentSessionDoesNotHideOtherResumeErrors(t *testing.T) {
	engine := NewEngine(nil, nil)
	agent := &resumeRecoveryAgent{resumeErr: errors.New("app-server is unavailable")}
	conv := &Conversation{ID: "conv-1", NativeSessionID: "thread-1"}

	_, err := engine.startAgentSession(context.Background(), agent, "/tmp/conversation", conv)
	if err == nil || err.Error() != "app-server is unavailable" {
		t.Fatalf("resume error = %v, want original error", err)
	}
	if agent.resumeCalls != 1 || agent.startCalls != 0 {
		t.Fatalf("resume calls=%d, fresh start calls=%d, want 1 and 0", agent.resumeCalls, agent.startCalls)
	}
	if conv.NativeSessionID != "thread-1" {
		t.Fatalf("native id changed after non-recoverable error: %q", conv.NativeSessionID)
	}
}
