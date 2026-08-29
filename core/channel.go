package core

import (
	"context"
	"time"
)

// Channel is a first-class messaging connection: one configured platform
// adapter (feishu, telegram, dingtalk, slack, discord, webhook, ...) bound to
// an Agent instance. Channels live in SQLite, are managed from the console and
// are attached to the Engine at runtime — the console-managed counterpart of
// config.toml's [[projects.platforms]].
type Channel struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Type            string            `json:"type"`
	AgentID         string            `json:"agent_id,omitempty"`
	Config          map[string]string `json:"config,omitempty"`
	Enabled         bool              `json:"enabled"`
	OwnerTenantID   string            `json:"owner_tenant_id,omitempty"`
	OwnerTenantName string            `json:"owner_tenant_name,omitempty"`
	Visibility      string            `json:"visibility,omitempty"` // private, public
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

// ChannelStatus reports the live state of a channel attached to the Engine.
type ChannelStatus struct {
	ChannelID       string    `json:"channel_id"`
	State           string    `json:"state"` // starting, running, reconnecting, degraded, error, stopped
	Connected       bool      `json:"connected"`
	Error           string    `json:"error,omitempty"`
	StartedAt       time.Time `json:"started_at,omitempty"`
	ConnectedAt     time.Time `json:"connected_at,omitempty"`
	LastCheckedAt   time.Time `json:"last_checked_at,omitempty"`
	LastHeartbeatAt time.Time `json:"last_heartbeat_at,omitempty"`
	LastEventAt     time.Time `json:"last_event_at,omitempty"`
	LastInboundAt   time.Time `json:"last_inbound_at,omitempty"`
}

// Channel runtime states.
const (
	ChannelStateStarting     = "starting"
	ChannelStateRunning      = "running"
	ChannelStateReconnecting = "reconnecting"
	ChannelStateDegraded     = "degraded"
	ChannelStateError        = "error"
	ChannelStateStopped      = "stopped"
)

// Feishu/Lark channel config keys.
const (
	ChannelConfigReplyScope          = "reply_scope"
	ChannelConfigReplyMode           = "reply_mode"
	ChannelConfigAckReaction         = "ack_reaction_enabled"
	ChannelConfigAckReactionEmojis   = "ack_reaction_emojis"
	ChannelConfigMeetingVoice        = "meeting_voice_enabled"
	ChannelConfigMeetingTTSBaseURL   = "meeting_voice_tts_base_url"
	ChannelConfigMeetingTTSAPIKey    = "meeting_voice_tts_api_key"
	ChannelConfigMeetingTTSModel     = "meeting_voice_tts_model"
	ChannelConfigMeetingTTSVoice     = "meeting_voice_tts_voice"
	ChannelConfigMeetingTTSMode      = "meeting_voice_tts_mode"
	ChannelConfigMeetingLocalModel   = "meeting_voice_local_model"
	ChannelConfigMeetingLocalVoice   = "meeting_voice_local_voice"
	ChannelConfigMeetingWakeWords    = "meeting_voice_wake_words"
	ChannelConfigMeetingGreeting     = "meeting_greeting"
	ChannelConfigMeetingReplyMode    = "meeting_reply_mode"
	ChannelConfigMeetingResponseMode = "meeting_response_mode"
)

// Channel reply scopes.
const (
	ReplyScopeDMAndMentions = "dm_and_mentions"
	ReplyScopeAll           = "all"
	ReplyScopeMentionsOnly  = "mentions_only"
)

// Channel reply modes.
const (
	ReplyModeStreamMessage = "stream_message"
	ReplyModeStreamCard    = "stream_card"
)

// Meeting reply modes control how Agent output is delivered to the in-meeting
// chat. Stream mode sends throttled coherent chunks; final mode sends exactly
// once after the Agent event stream has closed.
const (
	MeetingReplyModeStream = "stream"
	MeetingReplyModeFinal  = "final"
)

// Meeting response modes are the user-facing combinations exposed by the
// meeting console and /meeting command. The older reply-mode and voice keys
// remain synchronized for backwards compatibility with existing channels.
const (
	MeetingResponseModeStreamText = "stream_text"
	MeetingResponseModeFinalText  = "final_text"
	MeetingResponseModeTextVoice  = "text_voice"
	MeetingResponseModeVoice      = "voice"
)

const (
	MeetingTTSModeAPI          = "api"
	MeetingTTSModeLocal        = "local"
	DefaultAckReactionEnabled  = "true"
	DefaultAckReactionEmojis   = "OK,THUMBSUP,MUSCLE,THANKS"
	DefaultMeetingVoice        = "false"
	DefaultMeetingTTSBaseURL   = "https://api.openai.com/v1"
	DefaultMeetingTTSModel     = "gpt-4o-mini-tts"
	DefaultMeetingTTSVoice     = "alloy"
	DefaultMeetingTTSMode      = MeetingTTSModeAPI
	DefaultMeetingLocalModel   = "kokoro-82m-zh-int8"
	DefaultMeetingLocalVoice   = "3"
	DefaultMeetingReplyMode    = MeetingReplyModeStream
	DefaultMeetingResponseMode = MeetingResponseModeStreamText
)

// Trigger kinds. A trigger is the unified automation entry: cron schedules,
// inbound webhooks and engine lifecycle events all flow through it
// (cc-connect's CronJob + WebhookServer + hooks, unified).
const (
	TriggerCron    = "cron"
	TriggerWebhook = "webhook"
	TriggerEvent   = "event"
)

// Trigger session modes (aligned with cc-connect's cron session_mode).
const (
	SessionModeReuse     = "reuse"
	SessionModeNewPerRun = "new_per_run"
)

// Trigger event action types.
const (
	ActionShell = "shell"
	ActionHTTP  = "http"
)

// Trigger is one automation rule.
//
//   - kind=cron:    CronExpr fires Prompt against the bound agent; the final
//     answer is pushed to ChannelID/ChatID when set.
//   - kind=webhook: POST /hook/{id} (authenticated by Token) fires Prompt plus
//     the request payload against the bound agent, same delivery.
//   - kind=event:   Event (message.received, cron.triggered, error, ...) runs
//     ActionType/ActionTarget (shell command or HTTP POST callback),
//     optionally filtered to one ChannelID.
type Trigger struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Kind         string    `json:"kind"`
	AgentID      string    `json:"agent_id,omitempty"`
	ChannelID    string    `json:"channel_id,omitempty"`
	ChatID       string    `json:"chat_id,omitempty"`
	CronExpr     string    `json:"cron_expr,omitempty"`
	Prompt       string    `json:"prompt,omitempty"`
	Event        string    `json:"event,omitempty"`
	ActionType   string    `json:"action_type,omitempty"`
	ActionTarget string    `json:"action_target,omitempty"`
	Token        string    `json:"token,omitempty"`
	SessionMode  string    `json:"session_mode,omitempty"`
	Enabled       bool      `json:"enabled"`
	LastRun       time.Time `json:"last_run,omitempty"`
	LastStatus    string    `json:"last_status,omitempty"` // running, ok, error
	LastError     string    `json:"last_error,omitempty"`
	OwnerTenantID string    `json:"owner_tenant_id,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// ConnectStore is the persistence surface the connect runtime (channels +
// triggers) needs. Implemented by store.Store; declared here because core
// never imports store.
type ConnectStore interface {
	ListChannels(ctx context.Context) ([]Channel, error)
	GetChannel(ctx context.Context, id string) (*Channel, error)
	ListTriggers(ctx context.Context) ([]Trigger, error)
	GetTrigger(ctx context.Context, id string) (*Trigger, error)
	UpdateTriggerRun(ctx context.Context, id string, lastRun time.Time, status, errMsg string) error
	GetAgentInstance(ctx context.Context, id string) (*AgentInstance, error)
	UpdateAgentRuntimeSettings(ctx context.Context, id string, settings RuntimeSettings) error
	GetProvider(ctx context.Context, id string) (*Provider, error)
	ActiveProviderRoutes(ctx context.Context) ([]ProviderRoute, error)
}
