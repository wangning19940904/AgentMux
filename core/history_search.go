package core

import (
	"strings"
	"time"
)

// History search kinds. A document of a memory kind lands in the memory list
// of a search result, an original kind in the originals list
// (docs/architecture/history-search.md §5).
const (
	// HistoryKindMemory is a memory_entries row.
	HistoryKindMemory = "memory"
	// HistoryKindSummary is reserved for summaries a memory loop writes.
	HistoryKindSummary = "summary"
	// HistoryKindTurnInput is what a person sent an agent in one turn.
	HistoryKindTurnInput = "turn_input"
	// HistoryKindTurnAnswer is the agent's final answer in one turn.
	HistoryKindTurnAnswer = "turn_answer"
)

// HistoryDoc is one searchable text: a memory entry, or one side of a turn.
// Content is already redacted when it reaches the index.
type HistoryDoc struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// AgentID owns a turn; it is empty for a memory entry.
	AgentID string `json:"agent_id,omitempty"`
	// MemoryScope is a memory entry's scope ("global", "agent:<id>", …).
	MemoryScope string `json:"memory_scope,omitempty"`
	// PrivateTo names the person of a one-to-one chat. Only that person's
	// turns, and the Console, see the document. Empty means shared.
	PrivateTo       string    `json:"-"`
	ChannelID       string    `json:"channel_id,omitempty"`
	ChatID          string    `json:"chat_id,omitempty"`
	ConversationKey string    `json:"conversation_key,omitempty"`
	UserID          string    `json:"user_id,omitempty"`
	UserName        string    `json:"user_name,omitempty"`
	TraceID         string    `json:"trace_id,omitempty"`
	Content         string    `json:"-"`
	Terms           []string  `json:"-"`
	OccurredAt      time.Time `json:"time"`
	// ExpiresAt is when the document leaves the index; zero keeps it until
	// its source is deleted.
	ExpiresAt time.Time `json:"-"`
	// SourceVersion lets reconciliation tell a stale copy of its source.
	SourceVersion string `json:"-"`
}

// HistoryScope is what one search may see.
type HistoryScope struct {
	// Admin is the Console administrator: every agent, or AgentID's when set.
	Admin bool
	// AgentID is the agent searching (or, for Admin, the agent filter).
	AgentID string
	// MemoryScopes are the memory entry scopes it reads.
	MemoryScopes []string
	// PersonID is the person the turn works for. Private documents of any
	// other person stay hidden.
	PersonID string
}

// HistoryDocQuery is one wording of a search, already tokenized.
type HistoryDocQuery struct {
	Terms []string
	// Phrase is the whole wording, lower-cased; a document holding it ranks
	// first and counts as a match even below MinMatched.
	Phrase     string
	MinMatched int
	Kinds      []string
	Scope      HistoryScope
	Limit      int
	Now        time.Time
}

// HistoryDocHit is a document a wording matched.
type HistoryDocHit struct {
	HistoryDoc
	Matched int
	Phrase  bool
}

// HistoryMemoryDocID is the document id of a memory entry.
func HistoryMemoryDocID(entryID string) string { return "mem:" + entryID }

// ActiveTurnIdentity is who the running turn of one channel conversation
// acts for.
type ActiveTurnIdentity struct {
	AgentID     string
	MemoryScope string
	ChannelID   string
	ChatID      string
	ChatType    string
	UserID      string
}

// ActiveTurnResolver finds the running turn of a channel conversation.
type ActiveTurnResolver interface {
	ActiveTurnIdentity(channelID, conversationKey string) (ActiveTurnIdentity, error)
}

// PrivateChatType reports whether chatType is a one-to-one chat. Platforms
// name it differently; anything not known to be a group with a person on it
// counts as private, so an unknown type never widens who sees a turn.
func PrivateChatType(chatType, userID string) bool {
	if strings.TrimSpace(userID) == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(chatType)) {
	case "group", "topic_group", "topic", "supergroup", "channel", "room", "thread":
		return false
	}
	return true
}

// AgentMemoryScope is the memory scope an agent reads besides "global".
func AgentMemoryScope(agentID, configured string) string {
	if scope := strings.TrimSpace(configured); scope != "" {
		return scope
	}
	if agentID = strings.TrimSpace(agentID); agentID != "" {
		return "agent:" + agentID
	}
	return ""
}
