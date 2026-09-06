package core

import (
	"context"
	"math/rand"
	"strings"
	"time"
)

// handleChannelMessage routes an inbound message from an attached channel to
// the bound agent and streams responses back through the channel's platform.
func (e *Engine) handleChannelMessageDirect(ctx context.Context, msg *Message, data map[string]string) {
	rt := e.channelRuntime(msg.ChannelID)
	if rt == nil {
		e.log.Warn("no runtime for channel message", "channel_id", msg.ChannelID)
		return
	}

	if e.handleChannelHelpCommand(ctx, rt, msg) {
		e.emit(ctx, HookMessageSent, data)
		return
	}
	managedDirectTurn := !rt.remoteControlEnabled()
	if managedDirectTurn && e.handleDirectStop(ctx, rt, msg, data) {
		return
	}

	if managedDirectTurn && isConversationCommand(msg.Text) {
		rt.cancelDirectTurnForReset(ctx, ResolveConversationKey(msg))
	}
	if e.handleConversationCommand(ctx, rt, msg) {
		e.emit(ctx, HookMessageSent, data)
		return
	}
	reactionID := ""
	if msg.RuntimeSettingsAction == nil {
		reactionID = e.addChannelAckReaction(ctx, rt, msg)
		defer e.deleteChannelAckReaction(ctx, rt, msg, reactionID)
	}

	sess, conv, created, generation, releaseSession, err := rt.session(ctx, msg)
	if err != nil {
		e.log.Error("start channel session", "channel", rt.channel.Name, "err", err)
		e.emit(ctx, HookError, withError(data, err))
		if replyErr := rt.platform.Reply(ctx, msg, "failed to start agent session: "+err.Error()); replyErr != nil {
			e.log.Error("channel reply", "channel", rt.channel.Name, "err", replyErr)
		}
		return
	}
	defer releaseSession()
	sessionAgent, sessionWorkspace := generation.agent, generation.workspace
	data["agent_id"] = sessionWorkspace.AgentID
	data["runtime_id"] = sessionWorkspace.RuntimeID
	data["memory_scope"] = sessionWorkspace.MemoryScope
	if sessionAgent != nil {
		data["agent_name"] = sessionAgent.Name()
	}
	data["session_id"] = sessionObservationID(sess)
	if conv != nil {
		data["conversation_id"] = conv.ID
	}
	rt.attachRemoteSession(ResolveConversationKey(msg), sess, conv, generation)
	rt.decorateRemoteTaskData(ResolveConversationKey(msg), data)
	if created {
		e.emit(ctx, HookSessionStarted, data)
	}
	defer e.persistConversationTurn(ctx, conv, sess)
	defaults := rt.runtimeDefaults()
	actionApplied := false
	if e.handleRuntimeSettingsAction(ctx, sess, msg, &defaults, sessionWorkspace.AgentID, func(state RuntimeSettingsPickerState) bool {
		actionApplied = state.Notice == ""
		picker, ok := rt.platform.(RuntimeSettingsPickerReplier)
		if !ok {
			return false
		}
		if err := picker.UpdateRuntimeSettingsPicker(ctx, msg, state); err != nil {
			e.log.Error("channel runtime settings picker update", "channel", rt.channel.Name, "err", err)
			return false
		}
		return true
	}, func(text string) {
		if err := rt.platform.Reply(ctx, msg, text); err != nil {
			e.log.Error("channel runtime settings reply", "channel", rt.channel.Name, "err", err)
		}
	}) {
		if msg.RuntimeSettingsAction != nil && msg.RuntimeSettingsAction.Scope == RuntimeSettingsScopeAgent {
			rt.setRuntimeDefaults(defaults)
		}
		e.emit(ctx, HookMessageSent, data)
		if actionApplied {
			if pending := rt.takePendingInitialTurn(msg, *msg.RuntimeSettingsAction); pending != nil {
				e.handleChannelMessage(ctx, pending, eventData(pending))
			}
		}
		return
	}
	settingsCommand, settingsCommandParsed := parseRuntimeSettingsCommand(msg.Text)
	if e.handleRuntimeSettingsCommand(sess, msg.Text, func(text string) {
		if err := rt.platform.Reply(ctx, msg, text); err != nil {
			e.log.Error("channel reply", "channel", rt.channel.Name, "err", err)
		}
	}, func(state RuntimeSettingsPickerState) bool {
		picker, ok := rt.platform.(RuntimeSettingsPickerReplier)
		if !ok {
			return false
		}
		state.AgentDefaultsEditable = sessionWorkspace.AgentID != "" && !strings.HasPrefix(sessionWorkspace.AgentID, "config:") && e.runtimeSettingsDefaults != nil
		state.RuntimeDefaults = defaults
		if err := picker.ReplyRuntimeSettingsPicker(ctx, msg, state); err != nil {
			e.log.Error("channel runtime settings picker reply", "channel", rt.channel.Name, "err", err)
			return false
		}
		return true
	}, func(state ModelPickerState) bool {
		mp, ok := rt.platform.(ModelPickerReplier)
		if !ok {
			return false
		}
		if err := mp.ReplyModelPicker(ctx, msg, state); err != nil {
			e.log.Error("channel model picker reply", "channel", rt.channel.Name, "err", err)
			return false
		}
		return true
	}) {
		e.emit(ctx, HookMessageSent, data)
		if settingsCommandParsed && runtimeSettingsCommandApplied(sess, settingsCommand) {
			if pending := rt.takePendingInitialTurn(msg, RuntimeSettingsAction{
				Scope: RuntimeSettingsScopeConversation, Setting: settingsCommand.Setting,
			}); pending != nil {
				e.handleChannelMessage(ctx, pending, eventData(pending))
			}
		}
		return
	}
	if created && conv != nil && conv.MessageCount == 0 {
		setting, required := rt.initialRuntimeConfigurationSetting(sess)
		if required {
			rt.storePendingInitialTurn(msg, setting)
		}
		if required && rt.promptInitialRuntimeConfiguration(ctx, sess, msg, setting) {
			e.emit(ctx, HookMessageSent, data)
			return
		}
		if required {
			rt.discardPendingInitialTurn(msg)
		}
	}

	if managedDirectTurn {
		turnCtx, cancelTurn := context.WithTimeout(ctx, ChannelTurnTimeout(rt.channel))
		conversationKey := ResolveConversationKey(msg)
		turn, started := rt.beginDirectTurn(turnCtx, conversationKey, msg.UserID, cancelTurn)
		if !started {
			cancelTurn()
			_ = rt.platform.Reply(ctx, msg, "上一条消息仍在处理中。请等待完成，或发送 /stop 终止后再试。")
			e.emit(ctx, HookMessageSent, data)
			return
		}
		directTask := e.newRemoteTask(rt, msg, "", ChannelTaskRunning).task
		directTask.StartedAt = time.Now().UTC()
		directTask.UpdatedAt = directTask.StartedAt
		directTask.DeliveryKey = "turn:" + directTask.ID
		directTask.DeliveryStatus = ChannelDeliveryPending
		if conv != nil {
			directTask.ConversationID = conv.ID
			directTask.NativeThreadID = conv.NativeSessionID
		}
		if native, ok := sess.(NativeSessioned); ok && native.NativeSessionID() != "" {
			directTask.NativeThreadID = native.NativeSessionID()
		}
		if !rt.attachDirectTask(conversationKey, turn, directTask, msg) {
			cancelTurn()
			return
		}
		if e.channelControl != nil {
			if err := e.channelControl.CreateChannelTask(ctx, directTask); err != nil {
				rt.controlMu.Lock()
				turn.task = nil
				turn.runErr = err.Error()
				rt.controlMu.Unlock()
				cancelTurn()
				rt.finishDirectTurn(conversationKey, turn)
				_ = rt.platform.Reply(ctx, msg, "创建任务失败："+err.Error())
				return
			}
		}
		data = withTaskData(data, directTask, "started")
		e.emit(ctx, HookTaskStarted, data)
		defer cancelTurn()
		defer rt.finishDirectTurn(conversationKey, turn)
		ctx = turnCtx
	}

	agentMsg := channelMessageForAgent(rt.channel, msg)
	mode, ok := channelReplyMode(rt.channel)
	if CodexRemoteControlEnabled(rt.channel) && data["task_id"] != "" && isFeishuLikeChannel(rt.channel.Type) {
		// Codex remote-control tasks always use one durable status card in
		// Feishu/Lark. The classic reply_mode remains unchanged for ordinary
		// channels and runtime/model control messages.
		mode, ok = ReplyModeStreamCard, true
	}
	if !ok {
		e.log.Warn("unknown channel reply mode, falling back to stream_message", "channel", rt.channel.Name, "mode", rt.channel.Config[ChannelConfigReplyMode])
	}
	if mode == ReplyModeStreamCard {
		if sr, ok := rt.platform.(StreamReplier); ok {
			e.streamTurnCard(ctx, sr, sess, agentMsg, data)
			e.emit(ctx, HookMessageSent, data)
			return
		}
		e.log.Warn("channel reply mode stream_card not supported, falling back to stream_message", "channel", rt.channel.Name, "type", rt.channel.Type)
	}
	if mr, ok := rt.platform.(StreamMessageReplier); ok {
		e.streamTurnMessage(ctx, mr, sess, agentMsg, data)
		e.emit(ctx, HookMessageSent, data)
		return
	}

	var deliveryErr error
	answer, _ := e.streamTurn(ctx, sess, agentMsg.Text, func(text string) {
		if err := rt.platform.Reply(ctx, msg, text); err != nil {
			deliveryErr = err
			e.log.Error("channel reply", "channel", rt.channel.Name, "err", err)
		}
	}, data)
	if deliveryErr != nil {
		e.recordChannelDeliveryAttempt(data, deliveryErr, true)
	} else if answer != "" {
		e.recordChannelDeliveryAttempt(data, nil, false)
	}
	e.emit(ctx, HookMessageSent, data)
}

func (rt *channelRuntime) initialRuntimeConfigurationSetting(sess AgentSession) (RuntimeSetting, bool) {
	settings, ok := RuntimeSettingsForSession(sess)
	if !ok {
		return "", false
	}
	caps := settings.RuntimeSettingsCapabilities()
	if strings.TrimSpace(settings.CurrentRuntimeSettings().Model) == "" && len(caps.Models) > 1 {
		return RuntimeSettingModel, true
	}
	// Approval mode is owned by the bound Agent. Only ask when the Agent has no
	// usable default; legacy channel-level approval_mode values are ignored.
	agentDefault := strings.TrimSpace(rt.runtimeDefaults().ApprovalMode)
	if len(caps.ApprovalModes) > 1 && (agentDefault == "" || !runtimeOptionContains(caps.ApprovalModes, agentDefault)) {
		return RuntimeSettingApprovalMode, true
	}
	return "", false
}

func (rt *channelRuntime) promptInitialRuntimeConfiguration(ctx context.Context, sess AgentSession, msg *Message, setting RuntimeSetting) bool {
	settings, ok := RuntimeSettingsForSession(sess)
	if !ok {
		return false
	}
	state := runtimeSettingsPickerState(settings, RuntimeSettingsScopeConversation, RuntimeSettings{}, false)
	command := "/approval <模式>"
	if setting == RuntimeSettingModel {
		state.Notice = "这是新工作目录的首次对话。请先选择模型；选择后将自动继续刚才的消息。"
		command = "/model <模型>"
	} else {
		state.Notice = "这是新工作目录的首次对话。请先确认审批模式；选择后将自动继续刚才的消息。"
	}
	if picker, ok := rt.platform.(RuntimeSettingsPickerReplier); ok {
		if err := picker.ReplyRuntimeSettingsPicker(ctx, msg, state); err == nil {
			return true
		} else {
			rt.owner.log.Warn("reply first-workspace runtime settings picker", "channel", rt.channel.Name, "setting", setting, "err", err)
		}
	}
	values := runtimeOptionValues(settings.RuntimeSettingsCapabilities().Options(setting))
	text := state.Notice + "\n可选值：" + strings.Join(values, ", ") + "\n发送 " + command + " 完成配置。"
	if err := rt.platform.Reply(ctx, msg, text); err != nil {
		rt.owner.log.Error("channel runtime settings configuration reply", "channel", rt.channel.Name, "setting", setting, "err", err)
	}
	return true
}

func (rt *channelRuntime) storePendingInitialTurn(msg *Message, requiredSetting RuntimeSetting) {
	if rt == nil || msg == nil {
		return
	}
	key := ResolveConversationKey(msg)
	if key == "" {
		return
	}
	clone := *msg
	if len(msg.Images) > 0 {
		clone.Images = make([][]byte, len(msg.Images))
		for i, image := range msg.Images {
			clone.Images[i] = append([]byte(nil), image...)
		}
	}
	clone.RuntimeSettingsAction = nil
	clone.AgentInteractionAction = nil
	clone.InteractionMessageID = ""
	clone.Callback = nil
	clone.LogOnly = false

	rt.mu.Lock()
	if rt.pendingTurns == nil {
		rt.pendingTurns = map[string]pendingInitialTurn{}
	}
	rt.pendingTurns[key] = pendingInitialTurn{message: &clone, requiredSetting: requiredSetting}
	rt.mu.Unlock()
}

func (rt *channelRuntime) discardPendingInitialTurn(msg *Message) {
	if rt == nil || msg == nil {
		return
	}
	key := ResolveConversationKey(msg)
	rt.mu.Lock()
	pending, ok := rt.pendingTurns[key]
	if ok && pending.message != nil && pending.message.ID == msg.ID {
		delete(rt.pendingTurns, key)
	}
	rt.mu.Unlock()
}

func (rt *channelRuntime) takePendingInitialTurn(msg *Message, action RuntimeSettingsAction) *Message {
	if rt == nil || msg == nil || action.Setting == RuntimeSettingScope {
		return nil
	}
	if action.Scope != "" && action.Scope != RuntimeSettingsScopeConversation {
		return nil
	}
	key := ResolveConversationKey(msg)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	pending, ok := rt.pendingTurns[key]
	if !ok || pending.requiredSetting != action.Setting {
		return nil
	}
	delete(rt.pendingTurns, key)
	return pending.message
}

func runtimeSettingsCommandApplied(sess AgentSession, command runtimeSettingsCommand) bool {
	if command.List {
		return false
	}
	settings, ok := RuntimeSettingsForSession(sess)
	if !ok || !settings.RuntimeSettingsCapabilities().Supports(command.Setting) {
		return false
	}
	current := settings.CurrentRuntimeSettings().Value(command.Setting)
	if command.Reset {
		return current == settings.DefaultRuntimeSettings().Value(command.Setting)
	}
	return current == command.Value
}

func (e *Engine) addChannelAckReaction(ctx context.Context, rt *channelRuntime, msg *Message) string {
	if rt == nil || msg == nil || msg.ID == "" || !channelAckReactionEnabled(rt.channel) {
		return ""
	}
	reacter, ok := rt.platform.(MessageReactioner)
	if !ok {
		return ""
	}
	emoji := chooseAckReactionEmoji(rt.channel)
	if emoji == "" {
		return ""
	}
	reactionID, err := reacter.AddReaction(ctx, msg, emoji)
	if err != nil {
		e.log.Warn("add channel ack reaction", "channel", rt.channel.Name, "message_id", msg.ID, "emoji", emoji, "err", err)
		return ""
	}
	return reactionID
}

func (e *Engine) deleteChannelAckReaction(ctx context.Context, rt *channelRuntime, msg *Message, reactionID string) {
	if reactionID == "" || rt == nil || msg == nil {
		return
	}
	reacter, ok := rt.platform.(MessageReactioner)
	if !ok {
		return
	}
	if err := reacter.DeleteReaction(ctx, msg, reactionID); err != nil {
		e.log.Warn("delete channel ack reaction", "channel", rt.channel.Name, "message_id", msg.ID, "reaction_id", reactionID, "err", err)
	}
}

// handleConversationCommand intercepts control commands like /new and /clear
// that end the active conversation for a chat (soft delete) so the next
// message starts fresh. It reports whether the message was a command and was
// handled (and thus should not be forwarded to the agent).
func (e *Engine) handleConversationCommand(ctx context.Context, rt *channelRuntime, msg *Message) bool {
	if !isConversationCommand(msg.Text) {
		return false
	}
	_, _, workspace := rt.agentSnapshot()
	e.resetConversation(ctx, rt.scope(), msg.ChatID, msg.ChatType, ResolveConversationKey(msg), workspace.AgentID, rt.dropSession)
	if replyErr := rt.platform.Reply(ctx, msg, conversationResetReply); replyErr != nil {
		e.log.Error("channel reply", "channel", rt.channel.Name, "err", replyErr)
	}
	return true
}

func isFeishuLikeChannel(typ string) bool {
	return typ == "feishu" || typ == "lark"
}

func channelReplyScope(ch Channel) string {
	switch strings.TrimSpace(ch.Config[ChannelConfigReplyScope]) {
	case ReplyScopeAll:
		return ReplyScopeAll
	case ReplyScopeMentionsOnly:
		return ReplyScopeMentionsOnly
	default:
		return ReplyScopeDMAndMentions
	}
}

func channelReplyMode(ch Channel) (string, bool) {
	switch strings.TrimSpace(ch.Config[ChannelConfigReplyMode]) {
	case "", ReplyModeStreamMessage:
		return ReplyModeStreamMessage, true
	case ReplyModeStreamCard:
		return ReplyModeStreamCard, true
	default:
		return ReplyModeStreamMessage, false
	}
}

func channelAckReactionEnabled(ch Channel) bool {
	if !isFeishuLikeChannel(ch.Type) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(ch.Config[ChannelConfigAckReaction])) {
	case "", "true", "1", "yes", "on":
		return true
	default:
		return false
	}
}

func chooseAckReactionEmoji(ch Channel) string {
	raw := strings.TrimSpace(ch.Config[ChannelConfigAckReactionEmojis])
	if raw == "" {
		raw = DefaultAckReactionEmojis
	}
	parts := strings.Split(raw, ",")
	emojis := make([]string, 0, len(parts))
	for _, part := range parts {
		if emoji := strings.TrimSpace(part); emoji != "" {
			emojis = append(emojis, emoji)
		}
	}
	if len(emojis) == 0 {
		return ""
	}
	return emojis[rand.Intn(len(emojis))]
}
