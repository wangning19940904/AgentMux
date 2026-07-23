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
	ID       string
	ChatID   string
	ChatType string
	// RootID, ParentID and ThreadID preserve transport-native conversation
	// identity. They let group topics bind to independent native agent threads
	// without leaking platform-specific event types into the engine.
	RootID   string
	ParentID string
	ThreadID string
	// ConversationKey is the normalized durable routing key. Platforms may
	// populate it directly (for callbacks); otherwise the engine derives it
	// from the fields above before dispatch.
	ConversationKey string
	UserID          string
	UserName        string
	Text            string
	Images          [][]byte
	Timestamp       time.Time
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
	// AgentInteractionAction carries an idempotent response to a pending native
	// agent approval or request_user_input prompt.
	AgentInteractionAction *AgentInteractionAction
	// InteractionMessageID is the card/message that an interactive action
	// updates. ID remains the unique inbound event id used for deduplication.
	InteractionMessageID string
	// Callback carries structured metadata for transport callbacks such as a
	// Feishu card.action.trigger event. Callback tokens are intentionally not
	// retained here because channel logs are durable and user-readable.
	Callback *CallbackEvent
	// LogOnly persists the inbound event without dispatching it to hooks or an
	// agent session. Platforms use this for callbacks that are observable but
	// are not an AgentNexus control action.
	LogOnly bool
}

// CallbackEvent is the transport-neutral subset of an interactive callback
// that is safe and useful to retain in a channel JSONL log.
type CallbackEvent struct {
	Type        string
	MessageID   string
	Host        string
	ActionTag   string
	ActionName  string
	ActionValue string
	FormValue   string
	InputValue  string
	Option      string
	Options     string
	Checked     bool
	Timezone    string
}

// AgentInteractionKind is a native agent request that must be resolved before
// the active turn can continue.
type AgentInteractionKind string

const (
	AgentInteractionCommandApproval    AgentInteractionKind = "command_approval"
	AgentInteractionFileChangeApproval AgentInteractionKind = "file_change_approval"
	AgentInteractionPermissionApproval AgentInteractionKind = "permission_approval"
	AgentInteractionUserInput          AgentInteractionKind = "user_input"
)

// AgentInteraction describes a correlation-safe approval or input request.
// RawParams is adapter-private context required to produce a protocol-correct
// response; renderers must not display or persist it.
type AgentInteraction struct {
	ID               string                `json:"id"`
	Kind             AgentInteractionKind  `json:"kind"`
	ThreadID         string                `json:"thread_id,omitempty"`
	TurnID           string                `json:"turn_id,omitempty"`
	ItemID           string                `json:"item_id,omitempty"`
	Title            string                `json:"title,omitempty"`
	Description      string                `json:"description,omitempty"`
	Command          string                `json:"command,omitempty"`
	Cwd              string                `json:"cwd,omitempty"`
	Reason           string                `json:"reason,omitempty"`
	HighRisk         bool                  `json:"high_risk,omitempty"`
	Questions        []InteractionQuestion `json:"questions,omitempty"`
	AutoResolutionMs int64                 `json:"auto_resolution_ms,omitempty"`
	RawParams        map[string]any        `json:"-"`
}

type InteractionQuestion struct {
	ID       string              `json:"id"`
	Header   string              `json:"header,omitempty"`
	Question string              `json:"question"`
	Secret   bool                `json:"secret,omitempty"`
	Other    bool                `json:"other,omitempty"`
	Options  []InteractionOption `json:"options,omitempty"`
}

type InteractionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// AgentInteractionResponse is transport-neutral. Adapters translate Decision
// and Answers into their native JSON-RPC result shape.
type AgentInteractionResponse struct {
	Decision string              `json:"decision,omitempty"`
	Answers  map[string][]string `json:"answers,omitempty"`
}

// AgentInteractionAction is submitted by a channel card or the local console.
// Nonce makes the action single-use and prevents cross-card replay.
type AgentInteractionAction struct {
	InteractionID string              `json:"interaction_id"`
	TaskID        string              `json:"task_id,omitempty"`
	Nonce         string              `json:"nonce"`
	Decision      string              `json:"decision,omitempty"`
	Answers       map[string][]string `json:"answers,omitempty"`
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
	Type       EventType
	EventID    string
	TurnID     string
	ItemID     string
	Text       string
	Final      bool
	Err        error
	Status     string
	DurationMs int64
	Usage      *TurnUsage
	ToolUse    string
	// ToolName is the invoked tool's name (e.g. "Bash", "mcp__lark__im_send")
	// for EventToolUse events. ToolInput is a short, human-readable summary of
	// its arguments (already truncated by the adapter). ToolResult, when set on
	// a later EventToolUse, carries a short summary of that tool's output.
	ToolCallID    string
	ToolName      string
	ToolInput     string
	ToolInputRaw  string
	ToolResult    string
	ToolResultRaw string
	// Metadata carries adapter-specific correlation and coverage hints without
	// forcing renderers to understand every native protocol field.
	Metadata map[string]string
	// Interaction is populated on EventPermission when the native agent blocks
	// awaiting an approval or explicit user input.
	Interaction *AgentInteraction
}

// EventType enumerates the kinds of events an agent session emits.
type EventType string

const (
	EventOutput EventType = "output"
	// EventThinking carries an adapter-provided reasoning summary or progress
	// status. It deliberately is not raw private chain-of-thought.
	EventThinking      EventType = "thinking"
	EventToolUse       EventType = "tool_use"
	EventModelRequest  EventType = "model_request"
	EventModelResponse EventType = "model_response"
	EventCompaction    EventType = "compaction"
	EventPermission    EventType = "permission"
	EventFinal         EventType = "final"
	EventError         EventType = "error"
)

// TurnUsage carries token accounting for a single agent turn.
type TurnUsage struct {
	Model            string
	RequestID        string
	RequestedModel   string
	ResolvedModel    string
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	ReasoningTokens  int64
	TotalTokens      int64
	Cumulative       bool
	Attempt          int
	TTFTMs           int64
	DurationMs       int64
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

// PlatformHealth is a transport-neutral snapshot of a long-lived platform
// connection. Messaging adapters that maintain sockets can expose it through
// PlatformHealthReporter so the channel supervisor can detect connections
// that are reconnecting or still look open but have stopped receiving
// heartbeats.
type PlatformHealth struct {
	State           string
	Connected       bool
	Error           string
	CheckedAt       time.Time
	ConnectedAt     time.Time
	LastHeartbeatAt time.Time
	LastEventAt     time.Time
	LastInboundAt   time.Time
}

// PlatformHealthReporter is an optional Platform capability. Implementations
// must return quickly and must not perform network I/O; the Engine polls this
// method on a watchdog interval.
type PlatformHealthReporter interface {
	ChannelHealth() PlatformHealth
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

// AgentInteractionReplier is an optional rich-channel capability for
// rendering a pending native agent approval or question.
type AgentInteractionReplier interface {
	ReplyAgentInteraction(ctx context.Context, msg *Message, task ChannelTask, interaction ChannelInteraction) (messageID string, err error)
}

type AgentInteractionUpdateReplier interface {
	UpdateAgentInteraction(ctx context.Context, msg *Message, interaction ChannelInteraction, outcome string) error
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

type NativeThread struct {
	ID        string    `json:"id"`
	Title     string    `json:"title,omitempty"`
	Preview   string    `json:"preview,omitempty"`
	WorkDir   string    `json:"work_dir,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// NativeThreadAgent exposes the native desktop/app-server thread catalog used
// by channel /sessions and /bind commands.
type NativeThreadAgent interface {
	ListNativeThreads(ctx context.Context, workDir string) ([]NativeThread, error)
}

// NativeThreadOpener opens an exact native thread in the local desktop app
// when supported, otherwise returning a truthful CLI fallback.
type NativeThreadOpener interface {
	OpenNativeThread(ctx context.Context, threadID string) (opened bool, fallbackCommand string, err error)
}

type CodexControlCapability struct {
	State        string `json:"state"`
	Error        string `json:"error,omitempty"`
	Experimental bool   `json:"experimental_api"`
	Threads      bool   `json:"threads"`
	Steer        bool   `json:"steer"`
	Interrupt    bool   `json:"interrupt"`
	Interactions bool   `json:"interactions"`
	DeepLink     bool   `json:"deep_link"`
}

type CodexControlCapabilityReporter interface {
	CodexControlCapability() CodexControlCapability
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

// InteractiveAgentSession is implemented by runtimes that support mutating an
// active turn and resolving correlated native interactions.
type InteractiveAgentSession interface {
	AgentSession
	Steer(ctx context.Context, text string) error
	Interrupt(ctx context.Context) error
	ResolveInteraction(ctx context.Context, interactionID string, response AgentInteractionResponse) error
	ActiveTurnID() string
}
