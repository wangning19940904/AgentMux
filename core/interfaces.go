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
	ChatType  string
	UserID    string
	UserName  string
	Text      string
	Images    [][]byte
	Timestamp time.Time
	// Mention metadata is populated by platforms that can distinguish group
	// messages and bot mentions, such as Feishu/Lark.
	MentionedBot bool
	MentionAll   bool
	// Platform/Project routing context.
	Platform string
	Project  string
	// ChannelID routes the message to a console-managed channel runtime
	// instead of a config.toml project. Stamped by the Engine's channel relay.
	ChannelID string
	// Origin records what produced the message: channel, cron, webhook or api.
	Origin string
	// RuntimeSettingsAction is set by interactive setting cards. It is kept
	// separate from Text so callbacks cannot accidentally become agent prompts.
	RuntimeSettingsAction *RuntimeSettingsAction
	// InteractionMessageID is the card/message that an interactive action
	// updates. ID remains the unique inbound event id used for deduplication.
	InteractionMessageID string
}

// Message origins.
const (
	OriginChannel = "channel"
	OriginCron    = "cron"
	OriginWebhook = "webhook"
	OriginAPI     = "api"
)

// Event is something an AgentSession emits while processing a turn: streamed
// output, a user-visible reasoning summary, a tool call, a permission request,
// or the final answer.
type Event struct {
	Type    EventType
	Text    string
	Final   bool
	Err     error
	Usage   *TurnUsage
	ToolUse string
	// ToolName is the invoked tool's name (e.g. "Bash", "mcp__lark__im_send")
	// for EventToolUse events. ToolInput is a short, human-readable summary of
	// its arguments (already truncated by the adapter). ToolResult, when set on
	// a later EventToolUse, carries a short summary of that tool's output.
	ToolName   string
	ToolInput  string
	ToolResult string
}

// EventType enumerates the kinds of events an agent session emits.
type EventType string

const (
	EventOutput EventType = "output"
	// EventThinking carries an adapter-provided reasoning summary or progress
	// status. It deliberately is not raw private chain-of-thought.
	EventThinking   EventType = "thinking"
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

// StreamReplier is an optional capability a Platform can implement to render a
// single agent turn as one live, in-place updating message (e.g. a Feishu
// interactive card) instead of posting a new message per streamed event. The
// Engine prefers this path when available and falls back to Reply otherwise.
type StreamReplier interface {
	// BeginReply opens a streaming reply for the turn originated by msg and
	// returns a handle used to push incremental updates. done reports, on the
	// final update, whether the turn ended in error so the renderer can style
	// the message accordingly.
	BeginReply(ctx context.Context, msg *Message) (ReplyStream, error)
}

// ReplyStream is a live, in-place updating reply produced by a StreamReplier.
// Update may be called repeatedly with the full accumulated text so far; the
// implementation is responsible for rendering it. Close finalizes the message.
type ReplyStream interface {
	// Update renders text as the current content of the streaming message.
	// done marks this as the terminal update (turn finished); failed styles it
	// as an error when true.
	Update(ctx context.Context, text string, done, failed bool) error
	// Close releases any resources held by the stream.
	Close(ctx context.Context) error
}

// StreamMessageReplier is an optional Platform capability for rendering a
// whole agent turn as one in-place updating plain-text message.
type StreamMessageReplier interface {
	BeginMessageReply(ctx context.Context, msg *Message) (ReplyStream, error)
}

// ModelPickerReplier is an optional Platform capability for rendering the
// /model status command as an interactive model picker.
type ModelPickerReplier interface {
	ReplyModelPicker(ctx context.Context, msg *Message, state ModelPickerState) error
}

// RuntimeSettingsPickerReplier renders and updates the richer model/effort/
// speed picker. UpdateRuntimeSettingsPicker must edit the original picker
// message referenced by msg.ID instead of posting a confirmation message.
type RuntimeSettingsPickerReplier interface {
	ReplyRuntimeSettingsPicker(ctx context.Context, msg *Message, state RuntimeSettingsPickerState) error
	UpdateRuntimeSettingsPicker(ctx context.Context, msg *Message, state RuntimeSettingsPickerState) error
}

// MessageReactioner is an optional Platform capability for marking an inbound
// message while work is in progress and removing that mark when finished.
type MessageReactioner interface {
	AddReaction(ctx context.Context, msg *Message, emojiType string) (reactionID string, err error)
	DeleteReaction(ctx context.Context, msg *Message, reactionID string) error
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

// ResumableAgent is an optional Agent capability: starting a session that
// resumes a prior agent-native session id so context carries across process
// restarts. Agents that cannot resume simply omit this interface and the
// Engine starts a fresh session instead.
type ResumableAgent interface {
	// StartSessionResume spawns a session bound to workDir that resumes the
	// conversation identified by resumeID (agent-native session id). When
	// resumeID is empty it behaves like StartSession.
	StartSessionResume(ctx context.Context, workDir, resumeID string) (AgentSession, error)
}

// NativeSessioned is an optional AgentSession capability exposing the
// agent-native session id (resume handle) discovered while running turns. The
// Engine persists this onto the durable Conversation so later turns and
// restarts can resume.
type NativeSessioned interface {
	NativeSessionID() string
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
