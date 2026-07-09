package core

import (
	"context"
	"time"
)

// Conversation is a first-class, persisted chat thread with an agent. It is
// the durable counterpart of the in-memory AgentSession: a single agent talks
// to many chats (different DMs, different groups), and each such chat is one
// Conversation. Conversations are located by (Scope, ChatID) and survive
// process restarts so context can be resumed.
//
// Scope namespaces a chat to the runtime that owns it:
//   - "channel:<channelID>" for console-managed channels
//   - "project:<name>"      for config.toml projects
//
// Group chats are shared per chat (all members share one Conversation, keyed
// by the platform chat id); direct messages are naturally per-user because
// each DM has its own chat id.
type Conversation struct {
	ID string `json:"id"`
	// Scope + ChatID form the natural key used to find or create a
	// conversation for an inbound message.
	Scope    string `json:"scope"`
	ChatID   string `json:"chat_id"`
	ChatType string `json:"chat_type,omitempty"`
	AgentID  string `json:"agent_id,omitempty"`
	// WorkDir is this conversation's isolated working directory (sandbox).
	WorkDir string `json:"work_dir,omitempty"`
	// NativeSessionID is the agent-native resume handle (e.g. Claude Code's
	// session id) used to restore context across turns and restarts.
	NativeSessionID string    `json:"native_session_id,omitempty"`
	Title           string    `json:"title,omitempty"`
	MessageCount    int       `json:"message_count"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	LastActiveAt    time.Time `json:"last_active_at,omitempty"`
	// EndedAt marks a soft-deleted (ended) conversation. Ended conversations
	// are skipped when finding the active conversation for a chat, so the next
	// message opens a fresh one (implements /new and /clear).
	EndedAt time.Time `json:"ended_at,omitempty"`
}

// ConversationStore is the persistence surface for conversations. Implemented
// by store.Store; declared here because core never imports store.
type ConversationStore interface {
	// GetOrCreateConversation returns the active (not ended) conversation for
	// (scope, chatID), creating one with the given seed fields when none
	// exists. created reports whether a new record was inserted.
	GetOrCreateConversation(ctx context.Context, seed Conversation) (conv *Conversation, created bool, err error)
	// UpdateConversationSession persists the agent-native session id and work
	// directory for a conversation.
	UpdateConversationSession(ctx context.Context, id, nativeSessionID, workDir string) error
	// TouchConversation bumps the message count and last-active timestamp.
	TouchConversation(ctx context.Context, id string) error
	// EndConversation marks a conversation ended (soft delete) so the next
	// message for its chat starts fresh.
	EndConversation(ctx context.Context, id string) error
	// ListConversations returns conversations, most-recently-active first.
	// When scope is non-empty results are limited to that scope; when
	// includeEnded is false ended conversations are omitted.
	ListConversations(ctx context.Context, scope string, includeEnded bool) ([]Conversation, error)
}
