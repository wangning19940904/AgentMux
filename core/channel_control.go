package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
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

func ChannelMeetingReplyMode(ch Channel) string {
	switch ChannelMeetingResponseMode(ch) {
	case MeetingResponseModeFinalText, MeetingResponseModeVoice:
		return MeetingReplyModeFinal
	default:
		return MeetingReplyModeStream
	}
}

// NormalizeMeetingResponseMode accepts the canonical API values and the
// compact aliases used by /meeting. It returns an empty string for unknown
// input so callers can report a useful validation error.
func NormalizeMeetingResponseMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case MeetingResponseModeStreamText, "stream", "streaming", "流式", "流式文字":
		return MeetingResponseModeStreamText
	case MeetingResponseModeFinalText, "final", "nonstream", "non_stream", "非流式", "非流式文字":
		return MeetingResponseModeFinalText
	case MeetingResponseModeTextVoice, "text+voice", "文字+语音", "文字语音":
		return MeetingResponseModeTextVoice
	case MeetingResponseModeVoice, "voice_only", "语音", "仅语音":
		return MeetingResponseModeVoice
	default:
		return ""
	}
}

// ChannelMeetingResponseMode maps legacy meeting_reply_mode +
// meeting_voice_enabled records to the four user-facing response modes.
func ChannelMeetingResponseMode(ch Channel) string {
	if mode := NormalizeMeetingResponseMode(ch.Config[ChannelConfigMeetingResponseMode]); mode != "" {
		return mode
	}
	voice := parseChannelBool(ch.Config[ChannelConfigMeetingVoice])
	if voice {
		return MeetingResponseModeTextVoice
	}
	if strings.EqualFold(strings.TrimSpace(ch.Config[ChannelConfigMeetingReplyMode]), MeetingReplyModeFinal) {
		return MeetingResponseModeFinalText
	}
	return DefaultMeetingResponseMode
}

func MeetingResponseModeUsesText(mode string) bool {
	switch NormalizeMeetingResponseMode(mode) {
	case MeetingResponseModeStreamText, MeetingResponseModeFinalText, MeetingResponseModeTextVoice:
		return true
	default:
		return false
	}
}

func MeetingResponseModeUsesVoice(mode string) bool {
	switch NormalizeMeetingResponseMode(mode) {
	case MeetingResponseModeTextVoice, MeetingResponseModeVoice:
		return true
	default:
		return false
	}
}

const (
	maxMeetingVoiceWakeWords = 20
	maxMeetingVoiceWakeRunes = 64
)

// ParseMeetingVoiceWakeWords accepts the comma-, semicolon-, or newline-
// separated value stored in channel config. Spaces remain part of a wake word
// so names such as "Meeting Bot" continue to work.
func ParseMeetingVoiceWakeWords(value string) ([]string, error) {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '；' || r == '\n' || r == '\r'
	})
	words := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		word := strings.TrimSpace(part)
		if word == "" {
			continue
		}
		if len([]rune(word)) > maxMeetingVoiceWakeRunes {
			return nil, fmt.Errorf("meeting voice wake word %q exceeds %d characters", word, maxMeetingVoiceWakeRunes)
		}
		key := strings.ToLower(word)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		words = append(words, word)
		if len(words) > maxMeetingVoiceWakeWords {
			return nil, fmt.Errorf("meeting voice wake words exceed %d entries", maxMeetingVoiceWakeWords)
		}
	}
	return words, nil
}

// NormalizeMeetingVoiceWakeWords returns the stable newline-separated form
// used by the channel editor and persistence layer.
func NormalizeMeetingVoiceWakeWords(value string) (string, error) {
	words, err := ParseMeetingVoiceWakeWords(value)
	if err != nil {
		return "", err
	}
	return strings.Join(words, "\n"), nil
}

// ApplyMeetingResponseMode stores the canonical mode and keeps the legacy
// component keys aligned so older AgentMux versions retain sensible behavior.
func ApplyMeetingResponseMode(ch *Channel, value string) error {
	mode := NormalizeMeetingResponseMode(value)
	if mode == "" {
		return fmt.Errorf("invalid meeting response mode %q", strings.TrimSpace(value))
	}
	if ch.Config == nil {
		ch.Config = map[string]string{}
	}
	ch.Config[ChannelConfigMeetingResponseMode] = mode
	if MeetingResponseModeUsesVoice(mode) {
		ch.Config[ChannelConfigMeetingVoice] = "true"
	} else {
		ch.Config[ChannelConfigMeetingVoice] = "false"
	}
	if mode == MeetingResponseModeFinalText || mode == MeetingResponseModeVoice {
		ch.Config[ChannelConfigMeetingReplyMode] = MeetingReplyModeFinal
	} else {
		ch.Config[ChannelConfigMeetingReplyMode] = MeetingReplyModeStream
	}
	return nil
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
