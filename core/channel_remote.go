package core

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type channelControlState struct {
	active *runtimeChannelTask
	queue  []*runtimeChannelTask
}

type runtimeChannelTask struct {
	task          ChannelTask
	msg           *Message
	session       AgentSession
	conv          *Conversation
	stopRequested bool
	runErr        string
	cancel        context.CancelFunc
}

// handleChannelMessage gates the opt-in Codex remote-control path and leaves
// every classic channel/runtime on the existing direct path.
func (e *Engine) handleChannelMessage(ctx context.Context, msg *Message, data map[string]string) {
	rt := e.channelRuntime(msg.ChannelID)
	if rt == nil || !rt.remoteControlEnabled() {
		e.handleChannelMessageDirect(ctx, msg, data)
		return
	}
	if !rt.authorized(msg.UserID) {
		_ = rt.platform.Reply(ctx, msg, "你不在此渠道的 Codex 远程控制白名单中。")
		e.emit(ctx, HookMessageSent, data)
		return
	}
	if e.handleChannelHelpCommand(ctx, rt, msg) {
		e.emit(ctx, HookMessageSent, data)
		return
	}
	if e.handleChannelCLIAuth(ctx, rt, msg, data) {
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
		attemptedSteer := active.task.ControllerID == msg.UserID || rt.isAdmin(msg.UserID)
		if attemptedSteer {
			if session, ok := active.session.(InteractiveAgentSession); ok {
				steerCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
				err := session.Steer(steerCtx, channelMessageForAgent(rt.channel, msg).Text)
				cancel()
				if err == nil {
					rt.controlMu.Lock()
					active.task.TurnID = session.ActiveTurnID()
					taskSnapshot := active.task
					rt.controlMu.Unlock()
					_ = e.updateRemoteTask(context.Background(), taskSnapshot)
					_ = rt.platform.Reply(ctx, msg, "已追加到当前 Codex 任务。")
					e.emit(ctx, HookTaskSteered, withTaskData(data, active.task, "steered"))
					e.emit(ctx, HookMessageSent, withTaskData(data, active.task, "steered"))
					return
				}
			}
		}
		if _, err := e.enqueueRemoteTask(ctx, rt, msg, msg.Text); err != nil {
			_ = rt.platform.Reply(ctx, msg, "排队失败："+err.Error())
		} else if attemptedSteer {
			_ = rt.platform.Reply(ctx, msg, "当前 Codex turn 无法追加，消息已自动加入队列。")
		} else {
			_ = rt.platform.Reply(ctx, msg, "当前任务由其他控制人执行，消息已加入队列。")
		}
		e.emit(ctx, HookMessageSent, data)
		return
	}

	task := e.newRemoteTask(rt, msg, msg.Text, ChannelTaskRunning)
	rt.controlMu.Lock()
	state = rt.controlStateLocked(conversationKey)
	if state.active != nil {
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
	rt.controlMu.Unlock()
	if e.channelControl != nil {
		if err := e.channelControl.CreateChannelTask(ctx, task.task); err != nil {
			rt.controlMu.Lock()
			if state.active == task {
				state.active = nil
			}
			rt.controlMu.Unlock()
			_ = rt.platform.Reply(ctx, msg, "创建任务失败："+err.Error())
			return
		}
	}
	e.runRemoteTask(ctx, rt, task, data)
}

func (rt *channelRuntime) remoteControlEnabled() bool {
	if rt == nil || !CodexRemoteControlEnabled(rt.channel) {
		return false
	}
	if rt.workspace.RuntimeID == "codex" {
		return true
	}
	return rt.agent != nil && rt.agent.Name() == "codex"
}

func (rt *channelRuntime) authorized(userID string) bool {
	if strings.TrimSpace(userID) == "" {
		return false
	}
	return ChannelAllowedUsers(rt.channel)[userID] || ChannelAdminUsers(rt.channel)[userID]
}

func (rt *channelRuntime) isAdmin(userID string) bool {
	return ChannelAdminUsers(rt.channel)[userID]
}

func (rt *channelRuntime) controlStateLocked(key string) *channelControlState {
	state := rt.controlTasks[key]
	if state == nil {
		state = &channelControlState{}
		rt.controlTasks[key] = state
	}
	return state
}

func (rt *channelRuntime) attachRemoteSession(key string, session AgentSession, conv *Conversation) {
	if !rt.remoteControlEnabled() {
		return
	}
	rt.controlMu.Lock()
	state := rt.controlStateLocked(key)
	if state.active != nil {
		state.active.session = session
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
		state.active.task.UpdatedAt = time.Now().UTC()
		if rt.owner.channelControl != nil {
			_ = rt.owner.channelControl.UpdateChannelTask(context.Background(), state.active.task)
		}
	}
	rt.controlMu.Unlock()
}

func (e *Engine) newRemoteTask(rt *channelRuntime, msg *Message, prompt string, status ChannelTaskStatus) *runtimeChannelTask {
	now := time.Now().UTC()
	task := ChannelTask{
		ID:              NewChannelControlID("task"),
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
	clone.Images = nil
	clone.RuntimeSettingsAction = nil
	clone.AgentInteractionAction = nil
	clone.ChannelTaskAction = nil
	clone.Callback = nil
	clone.LogOnly = false
	return &clone
}

func (e *Engine) enqueueRemoteTask(ctx context.Context, rt *channelRuntime, msg *Message, prompt string) (*runtimeChannelTask, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
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
	state.queue = append(state.queue, task)
	rt.controlMu.Unlock()
	if e.channelControl != nil {
		if err := e.channelControl.CreateChannelTask(ctx, task.task); err != nil {
			rt.controlMu.Lock()
			for i, queued := range state.queue {
				if queued == task {
					state.queue = append(state.queue[:i], state.queue[i+1:]...)
					break
				}
			}
			rt.controlMu.Unlock()
			return nil, err
		}
	}
	e.emit(ctx, HookTaskQueued, withTaskData(eventData(msg), task.task, "queued"))
	rt.controlMu.Lock()
	state = rt.controlStateLocked(key)
	start := state.active == nil && len(state.queue) > 0 && state.queue[0] == task && rt.runCtx != nil && rt.runCtx.Err() == nil
	if start {
		state.queue = state.queue[1:]
		task.task.Status = ChannelTaskRunning
		task.task.StartedAt = time.Now().UTC()
		task.task.UpdatedAt = task.task.StartedAt
		task.task.Prompt = ""
		state.active = task
	}
	rt.controlMu.Unlock()
	if start {
		if err := e.updateRemoteTask(context.Background(), task.task); err != nil {
			rt.controlMu.Lock()
			state = rt.controlStateLocked(key)
			if state.active == task {
				state.active = nil
				task.task.Status = ChannelTaskQueued
				task.task.StartedAt = time.Time{}
				task.task.Prompt = task.msg.Text
				state.queue = append([]*runtimeChannelTask{task}, state.queue...)
			}
			rt.controlMu.Unlock()
			e.log.Error("claim queued channel task", "task", task.task.ID, "err", err)
		} else {
			go e.runRemoteTask(rt.runCtx, rt, task, eventData(task.msg))
		}
	}
	return task, nil
}

func (e *Engine) runRemoteTask(parent context.Context, rt *channelRuntime, task *runtimeChannelTask, data map[string]string) {
	lock := e.remoteWorkLock(rt.workDir)
	lock.Lock()
	defer lock.Unlock()

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

	taskData := withTaskData(data, task.task, "started")
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
	if state.active == nil || state.active.task.ID != data["task_id"] {
		return
	}
	state.active.runErr = strings.TrimSpace(data["error"])
	if state.active.runErr == "" {
		state.active.runErr = "Codex task failed"
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
	if state.active == nil || state.active.task.ID != data["task_id"] || state.active.task.TurnID == event.TurnID {
		rt.controlMu.Unlock()
		return
	}
	state.active.task.TurnID = event.TurnID
	state.active.task.UpdatedAt = time.Now().UTC()
	task := state.active.task
	data["native_turn_id"] = event.TurnID
	rt.controlMu.Unlock()
	_ = e.updateRemoteTask(context.Background(), task)
}

func (e *Engine) finishRemoteTask(rt *channelRuntime, task *runtimeChannelTask, status ChannelTaskStatus, errText string) {
	now := time.Now().UTC()
	rt.controlMu.Lock()
	state := rt.controlStateLocked(task.task.ConversationKey)
	task.task.Status = status
	task.task.Error = errText
	task.task.FinishedAt = now
	task.task.UpdatedAt = now
	task.task.Prompt = ""
	if state.active == task {
		state.active = nil
	}
	var next *runtimeChannelTask
	if len(state.queue) > 0 && rt.runCtx != nil && rt.runCtx.Err() == nil {
		next = state.queue[0]
		state.queue = state.queue[1:]
		next.task.Status = ChannelTaskRunning
		next.task.StartedAt = now
		next.task.UpdatedAt = now
		next.task.Prompt = ""
		state.active = next
	}
	rt.controlMu.Unlock()
	_ = e.updateRemoteTask(context.Background(), task.task)
	e.emit(context.Background(), HookTaskCompleted, withTaskData(eventData(task.msg), task.task, string(status)))
	if next != nil {
		if err := e.updateRemoteTask(context.Background(), next.task); err != nil {
			rt.controlMu.Lock()
			state := rt.controlStateLocked(next.task.ConversationKey)
			if state.active == next {
				state.active = nil
				next.task.Status = ChannelTaskQueued
				next.task.StartedAt = time.Time{}
				next.task.Prompt = next.msg.Text
				state.queue = append([]*runtimeChannelTask{next}, state.queue...)
			}
			rt.controlMu.Unlock()
			e.log.Error("claim next channel task", "task", next.task.ID, "err", err)
		} else {
			go e.runRemoteTask(rt.runCtx, rt, next, eventData(next.msg))
		}
	}
}

func (e *Engine) updateRemoteTask(ctx context.Context, task ChannelTask) error {
	if e.channelControl == nil {
		return nil
	}
	return e.channelControl.UpdateChannelTask(ctx, task)
}

func (e *Engine) remoteWorkLock(workDir string) *sync.Mutex {
	key := strings.TrimSpace(workDir)
	if key == "" {
		key = "."
	}
	if absolute, err := filepath.Abs(key); err == nil {
		key = filepath.Clean(absolute)
	}
	if resolved, err := filepath.EvalSymlinks(key); err == nil {
		key = resolved
	}
	e.remoteWorkMu.Lock()
	defer e.remoteWorkMu.Unlock()
	lock := e.remoteWorkLocks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		e.remoteWorkLocks[key] = lock
	}
	return lock
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
	if rt == nil || !rt.remoteControlEnabled() || e.channelControl == nil {
		return
	}
	tasks, err := e.channelControl.RecoverChannelTasks(context.Background(), rt.channel.ID)
	if err != nil {
		e.log.Warn("recover channel tasks", "channel", rt.channel.ID, "err", err)
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
	for _, stored := range tasks {
		msg := &Message{
			ID: stored.MessageID, ChatID: stored.ChatID, ChatType: stored.ChatType,
			RootID: stored.RootID, ThreadID: stored.ThreadID,
			UserID: stored.UserID, Platform: rt.channel.Type, ChannelID: rt.channel.ID,
			ConversationKey: stored.ConversationKey, Origin: OriginChannel, Text: stored.Prompt,
		}
		if msg.ID == "" {
			msg.ID = storedRootMessageID(stored.ConversationKey)
		}
		task := &runtimeChannelTask{task: stored, msg: msg}
		rt.controlMu.Lock()
		state := rt.controlStateLocked(stored.ConversationKey)
		state.queue = append(state.queue, task)
		start := state.active == nil && len(state.queue) == 1
		if start {
			state.queue = state.queue[1:]
			state.active = task
			task.task.Status = ChannelTaskRunning
			task.task.StartedAt = time.Now().UTC()
			task.task.Prompt = ""
		}
		rt.controlMu.Unlock()
		if start && rt.runCtx != nil {
			if err := e.updateRemoteTask(context.Background(), task.task); err != nil {
				rt.controlMu.Lock()
				state := rt.controlStateLocked(stored.ConversationKey)
				if state.active == task {
					state.active = nil
					task.task.Status = ChannelTaskQueued
					task.task.StartedAt = time.Time{}
					task.task.Prompt = msg.Text
					state.queue = append([]*runtimeChannelTask{task}, state.queue...)
				}
				rt.controlMu.Unlock()
				e.log.Error("claim recovered channel task", "task", task.task.ID, "err", err)
			} else {
				go e.runRemoteTask(rt.runCtx, rt, task, eventData(msg))
			}
		}
	}
}

func storedRootMessageID(key string) string {
	if strings.HasPrefix(key, "root:") {
		return strings.TrimPrefix(key, "root:")
	}
	return ""
}
