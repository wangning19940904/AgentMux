package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ChannelDelivery is an out-of-band payload produced by the agent that owns
// the currently running channel turn. The channel and conversation keys are
// both required so a local helper cannot accidentally publish into a different
// conversation.
type ChannelDelivery struct {
	ChannelID       string
	ConversationKey string
	Text            string
	Images          []ChannelDeliveryFile
	Files           []ChannelDeliveryFile
}

type ChannelDeliveryFile struct {
	Name string
	Data []byte
}

// ChannelDeliverySender sends an out-of-band payload to an active managed
// channel conversation.
type ChannelDeliverySender interface {
	SendToChannel(ctx context.Context, delivery ChannelDelivery) error
}

// ConversationSender is the richer console-chat capability implemented by
// Engine. It lets the management UI continue an AgentMux-managed channel
// conversation without publishing the console operator's message back to the
// external channel.
type ConversationSender interface {
	SendToConversation(ctx context.Context, channelID, conversationID, text string) (string, error)
}

// ConversationRuntimeState is the live execution state for one managed
// channel conversation. It intentionally describes only work owned by this
// AgentMux process; native sessions running in another app are never reported
// as stoppable.
type ConversationRuntimeState struct {
	Status  string `json:"status"`
	CanStop bool   `json:"can_stop"`
	TaskID  string `json:"task_id,omitempty"`
}

// ConversationRuntimeController lets the local management API inspect and
// interrupt the exact in-memory turn backing a channel conversation.
type ConversationRuntimeController interface {
	ConversationRuntimeState(ctx context.Context, channelID, conversationKey string) (ConversationRuntimeState, error)
	StopConversation(ctx context.Context, channelID, conversationKey, expectedTaskID string) (ConversationRuntimeState, error)
}

// ConversationTerminalController exposes only AgentMux-managed terminal
// sessions. The full Conversation is supplied by the authenticated management
// server so callers cannot substitute a different native session id or work
// directory in the request body.
type ConversationTerminalController interface {
	TerminalSessionInfo(ctx context.Context, channelID string, conversation Conversation) (TerminalSessionInfo, error)
	TerminalSnapshot(ctx context.Context, channelID string, conversation Conversation) (string, error)
	WriteTerminal(ctx context.Context, channelID string, conversation Conversation, text string, submit bool) error
	ResizeTerminal(ctx context.Context, channelID string, conversation Conversation, columns, rows int) error
}

// SendToChannel delivers text and artifacts to the exact conversation that
// owns a live turn. It intentionally refuses inactive conversations: callers
// must obtain channel_id and conversation_key from the injected turn metadata,
// and stale commands cannot publish after that turn has finished.
func (e *Engine) SendToChannel(ctx context.Context, delivery ChannelDelivery) error {
	channelID := strings.TrimSpace(delivery.ChannelID)
	conversationKey := strings.TrimSpace(delivery.ConversationKey)
	if channelID == "" || conversationKey == "" {
		return errors.New("channel and conversation key are required")
	}
	if strings.TrimSpace(delivery.Text) == "" && len(delivery.Images) == 0 && len(delivery.Files) == 0 {
		return errors.New("text, image, or file is required")
	}
	rt := e.channelRuntime(channelID)
	if rt == nil {
		return fmt.Errorf("channel %q is not running", channelID)
	}

	var msg *Message
	rt.controlMu.Lock()
	if state := rt.controlTasks[conversationKey]; state != nil && state.active != nil && state.active.msg != nil {
		copy := *state.active.msg
		msg = &copy
	}
	active := msg != nil || rt.directTurns[conversationKey] != nil
	rt.controlMu.Unlock()
	if !active {
		return fmt.Errorf("conversation %q has no active turn", conversationKey)
	}
	if msg == nil {
		if e.conversations == nil {
			return errors.New("conversation store is unavailable")
		}
		conversations, err := e.conversations.ListConversations(ctx, "channel:"+channelID, false)
		if err != nil {
			return err
		}
		for i := range conversations {
			if conversations[i].ConversationKey == conversationKey {
				msg = &Message{
					ChatID:          conversations[i].ChatID,
					ChatType:        conversations[i].ChatType,
					ConversationKey: conversationKey,
					Platform:        rt.channel.Type,
					ChannelID:       channelID,
				}
				break
			}
		}
	}
	if msg == nil || strings.TrimSpace(msg.ChatID) == "" {
		return fmt.Errorf("conversation %q has no delivery target", conversationKey)
	}
	var imageReplier ChannelImageReplier
	if len(delivery.Images) > 0 {
		var ok bool
		imageReplier, ok = rt.platform.(ChannelImageReplier)
		if !ok {
			return fmt.Errorf("channel %q does not support image delivery", channelID)
		}
	}
	var fileReplier ChannelFileReplier
	if len(delivery.Files) > 0 {
		var ok bool
		fileReplier, ok = rt.platform.(ChannelFileReplier)
		if !ok {
			return fmt.Errorf("channel %q does not support file delivery", channelID)
		}
	}

	if text := strings.TrimSpace(delivery.Text); text != "" {
		if err := rt.platform.Reply(ctx, msg, text); err != nil {
			return err
		}
	}
	if len(delivery.Images) > 0 {
		for _, image := range delivery.Images {
			if err := imageReplier.ReplyImage(ctx, msg, image.Name, image.Data); err != nil {
				return err
			}
		}
	}
	if len(delivery.Files) > 0 {
		for _, file := range delivery.Files {
			if err := fileReplier.ReplyFile(ctx, msg, file.Name, file.Data); err != nil {
				return err
			}
		}
	}
	return nil
}

// SendToConversation continues an existing channel conversation and returns
// the final agent answer to the local console. The external channel is not
// notified; its runtime and native session are reused so context stays intact.
func (e *Engine) SendToConversation(ctx context.Context, channelID, conversationID, text string) (string, error) {
	channelID = strings.TrimSpace(channelID)
	conversationID = strings.TrimSpace(conversationID)
	text = strings.TrimSpace(text)
	if channelID == "" || conversationID == "" {
		return "", errors.New("channel and conversation are required")
	}
	if text == "" {
		return "", errors.New("message text is required")
	}
	if e.conversations == nil {
		return "", errors.New("conversation store is unavailable")
	}
	rt := e.channelRuntime(channelID)
	if rt == nil {
		return "", fmt.Errorf("channel %q is not running", channelID)
	}
	conversations, err := e.conversations.ListConversations(ctx, "channel:"+channelID, false)
	if err != nil {
		return "", err
	}
	var conversation *Conversation
	for i := range conversations {
		if conversations[i].ID == conversationID {
			conversation = &conversations[i]
			break
		}
	}
	if conversation == nil {
		return "", fmt.Errorf("conversation %q was not found in channel %q", conversationID, channelID)
	}
	turnCtx, cancelTurn := context.WithCancel(ctx)
	turn, started := rt.beginDirectTurn(conversation.ConversationKey, "agentmux-console", cancelTurn)
	if !started {
		cancelTurn()
		return "", fmt.Errorf("conversation %q is already running", conversationID)
	}
	defer cancelTurn()
	defer rt.finishDirectTurn(conversation.ConversationKey, turn)

	msg := &Message{
		ID:              fmt.Sprintf("console-%d", time.Now().UTC().UnixNano()),
		ChatID:          conversation.ChatID,
		ChatType:        conversation.ChatType,
		ConversationKey: conversation.ConversationKey,
		UserID:          "agentmux-console",
		UserName:        "AgentMux Console",
		Text:            text,
		Timestamp:       time.Now().UTC(),
		Platform:        rt.channel.Type,
		ChannelID:       channelID,
		Origin:          "console",
	}
	data := eventData(msg)
	if e.msgLog != nil {
		if logErr := e.msgLog.Log(channelID, data); logErr != nil {
			e.log.Warn("write console conversation message log", "channel_id", channelID, "err", logErr)
		}
	}
	e.emit(ctx, HookMessageReceived, data)

	sess, conv, created, err := rt.session(turnCtx, msg)
	if err != nil {
		e.emit(ctx, HookError, withError(data, err))
		return "", err
	}
	data["agent_id"] = rt.workspace.AgentID
	data["runtime_id"] = rt.workspace.RuntimeID
	if rt.agent != nil {
		data["agent_name"] = rt.agent.Name()
	}
	data["session_id"] = sessionObservationID(sess)
	if conv != nil {
		data["conversation_id"] = conv.ID
	}
	if created {
		e.emit(ctx, HookSessionStarted, data)
	}
	answer, turnErr := e.streamTurn(turnCtx, sess, text, nil, data)
	if turnErr == nil && turnCtx.Err() != nil {
		turnErr = turnCtx.Err()
	}
	e.persistConversationTurn(ctx, conv, sess)
	e.emit(ctx, HookMessageSent, data)
	if turnErr != nil {
		return answer, turnErr
	}
	return answer, nil
}
