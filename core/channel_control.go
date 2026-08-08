package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

const (
	ChannelConfigCodexControlEnabled = "codex_control_enabled"
	ChannelConfigAllowedUserIDs      = "allowed_user_ids"
	ChannelConfigAdminUserIDs        = "admin_user_ids"
	ChannelConfigCodexMaxQueue       = "codex_max_queue"
	ChannelConfigCodexTurnTimeout    = "codex_turn_timeout_minutes"
	ChannelConfigTurnTimeout         = "turn_timeout_minutes"

	DefaultCodexMaxQueue             = 20
	DefaultCodexTurnTimeoutMinutes   = 20
	DefaultChannelTurnTimeoutMinutes = 20
)

type ChannelTaskStatus string

const (
	ChannelTaskQueued       ChannelTaskStatus = "queued"
	ChannelTaskRunning      ChannelTaskStatus = "running"
	ChannelTaskWaitingInput ChannelTaskStatus = "waiting_input"
	ChannelTaskSucceeded    ChannelTaskStatus = "succeeded"
	ChannelTaskFailed       ChannelTaskStatus = "failed"
	ChannelTaskCancelled    ChannelTaskStatus = "cancelled"
	ChannelTaskInterrupted  ChannelTaskStatus = "interrupted"
)

const ChannelTaskActionStop = "stop"

// ChannelTaskAction is an id-scoped control submitted by an interactive task
// card. Keeping the task ID separate from a plain /stop command prevents a
// delayed click on an old card from interrupting a newer task.
type ChannelTaskAction struct {
	TaskID string
	Action string
}

// ChannelTask is the durable, prompt-redacted task summary exposed to the
// console. Prompt is only populated internally while the task is queued.
type ChannelTask struct {
	ID              string            `json:"id"`
	ChannelID       string            `json:"channel_id"`
	ConversationID  string            `json:"conversation_id,omitempty"`
	ConversationKey string            `json:"conversation_key"`
	ChatID          string            `json:"chat_id"`
	MessageID       string            `json:"-"`
	ChatType        string            `json:"-"`
	RootID          string            `json:"-"`
	ThreadID        string            `json:"-"`
	UserID          string            `json:"user_id"`
	ControllerID    string            `json:"controller_id"`
	NativeThreadID  string            `json:"native_thread_id,omitempty"`
	TurnID          string            `json:"turn_id,omitempty"`
	Status          ChannelTaskStatus `json:"status"`
	Error           string            `json:"error,omitempty"`
	Prompt          string            `json:"-"`
	CreatedAt       time.Time         `json:"created_at"`
	StartedAt       time.Time         `json:"started_at,omitempty"`
	FinishedAt      time.Time         `json:"finished_at,omitempty"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

type ChannelInteractionStatus string

const (
	ChannelInteractionPending  ChannelInteractionStatus = "pending"
	ChannelInteractionResolved ChannelInteractionStatus = "resolved"
	ChannelInteractionDeclined ChannelInteractionStatus = "declined"
	ChannelInteractionExpired  ChannelInteractionStatus = "expired"
)

type ChannelInteraction struct {
	ID              string                   `json:"id"`
	TaskID          string                   `json:"task_id"`
	ChannelID       string                   `json:"channel_id"`
	ConversationID  string                   `json:"conversation_id,omitempty"`
	ConversationKey string                   `json:"conversation_key"`
	ControllerID    string                   `json:"controller_id"`
	Nonce           string                   `json:"nonce,omitempty"`
	MessageID       string                   `json:"message_id,omitempty"`
	Status          ChannelInteractionStatus `json:"status"`
	Request         AgentInteraction         `json:"request"`
	CreatedAt       time.Time                `json:"created_at"`
	ExpiresAt       time.Time                `json:"expires_at,omitempty"`
	ResolvedAt      time.Time                `json:"resolved_at,omitempty"`
	ResolvedBy      string                   `json:"resolved_by,omitempty"`
}

// ChannelControlStore persists queue and interaction state. The engine treats
// it as optional so config.toml projects and lightweight test stores continue
// to work without implementing remote control.
type ChannelControlStore interface {
	CreateChannelTask(ctx context.Context, task ChannelTask) error
	UpdateChannelTask(ctx context.Context, task ChannelTask) error
	ListChannelTasks(ctx context.Context, channelID, conversationID string, activeOnly bool) ([]ChannelTask, error)
	RecoverChannelTasks(ctx context.Context, channelID string) ([]ChannelTask, error)
	CreateChannelInteraction(ctx context.Context, interaction ChannelInteraction) error
	UpdateChannelInteractionMessage(ctx context.Context, id, messageID string) error
	ResolveChannelInteraction(ctx context.Context, id, nonce, actor string, status ChannelInteractionStatus) (bool, error)
	GetChannelInteraction(ctx context.Context, id string) (*ChannelInteraction, error)
	ListChannelInteractions(ctx context.Context, channelID, conversationID string, pendingOnly bool) ([]ChannelInteraction, error)
}

func CodexRemoteControlEnabled(ch Channel) bool {
	return parseChannelBool(ch.Config[ChannelConfigCodexControlEnabled])
}

func ChannelAllowedUsers(ch Channel) map[string]bool {
	return splitChannelIDs(ch.Config[ChannelConfigAllowedUserIDs])
}

func ChannelAdminUsers(ch Channel) map[string]bool {
	return splitChannelIDs(ch.Config[ChannelConfigAdminUserIDs])
}

func ChannelCodexMaxQueue(ch Channel) int {
	n, _ := strconv.Atoi(strings.TrimSpace(ch.Config[ChannelConfigCodexMaxQueue]))
	if n <= 0 {
		return DefaultCodexMaxQueue
	}
	if n > 100 {
		return 100
	}
	return n
}

func ChannelCodexTurnTimeout(ch Channel) time.Duration {
	return ChannelTurnTimeout(ch)
}

// ChannelTurnTimeout bounds every channel-originated agent turn. The generic
// key supersedes the original Codex-only key; the legacy value remains a
// fallback so existing channel records keep their configured behavior.
func ChannelTurnTimeout(ch Channel) time.Duration {
	raw := strings.TrimSpace(ch.Config[ChannelConfigTurnTimeout])
	if raw == "" {
		raw = strings.TrimSpace(ch.Config[ChannelConfigCodexTurnTimeout])
	}
	n, _ := strconv.Atoi(raw)
	if n <= 0 {
		n = DefaultChannelTurnTimeoutMinutes
	}
	if n > 240 {
		n = 240
	}
	return time.Duration(n) * time.Minute
}

func NewChannelControlID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + "-" + hex.EncodeToString(b)
}

func splitChannelIDs(raw string) map[string]bool {
	out := map[string]bool{}
	for _, value := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\t' || r == ' '
	}) {
		if value = strings.TrimSpace(value); value != "" {
			out[value] = true
		}
	}
	return out
}

func parseChannelBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
