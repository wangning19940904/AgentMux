// Package core defines the central interfaces and the plugin registry that
// the rest of AgentNexus builds on. core must never import from the
// platform/, agent/, provider/ or usage/ packages: adapters register
// themselves here via the registry instead.
package core

import (
	"context"
	"time"
)

// Message is an inbound or outbound message flowing between a Platform and an
// Agent. It is intentionally transport-agnostic.
type Message struct {
	ID        string
	ChatID    string
	UserID    string
	UserName  string
	Text      string
	Images    [][]byte
	Timestamp time.Time
	// Platform/Project routing context.
	Platform string
	Project  string
	// ChannelID routes the message to a console-managed channel runtime
	// instead of a config.toml project. Stamped by the Engine's channel relay.
	ChannelID string
	// Origin records what produced the message: channel, cron, webhook or api.
	Origin string
}

// Message origins.
const (
	OriginChannel = "channel"
	OriginCron    = "cron"
	OriginWebhook = "webhook"
	OriginAPI     = "api"
)

// Event is something an AgentSession emits while processing a turn: streamed
// output, a tool call, a permission request, or the final answer.
type Event struct {
	Type    EventType
	Text    string
	Final   bool
	Err     error
	Usage   *TurnUsage
	ToolUse string
}

// EventType enumerates the kinds of events an agent session emits.
type EventType string

const (
	EventOutput     EventType = "output"
	EventToolUse    EventType = "tool_use"
	EventPermission EventType = "permission"
	EventFinal      EventType = "final"
	EventError      EventType = "error"
)

// TurnUsage carries token accounting for a single agent turn.
type TurnUsage struct {
	Model            string
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
}

// Platform is a messaging integration (Feishu, Slack, Telegram, ...). It
// receives inbound messages and delivers rendered responses.
type Platform interface {
	// Name returns the registered platform type, e.g. "feishu".
	Name() string
	// Start begins listening for inbound messages, delivering them on inbound.
	Start(ctx context.Context, inbound chan<- *Message) error
	// Reply sends a response to the chat that originated msg.
	Reply(ctx context.Context, msg *Message, text string) error
	// Send delivers an unsolicited message to a chat.
	Send(ctx context.Context, chatID, text string) error
	// Stop releases platform resources.
	Stop(ctx context.Context) error
}

// Agent is an AI coding agent adapter (Claude Code, Codex, ...). It knows how
// to spawn and manage agent sessions.
type Agent interface {
	// Name returns the registered agent type, e.g. "claudecode".
	Name() string
	// StartSession spawns a new bidirectional session bound to workDir.
	StartSession(ctx context.Context, workDir string) (AgentSession, error)
	// ListSessions returns known session IDs for this agent.
	ListSessions(ctx context.Context) ([]string, error)
	// Stop releases agent-level resources.
	Stop(ctx context.Context) error
}

// AgentSession is a running conversation with an agent.
type AgentSession interface {
	// ID returns the agent-native session identifier.
	ID() string
	// Send submits user input and returns a channel of streamed events.
	Send(ctx context.Context, text string) (<-chan *Event, error)
	// RespondPermission answers a pending permission request.
	RespondPermission(ctx context.Context, allow bool) error
	// Close terminates the session.
	Close(ctx context.Context) error
}
