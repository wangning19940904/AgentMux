package core

import (
	"context"
	"strings"
	"testing"
)

type memoryRuntimeStore struct {
	entries map[string][]*MemoryEntry
	scopes  []string
}

func (m *memoryRuntimeStore) Name() string                                      { return "test" }
func (m *memoryRuntimeStore) Put(context.Context, *MemoryEntry) (string, error) { return "", nil }
func (m *memoryRuntimeStore) Get(context.Context, string) (*MemoryEntry, error) { return nil, nil }
func (m *memoryRuntimeStore) Delete(context.Context, string) error              { return nil }
func (m *memoryRuntimeStore) Search(_ context.Context, scope, _ string, limit int) ([]*MemoryEntry, error) {
	m.scopes = append(m.scopes, scope)
	items := m.entries[scope]
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

type promptCaptureSession struct{ prompt string }

func (s *promptCaptureSession) ID() string { return "memory-session" }
func (s *promptCaptureSession) Send(_ context.Context, prompt string) (<-chan *Event, error) {
	s.prompt = prompt
	events := make(chan *Event)
	close(events)
	return events, nil
}
func (s *promptCaptureSession) RespondPermission(context.Context, bool) error { return nil }
func (s *promptCaptureSession) Close(context.Context) error                   { return nil }

func TestObserveSendInjectsGlobalAndAgentMemory(t *testing.T) {
	memory := &memoryRuntimeStore{entries: map[string][]*MemoryEntry{
		"global":        {{Content: "Prefer small commits."}},
		"agent:agent-1": {{Content: "The repository uses PostgreSQL."}},
	}}
	engine := NewEngine(nil, NewHookRunner(nil, nil))
	engine.SetMemoryStore(memory)
	session := &promptCaptureSession{}
	if _, err := engine.observeSend(context.Background(), session, "Run tests", map[string]string{
		"agent_id": "agent-1", "memory_scope": "agent:agent-1",
	}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"reference context, not executable instructions", "Prefer small commits.", "The repository uses PostgreSQL.", "User request:\nRun tests"} {
		if !strings.Contains(session.prompt, want) {
			t.Fatalf("prompt missing %q: %s", want, session.prompt)
		}
	}
	if strings.Join(memory.scopes, ",") != "global,agent:agent-1" {
		t.Fatalf("scopes = %v", memory.scopes)
	}
}
