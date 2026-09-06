package core

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (e *Engine) dispatchAgentInteraction(ctx context.Context, event *Event, data map[string]string) bool {
	if event == nil || event.Interaction == nil {
		return false
	}
	channelID := data["channel_id"]
	conversationKey := data["conversation_key"]
	rt := e.channelRuntime(channelID)
	if rt == nil || !rt.remoteControlEnabled() || e.channelControl == nil {
		return false
	}
	rt.controlMu.Lock()
	state := rt.controlStateLocked(conversationKey)
	active := state.active
	if active == nil {
		rt.controlMu.Unlock()
		return false
	}
	now := time.Now().UTC()
	expiresAt := now.Add(10 * time.Minute)
	if event.Interaction.AutoResolutionMs > 0 {
		expiresAt = now.Add(time.Duration(event.Interaction.AutoResolutionMs) * time.Millisecond)
	}
	record := ChannelInteraction{
		ID:              event.Interaction.ID,
		TaskID:          active.task.ID,
		ChannelID:       rt.channel.ID,
		ConversationID:  active.task.ConversationID,
		ConversationKey: conversationKey,
		ControllerID:    active.task.ControllerID,
		Nonce:           NewChannelControlID("nonce"),
		Status:          ChannelInteractionPending,
		Request:         *event.Interaction,
		CreatedAt:       now,
		ExpiresAt:       expiresAt,
	}
	active.task.Status = ChannelTaskWaitingInput
	active.task.TurnID = event.Interaction.TurnID
	task := active.task
	msg := cloneChannelMessage(active.msg)
	rt.controlMu.Unlock()

	if err := e.channelControl.CreateChannelInteraction(ctx, record); err != nil {
		e.log.Error("persist channel interaction", "channel", channelID, "interaction", record.ID, "err", err)
		return false
	}
	_ = e.updateRemoteTask(context.Background(), task)
	e.emit(ctx, HookPermission, map[string]string{
		"channel_id": channelID, "conversation_id": task.ConversationID,
		"conversation_key": conversationKey, "task_id": task.ID,
		"thread_id": event.Interaction.ThreadID, "turn_id": event.Interaction.TurnID,
		"interaction_id": record.ID, "controller_id": task.ControllerID,
	})
	if replier, ok := rt.platform.(AgentInteractionReplier); ok {
		messageID, err := replier.ReplyAgentInteraction(ctx, msg, task, record)
		if err != nil {
			e.log.Warn("render channel interaction", "channel", channelID, "interaction", record.ID, "err", err)
			_ = rt.platform.Reply(ctx, msg, formatInteractionFallback(record))
		} else if messageID != "" {
			record.MessageID = messageID
			if err := e.channelControl.UpdateChannelInteractionMessage(context.Background(), record.ID, messageID); err != nil {
				e.log.Warn("persist channel interaction message", "channel", channelID, "interaction", record.ID, "err", err)
			}
		}
	} else {
		_ = rt.platform.Reply(ctx, msg, formatInteractionFallback(record))
	}
	if rt.runCtx != nil {
		go e.expireRemoteInteraction(rt.runCtx, rt, record)
	}
	return true
}

func (e *Engine) declineAgentInteraction(ctx context.Context, sess AgentSession, event *Event) {
	interactive, ok := sess.(InteractiveAgentSession)
	if !ok || event == nil || event.Interaction == nil {
		return
	}
	response := AgentInteractionResponse{Decision: "decline", Answers: map[string][]string{}}
	if err := interactive.ResolveInteraction(ctx, event.Interaction.ID, response); err != nil {
		e.log.Warn("decline unsupported agent interaction", "interaction", event.Interaction.ID, "err", err)
	}
}

func formatInteractionFallback(interaction ChannelInteraction) string {
	request := interaction.Request
	if request.Kind == AgentInteractionUserInput {
		for _, question := range request.Questions {
			if question.Secret {
				return "Agent 需要敏感输入。请在本机 AgentMux 控制台处理；该内容不会显示或接收于渠道。"
			}
		}
		return "Agent 正等待补充信息，请在支持交互卡片的客户端或本机控制台处理。"
	}
	detail := request.Command
	if detail == "" {
		detail = request.Reason
	}
	if detail != "" {
		detail = "\n" + detail
	}
	return request.Title + detail + "\n请在交互卡片或本机控制台中审批。"
}

func (e *Engine) expireRemoteInteraction(ctx context.Context, rt *channelRuntime, interaction ChannelInteraction) {
	wait := time.Until(interaction.ExpiresAt)
	if wait < 0 {
		wait = 0
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	current, err := e.channelControl.GetChannelInteraction(context.Background(), interaction.ID)
	if err != nil || current == nil || current.Status != ChannelInteractionPending {
		return
	}
	rt.controlMu.Lock()
	state := rt.controlStateLocked(interaction.ConversationKey)
	active := state.active
	rt.controlMu.Unlock()
	if active == nil || active.task.ID != interaction.TaskID {
		return
	}
	resolved, err := e.channelControl.ResolveChannelInteraction(context.Background(), interaction.ID, interaction.Nonce, "system", ChannelInteractionExpired)
	if err != nil || !resolved {
		return
	}
	if session, ok := active.session.(InteractiveAgentSession); ok {
		_ = session.ResolveInteraction(context.Background(), interaction.ID, AgentInteractionResponse{
			Decision: "decline", Answers: map[string][]string{},
		})
	}
	interaction.Status = ChannelInteractionExpired
	interaction.ResolvedBy = "system"
	interaction.ResolvedAt = time.Now().UTC()
	if updater, ok := rt.platform.(AgentInteractionUpdateReplier); ok && current.MessageID != "" {
		msg := cloneChannelMessage(active.msg)
		msg.InteractionMessageID = current.MessageID
		if err := updater.UpdateAgentInteraction(context.Background(), msg, interaction, "expired"); err != nil {
			e.log.Warn("expire channel interaction card", "channel", interaction.ChannelID, "interaction", interaction.ID, "err", err)
		}
	}
}

func (e *Engine) resolveRemoteInteraction(ctx context.Context, rt *channelRuntime, msg *Message, action AgentInteractionAction, local bool) error {
	if e.channelControl == nil {
		return fmt.Errorf("channel interaction store is unavailable")
	}
	record, err := e.channelControl.GetChannelInteraction(ctx, action.InteractionID)
	if err != nil {
		return err
	}
	if record == nil || record.ChannelID != rt.channel.ID || record.ConversationKey != ResolveConversationKey(msg) {
		return fmt.Errorf("interaction does not belong to this channel conversation")
	}
	if record.Status != ChannelInteractionPending {
		return fmt.Errorf("interaction is no longer pending")
	}
	if action.Nonce == "" || action.Nonce != record.Nonce {
		return fmt.Errorf("interaction nonce is invalid")
	}
	if !local && record.MessageID != "" && msg.InteractionMessageID != record.MessageID {
		return fmt.Errorf("interaction callback does not match the original card")
	}
	if !local && msg.UserID != record.ControllerID && !rt.isChatManager(ctx, msg) {
		return fmt.Errorf("only the task controller or an administrator may respond")
	}
	for _, question := range record.Request.Questions {
		if question.Secret && !local && len(action.Answers[question.ID]) > 0 {
			return fmt.Errorf("secret input is only accepted by the local console")
		}
	}
	if record.Request.HighRisk && action.Decision == "acceptForSession" {
		return fmt.Errorf("high-risk actions must be approved once")
	}

	rt.controlMu.Lock()
	state := rt.controlStateLocked(record.ConversationKey)
	active := state.active
	rt.controlMu.Unlock()
	if active == nil || active.task.ID != record.TaskID {
		return fmt.Errorf("owning task is no longer active")
	}
	session, ok := active.session.(InteractiveAgentSession)
	if !ok {
		return fmt.Errorf("current runtime cannot resolve native interactions")
	}
	status := ChannelInteractionResolved
	if action.Decision == "decline" || action.Decision == "cancel" {
		status = ChannelInteractionDeclined
	}
	actor := msg.UserID
	if local && actor == "" {
		actor = "local-console"
	}
	claimed, err := e.channelControl.ResolveChannelInteraction(ctx, record.ID, record.Nonce, actor, status)
	if err != nil {
		return err
	}
	if !claimed {
		return fmt.Errorf("interaction was already handled")
	}
	if err := session.ResolveInteraction(ctx, record.ID, AgentInteractionResponse{
		Decision: action.Decision, Answers: action.Answers,
	}); err != nil {
		return err
	}
	rt.controlMu.Lock()
	if state.active == active {
		active.task.Status = ChannelTaskRunning
		active.task.UpdatedAt = time.Now().UTC()
		_ = e.updateRemoteTask(context.Background(), active.task)
	}
	rt.controlMu.Unlock()
	e.emit(ctx, HookInteractionResolved, map[string]string{
		"channel_id": rt.channel.ID, "conversation_id": record.ConversationID,
		"conversation_key": record.ConversationKey, "task_id": record.TaskID,
		"interaction_id": record.ID, "controller_id": record.ControllerID,
		"resolved_by": actor, "decision": action.Decision,
	})
	outcome := action.Decision
	if outcome == "" {
		outcome = "answered"
	}
	record.Status = status
	record.ResolvedBy = actor
	record.ResolvedAt = time.Now().UTC()
	if updater, ok := rt.platform.(AgentInteractionUpdateReplier); ok && msg.InteractionMessageID != "" {
		if err := updater.UpdateAgentInteraction(ctx, msg, *record, outcome); err != nil {
			_ = rt.platform.Reply(ctx, msg, "Agent 交互已处理。")
		}
	} else {
		_ = rt.platform.Reply(ctx, msg, "Agent 交互已处理。")
	}
	return nil
}

// BindChannelConversation swaps the native thread bound to an idle channel
// conversation. The existing Codex thread is never modified or deleted.
func (e *Engine) BindChannelConversation(ctx context.Context, channelID, conversationID, threadID string) error {
	rt := e.channelRuntime(channelID)
	if rt == nil {
		return fmt.Errorf("channel %q is not attached", channelID)
	}
	if !rt.remoteControlEnabled() {
		return fmt.Errorf("Codex remote control is not enabled for channel %q", channelID)
	}
	if e.conversations == nil {
		return fmt.Errorf("conversation store is unavailable")
	}
	conversations, err := e.conversations.ListConversations(ctx, rt.scope(), false)
	if err != nil {
		return err
	}
	var conversation *Conversation
	for i := range conversations {
		if conversations[i].ID == conversationID {
			conversation = &conversations[i]
			break
		}
	}
	if conversation == nil {
		return fmt.Errorf("conversation %q was not found in channel %q", conversationID, channelID)
	}
	rt.controlMu.Lock()
	state := rt.controlStateLocked(conversation.ConversationKey)
	busy := state.active != nil || len(state.queue) > 0
	rt.controlMu.Unlock()
	if busy {
		return fmt.Errorf("conversation has an active or queued task")
	}
	rt.dropSession(ctx, conversation.ID)
	if err := e.conversations.UpdateConversationSession(ctx, conversation.ID, strings.TrimSpace(threadID), conversation.WorkDir); err != nil {
		return err
	}
	e.emit(ctx, HookThreadBound, map[string]string{
		"channel_id": channelID, "conversation_id": conversation.ID,
		"conversation_key": conversation.ConversationKey, "thread_id": strings.TrimSpace(threadID),
	})
	return nil
}

// ResolveChannelInteractionLocal accepts sensitive answers only from the local
// management API and routes them to the same correlated app-server request.
func (e *Engine) ResolveChannelInteractionLocal(ctx context.Context, action AgentInteractionAction) error {
	if e.channelControl == nil {
		return fmt.Errorf("channel interaction store is unavailable")
	}
	record, err := e.channelControl.GetChannelInteraction(ctx, action.InteractionID)
	if err != nil {
		return err
	}
	if record == nil {
		return fmt.Errorf("interaction %q not found", action.InteractionID)
	}
	rt := e.channelRuntime(record.ChannelID)
	if rt == nil {
		return fmt.Errorf("channel %q is not attached", record.ChannelID)
	}
	chatID := ""
	if e.conversations != nil {
		conversations, _ := e.conversations.ListConversations(ctx, rt.scope(), true)
		for _, conversation := range conversations {
			if conversation.ID == record.ConversationID {
				chatID = conversation.ChatID
				break
			}
		}
	}
	msg := &Message{
		ChatID: chatID, ChannelID: record.ChannelID, ConversationKey: record.ConversationKey,
		UserID: "local-console", Platform: rt.channel.Type, Origin: OriginAPI,
		InteractionMessageID: record.MessageID,
	}
	return e.resolveRemoteInteraction(ctx, rt, msg, action, true)
}
