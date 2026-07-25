package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Sender lets external callers (the bridge HTTP API) push a message into a
// project as if it came from a platform.
type Sender interface {
	// SendToProject delivers text to all platforms bound to project.
	SendToProject(ctx context.Context, project, text string) error
}

// ConversationSender is the richer console-chat capability implemented by
// Engine. It lets the management UI continue an AgentMux-managed channel
// conversation without publishing the console operator's message back to the
// external channel.
type ConversationSender interface {
	SendToConversation(ctx context.Context, channelID, conversationID, text string) (string, error)
}

// SendToProject implements Sender on the Engine: it sends an unsolicited
// message to every platform of the named project.
func (e *Engine) SendToProject(ctx context.Context, project, text string) error {
	e.mu.RLock()
	pr := e.projects[project]
	e.mu.RUnlock()
	if pr == nil {
		return ErrNoProject
	}
	for _, p := range pr.platforms {
		// Broadcasting needs a chat id; bridge callers typically target a
		// known chat, so we surface the first configured chat via Send with an
		// empty id which adapters may reject. This is a thin hook; richer
		// targeting is added with per-platform default chats.
		if err := p.Send(ctx, "", text); err != nil {
			e.log.Warn("bridge send", "platform", p.Name(), "err", err)
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

	sess, conv, created, err := rt.session(ctx, msg)
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
	answer, turnErr := e.streamTurn(ctx, sess, text, nil, data)
	e.persistConversationTurn(ctx, conv, sess)
	e.emit(ctx, HookMessageSent, data)
	if turnErr != nil {
		return answer, turnErr
	}
	return answer, nil
}

// ErrNoProject is returned when a project name is unknown.
var ErrNoProject = errProject("unknown project")

type errProject string

func (e errProject) Error() string { return string(e) }
