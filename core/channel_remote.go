package core

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type channelControlState struct {
	active   *runtimeChannelTask
	queue    []*runtimeChannelTask
	steering bool
}

type runtimeChannelTask struct {
	task          ChannelTask
	msg           *Message
	session       AgentSession
	generation    *channelAgentGeneration
	conv          *Conversation
	stopRequested bool
	runErr        string
	cancel        context.CancelFunc
}

// handleChannelMessage admits channel tasks to the per-conversation queue.
// Embedded engines without a control store retain their lightweight direct path.
func (e *Engine) handleChannelMessage(ctx context.Context, msg *Message, data map[string]string) {
	rt := e.channelRuntime(msg.ChannelID)
	if rt != nil && e.handleConversationMode(ctx, rt, msg) {
		return
	}
	if rt != nil && msg.ChannelFeedbackAction != nil {
		e.handleChannelFeedback(ctx, rt, msg, data, *msg.ChannelFeedbackAction)
		return
	}
	if rt != nil && msg.ChannelSessionAction != nil {
		e.handleChannelSessionAction(ctx, rt, msg, data, *msg.ChannelSessionAction)
		return
	}
	if rt != nil && !rt.remoteControlEnabled() && msg.ChannelTaskAction != nil {
		e.handleDirectTaskAction(ctx, rt, msg, data, *msg.ChannelTaskAction)
		return
	}
	if rt != nil && e.handleMeetingMessage(ctx, rt, msg, data) {
		return
	}
	if rt == nil || !rt.remoteControlEnabled() {
		e.handleChannelMessageDirect(ctx, msg, data)
		return
	}
	if !rt.authorized(msg.UserID) {
		_ = rt.platform.Reply(ctx, msg, "你不在此渠道的访问白名单中。")
		e.emit(ctx, HookMessageSent, data)
		return
	}
	if e.handleChannelHelpCommand(ctx, rt, msg) {
		e.emit(ctx, HookMessageSent, data)
		return
	}
	if msg.ChannelTaskAction != nil {
		e.handleRemoteTaskAction(ctx, rt, msg, data, *msg.ChannelTaskAction)
		return
	}
	if msg.AgentInteractionAction != nil {
		if err := e.resolveRemoteInteraction(ctx, rt, msg, *msg.AgentInteractionAction, false); err != nil {
			_ = rt.platform.Reply(ctx, msg, "操作未生效："+err.Error())
		}
		e.emit(ctx, HookMessageSent, data)
		return
	}

	text := strings.TrimSpace(msg.Text)
	if e.handleRemoteCommand(ctx, rt, msg, data, text) {
		return
	}
	// Runtime selection cards and /model are configuration operations, not
	// Codex tasks. They retain their existing implementation.
	if msg.RuntimeSettingsAction != nil {
		e.handleChannelMessageDirect(ctx, msg, data)
		return
	}
	if _, ok := parseRuntimeSettingsCommand(text); ok {
		e.handleChannelMessageDirect(ctx, msg, data)
		return
	}
	conversationKey := ResolveConversationKey(msg)
	rt.controlMu.Lock()
	state := rt.controlStateLocked(conversationKey)
	active := state.active
	rt.controlMu.Unlock()

	if active != nil {
		if _, err := e.enqueueRemoteTask(ctx, rt, msg, msg.Text); err != nil {
			_ = rt.platform.Reply(ctx, msg, "排队失败："+err.Error())
		}
		e.emit(ctx, HookMessageSent, data)
		return
	}

	task := e.newRemoteTask(rt, msg, msg.Text, ChannelTaskRunning)
	rt.controlMu.Lock()
	state = rt.controlStateLocked(conversationKey)
	if state.active != nil || state.steering || len(state.queue) > 0 || rt.directTurns[conversationKey] != nil {
		rt.controlMu.Unlock()
		if _, err := e.enqueueRemoteTask(ctx, rt, msg, task.task.Prompt); err != nil {
			_ = rt.platform.Reply(ctx, msg, "排队失败："+err.Error())
		}
		e.emit(ctx, HookMessageSent, data)
		return
	}
	task.task.Prompt = ""
	task.task.StartedAt = time.Now().UTC()
	state.active = task
	if e.channelControl != nil {
		if err := e.channelControl.CreateChannelTask(ctx, task.task); err != nil {
			state.active = nil
			rt.controlMu.Unlock()
			_ = rt.platform.Reply(ctx, msg, "创建任务失败："+err.Error())
			return
		}
	}
	rt.controlMu.Unlock()
	go e.runRemoteTask(ctx, rt, task, data)
}

func (rt *channelRuntime) remoteControlEnabled() bool {
	return rt != nil && ((rt.owner != nil && rt.owner.channelControl != nil) || CodexRemoteControlEnabled(rt.channel))
}

func (rt *channelRuntime) authorized(userID string) bool {
	allowed, admins := ChannelAllowedUsers(rt.channel), ChannelAdminUsers(rt.channel)
	if len(allowed) == 0 && len(admins) == 0 {
		return !CodexRemoteControlEnabled(rt.channel)
	}
	if strings.TrimSpace(userID) == "" {
		return false
	}
	return allowed[userID] || admins[userID]
}

func (rt *channelRuntime) isAdmin(userID string) bool {
	return ChannelAdminUsers(rt.channel)[userID]
}

func (rt *channelRuntime) controlStateLocked(key string) *channelControlState {
	if rt.controlTasks == nil {
		rt.controlTasks = map[string]*channelControlState{}
	}
	state := rt.controlTasks[key]
	if state == nil {
		state = &channelControlState{}
		rt.controlTasks[key] = state
	}
	return state
}

func (rt *channelRuntime) attachRemoteSession(key string, session AgentSession, conv *Conversation, generation *channelAgentGeneration) {
	if !rt.remoteControlEnabled() {
		return
	}
	rt.controlMu.Lock()
	state := rt.controlStateLocked(key)
	if state.active != nil {
		state.active.session = session
		state.active.generation = generation
		state.active.conv = conv
		if conv != nil {
			state.active.task.ConversationID = conv.ID
			state.active.task.NativeThreadID = conv.NativeSessionID
		}
		if native, ok := session.(NativeSessioned); ok && native.NativeSessionID() != "" {
			state.active.task.NativeThreadID = native.NativeSessionID()
		}
		if interactive, ok := session.(InteractiveAgentSession); ok {
			state.active.task.TurnID = interactive.ActiveTurnID()
		}
		go rt.owner.refreshQueueCards(rt, state.active)
		state.active.task.UpdatedAt = time.Now().UTC()
		if rt.owner.channelControl != nil {
			_ = rt.owner.channelControl.UpdateChannelTask(context.Background(), state.active.task)
		}
	}
	rt.controlMu.Unlock()
}

func (e *Engine) newRemoteTask(rt *channelRuntime, msg *Message, prompt string, status ChannelTaskStatus) *runtimeChannelTask {
	now := time.Now().UTC()
	taskID := NewChannelControlID("task")
	task := ChannelTask{
		ID:              taskID,
		SourceMessageID: msg.SourceMessageID, ChatMode: msg.ChatMode, ReplyInThread: msg.ReplyInThread,
		ChannelID:       rt.channel.ID,
		ConversationKey: ResolveConversationKey(msg),
		ChatID:          msg.ChatID,
		MessageID:       msg.ID,
		ChatType:        msg.ChatType,
		RootID:          msg.RootID,
		ThreadID:        msg.ThreadID,
		UserID:          msg.UserID,
		ControllerID:    msg.UserID,
		Status:          status,
		DeliveryKey:     "turn:" + taskID,
		DeliveryStatus:  ChannelDeliveryPending,
		FeedbackNonce:   NewChannelControlID("feedback"),
		Prompt:          prompt,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	taskMessage := cloneChannelMessage(msg)
	taskMessage.Text = prompt
	return &runtimeChannelTask{task: task, msg: taskMessage}
}

func cloneChannelMessage(msg *Message) *Message {
	if msg == nil {
		return &Message{}
	}
	clone := *msg
	clone.Images = append([][]byte(nil), msg.Images...)
	clone.RuntimeSettingsAction = nil
	clone.AgentInteractionAction = nil
	clone.ChannelTaskAction = nil
	clone.ChannelFeedbackAction = nil
	clone.ChannelSessionAction = nil
	clone.ConversationModeAction = nil
	clone.Callback = nil
	clone.LogOnly = false
	return &clone
}

func (e *Engine) enqueueRemoteTask(ctx context.Context, rt *channelRuntime, msg *Message, prompt string) (*runtimeChannelTask, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("排队内容不能为空")
	}
	key := ResolveConversationKey(msg)
	rt.controlMu.Lock()
	state := rt.controlStateLocked(key)
	if len(state.queue) >= ChannelCodexMaxQueue(rt.channel) {
		rt.controlMu.Unlock()
		return nil, fmt.Errorf("队列已达到上限 %d", ChannelCodexMaxQueue(rt.channel))
	}
	task := e.newRemoteTask(rt, msg, prompt, ChannelTaskQueued)
	task.task.ControlNonce = NewChannelControlID("queue")
	if state.active != nil {
		task.task.TargetTaskID = state.active.task.ID
		task.task.ConversationID = state.active.task.ConversationID
	}
	if e.channelControl != nil {
		if err := e.channelControl.CreateChannelTask(ctx, task.task); err != nil {
			rt.controlMu.Unlock()
			return nil, err
		}
	}
	state.queue = append(state.queue, task)
	next := e.startNextRemoteLocked(rt, state)
	snapshot := task.task
	rt.controlMu.Unlock()
	e.emit(ctx, HookTaskQueued, withTaskData(eventData(msg), snapshot, "queued"))
	e.refreshQueueCards(rt, task)
	if next != nil {
		go e.runRemoteTask(rt.runCtx, rt, next, eventData(next.msg))
	}
	return task, nil
}

func (e *Engine) runRemoteTask(parent context.Context, rt *channelRuntime, task *runtimeChannelTask, data map[string]string) {
	timeout := ChannelCodexTurnTimeout(rt.channel)
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	rt.controlMu.Lock()
	task.cancel = cancel
	stopBeforeStart := task.stopRequested
	rt.controlMu.Unlock()
	defer func() {
		rt.controlMu.Lock()
		task.cancel = nil
		rt.controlMu.Unlock()
	}()
	if stopBeforeStart {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				rt.controlMu.Lock()
				session := task.session
				rt.controlMu.Unlock()
				if interactive, ok := session.(InteractiveAgentSession); ok {
					interruptCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
					_ = interactive.Interrupt(interruptCtx)
					stop()
				}
			}
		case <-done:
		}
	}()

	rt.controlMu.Lock()
	taskData := withTaskData(data, task.task, "started")
	rt.controlMu.Unlock()
	e.emit(ctx, HookTaskStarted, taskData)
	e.handleChannelMessageDirect(ctx, task.msg, taskData)
	close(done)

	status := ChannelTaskSucceeded
	errText := ""
	if ctx.Err() == context.DeadlineExceeded {
		status = ChannelTaskInterrupted
		errText = fmt.Sprintf("task exceeded %s timeout", timeout)
	} else if ctx.Err() != nil && parent.Err() != nil {
		status = ChannelTaskInterrupted
		errText = parent.Err().Error()
	}
	rt.controlMu.Lock()
	stopRequested := task.stopRequested
	rt.controlMu.Unlock()
	if stopRequested {
		status = ChannelTaskInterrupted
		errText = "interrupted by task controller"
	} else {
		rt.controlMu.Lock()
		runErr := task.runErr
		rt.controlMu.Unlock()
		if runErr != "" {
			status = ChannelTaskFailed
			errText = runErr
		}
	}
	e.finishRemoteTask(rt, task, status, errText)
}

func (e *Engine) markRemoteTaskError(data map[string]string) {
	if data == nil || data["channel_id"] == "" || data["task_id"] == "" {
		return
	}
	rt := e.channelRuntime(data["channel_id"])
	if rt == nil {
		return
	}
	rt.controlMu.Lock()
	defer rt.controlMu.Unlock()
	state := rt.controlStateLocked(data["conversation_key"])
	if state.active != nil && state.active.task.ID == data["task_id"] {
		state.active.runErr = strings.TrimSpace(data["error"])
		if state.active.runErr == "" {
			state.active.runErr = "Agent task failed"
		}
		return
	}
	if direct := rt.directTurns[data["conversation_key"]]; direct != nil && direct.task != nil && direct.task.ID == data["task_id"] {
		direct.runErr = strings.TrimSpace(data["error"])
		if direct.runErr == "" {
			direct.runErr = "Agent task failed"
		}
	}
}

func (rt *channelRuntime) decorateRemoteTaskData(key string, data map[string]string) {
	if data == nil {
		return
	}
	rt.controlMu.Lock()
	defer rt.controlMu.Unlock()
	state := rt.controlStateLocked(key)
	if state.active == nil {
		return
	}
	data["task_id"] = state.active.task.ID
	data["controller_id"] = state.active.task.ControllerID
	data["thread_id"] = state.active.task.NativeThreadID
	if state.active.task.TurnID != "" {
		data["native_turn_id"] = state.active.task.TurnID
	}
}

func (e *Engine) updateRemoteTaskFromEvent(data map[string]string, event *Event) {
	if data == nil || event == nil || event.TurnID == "" || data["channel_id"] == "" || data["task_id"] == "" {
		return
	}
	rt := e.channelRuntime(data["channel_id"])
	if rt == nil {
		return
	}
	rt.controlMu.Lock()
	state := rt.controlStateLocked(data["conversation_key"])
	if state.active != nil && state.active.task.ID == data["task_id"] {
		if state.active.task.TurnID == event.TurnID {
			rt.controlMu.Unlock()
			return
		}
		state.active.task.TurnID = event.TurnID
		state.active.task.UpdatedAt = time.Now().UTC()
		task := state.active.task
		data["native_turn_id"] = event.TurnID
		rt.controlMu.Unlock()
		_ = e.updateRemoteTask(context.Background(), task)
		return
	}
	if direct := rt.directTurns[data["conversation_key"]]; direct != nil && direct.task != nil && direct.task.ID == data["task_id"] {
		if direct.task.TurnID == event.TurnID {
			rt.controlMu.Unlock()
			return
		}
		direct.task.TurnID = event.TurnID
		direct.task.UpdatedAt = time.Now().UTC()
		task := *direct.task
		data["native_turn_id"] = event.TurnID
		rt.controlMu.Unlock()
		_ = e.updateRemoteTask(context.Background(), task)
		return
	}
	rt.controlMu.Unlock()
}

func (e *Engine) finishRemoteTask(rt *channelRuntime, task *runtimeChannelTask, status ChannelTaskStatus, errText string) {
	rt.controlMu.Lock()
	state := rt.controlStateLocked(task.task.ConversationKey)
	task.task.Status, task.task.Error = status, errText
	task.task.FinishedAt = time.Now().UTC()
	task.task.UpdatedAt = task.task.FinishedAt
	task.task.Prompt = ""
	// A failed terminal write must not cause this prompt to replay on restart:
	// recovery already marks any persisted running task interrupted.
	_ = e.updateRemoteTask(context.Background(), task.task)
	if state.active == task {
		state.active = nil
	}
	next := e.startNextRemoteLocked(rt, state)
	snapshot := task.task
	rt.controlMu.Unlock()
	e.emit(context.Background(), HookTaskCompleted, withTaskData(eventData(task.msg), snapshot, string(status)))
	e.refreshQueueCards(rt, task)
	if next != nil {
		go e.runRemoteTask(rt.runCtx, rt, next, eventData(next.msg))
	}
}

// Caller holds controlMu. Commit running before launching; never remove an
// uncommitted item from the queue or let a steer-in-flight be overtaken.
func (e *Engine) startNextRemoteLocked(rt *channelRuntime, state *channelControlState) *runtimeChannelTask {
	if state.active != nil || state.steering || len(state.queue) == 0 || rt.runCtx == nil || rt.runCtx.Err() != nil {
		return nil
	}
	next := state.queue[0]
	if rt.directTurns[next.task.ConversationKey] != nil {
		return nil
	}
	previous := next.task
	next.task.Status = ChannelTaskRunning
	next.task.StartedAt = time.Now().UTC()
	next.task.Prompt = ""
	if err := e.updateRemoteTask(context.Background(), next.task); err != nil {
		next.task = previous
		e.log.Error("claim queued task", "err", err)
		return nil
	}
	state.queue = state.queue[1:]
	state.active = next
	return next
}

func (e *Engine) updateRemoteTask(ctx context.Context, task ChannelTask) error {
	if e.channelControl == nil {
		return nil
	}
	return e.channelControl.UpdateChannelTask(ctx, task)
}

// recordChannelDeliveryAttempt persists the final-response delivery state for
// both queued remote-control tasks and ordinary channel turns. finalFailure is
// true only after retry exhaustion; transient failures remain pending.
func (e *Engine) recordChannelDeliveryAttempt(data map[string]string, deliveryErr error, finalFailure bool) {
	if data == nil || data["channel_id"] == "" || data["conversation_key"] == "" || data["task_id"] == "" {
		return
	}
	rt := e.channelRuntime(data["channel_id"])
	if rt == nil {
		return
	}
	rt.controlMu.Lock()
	var task *ChannelTask
	if state := rt.controlStateLocked(data["conversation_key"]); state.active != nil && state.active.task.ID == data["task_id"] {
		task = &state.active.task
		if deliveryErr != nil && finalFailure {
			state.active.runErr = "final response delivery failed: " + deliveryErr.Error()
		}
	} else if direct := rt.directTurns[data["conversation_key"]]; direct != nil && direct.task != nil && direct.task.ID == data["task_id"] {
		task = direct.task
		if deliveryErr != nil && finalFailure {
			direct.runErr = "final response delivery failed: " + deliveryErr.Error()
		}
	}
	if task == nil {
		rt.controlMu.Unlock()
		return
	}
	task.DeliveryAttempts++
	task.UpdatedAt = time.Now().UTC()
	if deliveryErr == nil {
		task.DeliveryStatus = ChannelDeliverySent
		task.DeliveryError = ""
		task.DeliveredAt = task.UpdatedAt
	} else {
		task.DeliveryError = deliveryErr.Error()
		if finalFailure {
			task.DeliveryStatus = ChannelDeliveryFailed
		} else {
			task.DeliveryStatus = ChannelDeliveryPending
		}
	}
	snapshot := *task
	rt.controlMu.Unlock()
	_ = e.updateRemoteTask(context.Background(), snapshot)
}

func (e *Engine) activeChannelTask(data map[string]string) (ChannelTask, bool) {
	if data == nil || data["channel_id"] == "" || data["conversation_key"] == "" || data["task_id"] == "" {
		return ChannelTask{}, false
	}
	rt := e.channelRuntime(data["channel_id"])
	if rt == nil {
		return ChannelTask{}, false
	}
	rt.controlMu.Lock()
	defer rt.controlMu.Unlock()
	if state := rt.controlStateLocked(data["conversation_key"]); state.active != nil && state.active.task.ID == data["task_id"] {
		return state.active.task, true
	}
	if direct := rt.directTurns[data["conversation_key"]]; direct != nil && direct.task != nil && direct.task.ID == data["task_id"] {
		return *direct.task, true
	}
	return ChannelTask{}, false
}

func withTaskData(data map[string]string, task ChannelTask, action string) map[string]string {
	out := map[string]string{}
	for key, value := range data {
		out[key] = value
	}
	out["task_id"] = task.ID
	out["conversation_id"] = task.ConversationID
	out["conversation_key"] = task.ConversationKey
	out["thread_id"] = task.NativeThreadID
	out["turn_id"] = task.TurnID
	out["controller_id"] = task.ControllerID
	out["task_action"] = action
	return out
}

func (e *Engine) recoverRemoteTasks(rt *channelRuntime) {
	if rt == nil || e.channelControl == nil {
		return
	}
	tasks, err := e.channelControl.RecoverChannelTasks(context.Background(), rt.channel.ID)
	if err != nil {
		e.log.Warn("recover channel tasks", "channel", rt.channel.ID, "err", err)
		return
	}
	if !rt.remoteControlEnabled() {
		return
	}
	if updater, ok := rt.platform.(AgentInteractionUpdateReplier); ok {
		interactions, listErr := e.channelControl.ListChannelInteractions(context.Background(), rt.channel.ID, "", false)
		if listErr != nil {
			e.log.Warn("recover channel interactions", "channel", rt.channel.ID, "err", listErr)
		} else {
			for _, interaction := range interactions {
				if interaction.Status != ChannelInteractionExpired || interaction.ResolvedBy != "system-restart" || interaction.MessageID == "" {
					continue
				}
				msg := &Message{
					ChannelID: rt.channel.ID, Platform: rt.channel.Type,
					ConversationKey:      interaction.ConversationKey,
					InteractionMessageID: interaction.MessageID,
				}
				if err := updater.UpdateAgentInteraction(context.Background(), msg, interaction, "expired"); err != nil {
					e.log.Warn("recover channel interaction card", "channel", rt.channel.ID, "interaction", interaction.ID, "err", err)
				}
			}
		}
	}
	// Load the complete FIFO before launching any recovered task.
	rt.controlMu.Lock()
	for _, stored := range tasks {
		msg := &Message{ID: stored.MessageID, ChatID: stored.ChatID, ChatType: stored.ChatType, RootID: stored.RootID, ThreadID: stored.ThreadID, UserID: stored.UserID, Platform: rt.channel.Type, ChannelID: rt.channel.ID, ConversationKey: stored.ConversationKey, Origin: OriginChannel, Text: stored.Prompt, SourceMessageID: stored.SourceMessageID, ChatMode: stored.ChatMode, ReplyInThread: stored.ReplyInThread}
		task := &runtimeChannelTask{task: stored, msg: msg}
		state := rt.controlStateLocked(stored.ConversationKey)
		state.queue = append(state.queue, task)
	}
	var starts []*runtimeChannelTask
	for _, state := range rt.controlTasks {
		if next := e.startNextRemoteLocked(rt, state); next != nil {
			starts = append(starts, next)
		}
	}
	rt.controlMu.Unlock()
	for _, task := range starts {
		e.refreshQueueCards(rt, task)
		go e.runRemoteTask(rt.runCtx, rt, task, eventData(task.msg))
	}

}
