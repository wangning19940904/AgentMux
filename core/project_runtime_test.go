package core

import (
	"context"
	"path/filepath"
	"testing"
)

func TestPrepareConversationUsesConfiguredWorkDir(t *testing.T) {
	workDir := t.TempDir()
	conversations := &senderConversationStore{}
	engine := NewEngine(nil, nil)
	engine.SetConversationStore(conversations)

	conv, got, err := engine.prepareConversation(
		context.Background(),
		"channel:channel-1",
		"chat-1",
		"group",
		"root:message-1",
		WorkspaceInitOptions{AgentID: "agent-1", WorkDir: workDir},
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != workDir {
		t.Fatalf("work dir = %q, want configured path %q", got, workDir)
	}
	if conv.WorkDir != workDir {
		t.Fatalf("conversation work dir = %q, want configured path %q", conv.WorkDir, workDir)
	}
}

func TestPrepareConversationMigratesLegacyIsolatedWorkDir(t *testing.T) {
	workDir := t.TempDir()
	legacyWorkDir := filepath.Join(
		workDir,
		".agentmux",
		"conversations",
		"agent-1",
		"channel_channel-1",
		"root_message-1",
		"cwd",
	)
	conversations := &senderConversationStore{item: Conversation{
		ID:              "conversation-1",
		Scope:           "channel:channel-1",
		ConversationKey: "root:message-1",
		ChatID:          "chat-1",
		ChatType:        "group",
		AgentID:         "agent-1",
		WorkDir:         legacyWorkDir,
		NativeSessionID: "native-thread-with-legacy-cwd",
	}}
	engine := NewEngine(nil, nil)
	engine.SetConversationStore(conversations)

	conv, got, err := engine.prepareConversation(
		context.Background(),
		"channel:channel-1",
		"chat-1",
		"group",
		"root:message-1",
		WorkspaceInitOptions{AgentID: "agent-1", WorkDir: workDir},
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != workDir || conv.WorkDir != workDir {
		t.Fatalf("migrated work dirs = returned %q, conversation %q; want %q", got, conv.WorkDir, workDir)
	}
	if conv.NativeSessionID != "" {
		t.Fatalf("native session id = %q, want cleared after cwd migration", conv.NativeSessionID)
	}
	conversations.mu.Lock()
	defer conversations.mu.Unlock()
	if conversations.item.WorkDir != workDir {
		t.Fatalf("persisted work dir = %q, want %q", conversations.item.WorkDir, workDir)
	}
	if conversations.item.NativeSessionID != "" {
		t.Fatalf("persisted native session id = %q, want cleared", conversations.item.NativeSessionID)
	}
}
