package core

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
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

func (e *Engine) handleRemoteCommand(ctx context.Context, rt *channelRuntime, msg *Message, data map[string]string, text string) bool {
	lower := strings.ToLower(text)
	switch lower {
	case "/status":
		_ = rt.platform.Reply(ctx, msg, rt.remoteStatus(ResolveConversationKey(msg)))
		e.emit(ctx, HookMessageSent, data)
		return true
	case "停止", "/stop":
		e.stopRemoteTask(ctx, rt, msg, data)
		return true
	case "/queue clear":
		e.clearRemoteQueue(ctx, rt, msg, data, false)
		return true
	case "/queue clear confirm":
		e.clearRemoteQueue(ctx, rt, msg, data, true)
		return true
	case "/queue":
		_ = rt.platform.Reply(ctx, msg, "用法：/queue <内容>")
		e.emit(ctx, HookMessageSent, data)
		return true
	case "/sessions":
		e.listRemoteThreads(ctx, rt, msg)
		e.emit(ctx, HookMessageSent, data)
		return true
	case "/open":
		e.openRemoteThread(ctx, rt, msg, data)
		return true
	case "/takeover":
		e.takeOverRemoteTask(ctx, rt, msg, data)
		return true
	case "/bind":
		e.bindRemoteThread(ctx, rt, msg, "")
		e.emit(ctx, HookMessageSent, data)
		return true
	}
	if strings.HasPrefix(text, "排队 ") || strings.HasPrefix(lower, "/queue ") {
		prompt := strings.TrimSpace(strings.TrimPrefix(text, "排队"))
		if strings.HasPrefix(lower, "/queue ") {
			prompt = strings.TrimSpace(text[len("/queue "):])
		}
		if _, err := e.enqueueRemoteTask(ctx, rt, msg, prompt); err != nil {
			_ = rt.platform.Reply(ctx, msg, "排队失败："+err.Error())
		} else {
			_ = rt.platform.Reply(ctx, msg, "已加入 Codex 任务队列。")
		}
		e.emit(ctx, HookMessageSent, data)
		return true
	}
	if strings.HasPrefix(lower, "/bind ") {
		e.bindRemoteThread(ctx, rt, msg, strings.TrimSpace(text[len("/bind "):]))
		e.emit(ctx, HookMessageSent, data)
		return true
	}
	if isConversationCommand(text) {
		key := ResolveConversationKey(msg)
		rt.controlMu.Lock()
		state := rt.controlStateLocked(key)
		busy := state.active != nil || len(state.queue) > 0
		rt.controlMu.Unlock()
		if busy {
			_ = rt.platform.Reply(ctx, msg, "当前仍有活动或排队任务；请先停止任务并清空队列。")
			e.emit(ctx, HookMessageSent, data)
			return true
		}
	}
	return false
}

func (e *Engine) ensureRemoteConversation(ctx context.Context, rt *channelRuntime, msg *Message) (*Conversation, error) {
	opts := rt.workspace
	if opts.WorkDir == "" {
		opts.WorkDir = rt.workDir
	}
	conversation, _, err := e.prepareConversation(ctx, rt.scope(), msg.ChatID, msg.ChatType,
		ResolveConversationKey(msg), opts, rt.workDir)
	return conversation, err
}

func (e *Engine) listRemoteThreads(ctx context.Context, rt *channelRuntime, msg *Message) {
	catalog, ok := rt.agent.(NativeThreadAgent)
	if !ok {
		_ = rt.platform.Reply(ctx, msg, "当前 Codex 运行时不支持 thread 列表。")
		return
	}
	conversation, err := e.ensureRemoteConversation(ctx, rt, msg)
	if err != nil {
		_ = rt.platform.Reply(ctx, msg, "读取会话失败："+err.Error())
		return
	}
	threads, err := catalog.ListNativeThreads(ctx, conversation.WorkDir)
	if err != nil {
		_ = rt.platform.Reply(ctx, msg, "读取 Codex threads 失败："+err.Error())
		return
	}
	rt.controlMu.Lock()
	rt.threadLists[conversation.ConversationKey] = append([]NativeThread(nil), threads...)
	rt.controlMu.Unlock()
	if len(threads) == 0 {
		_ = rt.platform.Reply(ctx, msg, "当前工作目录下没有可绑定的 Codex thread。")
		return
	}
	limit := len(threads)
	if limit > 10 {
		limit = 10
	}
	var lines []string
	for i, thread := range threads[:limit] {
		title := strings.TrimSpace(thread.Title)
		if title == "" {
			title = strings.TrimSpace(thread.Preview)
		}
		if title == "" {
			title = "(untitled)"
		}
		lines = append(lines, fmt.Sprintf("%d. %s\n   %s", i+1, title, thread.ID))
	}
	lines = append(lines, "\n发送 /bind <序号或 thread_id> 绑定。")
	_ = rt.platform.Reply(ctx, msg, strings.Join(lines, "\n"))
}

func (e *Engine) bindRemoteThread(ctx context.Context, rt *channelRuntime, msg *Message, selector string) {
	if selector == "" {
		_ = rt.platform.Reply(ctx, msg, "用法：/bind <序号或 thread_id>")
		return
	}
	key := ResolveConversationKey(msg)
	rt.controlMu.Lock()
	state := rt.controlStateLocked(key)
	if state.active != nil || len(state.queue) > 0 {
		rt.controlMu.Unlock()
		_ = rt.platform.Reply(ctx, msg, "当前仍有活动或排队任务，不能切换 thread。")
		return
	}
	threads := append([]NativeThread(nil), rt.threadLists[key]...)
	rt.controlMu.Unlock()
	if len(threads) == 0 {
		catalog, ok := rt.agent.(NativeThreadAgent)
		if !ok {
			_ = rt.platform.Reply(ctx, msg, "当前 Codex 运行时不支持 thread 绑定。")
			return
		}
		conversation, err := e.ensureRemoteConversation(ctx, rt, msg)
		if err != nil {
			_ = rt.platform.Reply(ctx, msg, "读取会话失败："+err.Error())
			return
		}
		threads, err = catalog.ListNativeThreads(ctx, conversation.WorkDir)
		if err != nil {
			_ = rt.platform.Reply(ctx, msg, "读取 Codex threads 失败："+err.Error())
			return
		}
	}
	threadID := selector
	if index, err := strconv.Atoi(selector); err == nil {
		if index < 1 || index > len(threads) {
			_ = rt.platform.Reply(ctx, msg, "thread 序号超出范围，请先发送 /sessions。")
			return
		}
		threadID = threads[index-1].ID
	}
	found := false
	for _, thread := range threads {
		if thread.ID == threadID {
			found = true
			break
		}
	}
	if !found {
		_ = rt.platform.Reply(ctx, msg, "该 thread 不属于当前工作目录，请先发送 /sessions。")
		return
	}
	conversation, err := e.ensureRemoteConversation(ctx, rt, msg)
	if err != nil {
		_ = rt.platform.Reply(ctx, msg, "读取会话失败："+err.Error())
		return
	}
	if err := e.BindChannelConversation(ctx, rt.channel.ID, conversation.ID, threadID); err != nil {
		_ = rt.platform.Reply(ctx, msg, "绑定失败："+err.Error())
		return
	}
	_ = rt.platform.Reply(ctx, msg, "已绑定 Codex thread："+threadID)
}

func (rt *channelRuntime) remoteStatus(key string) string {
	rt.controlMu.Lock()
	defer rt.controlMu.Unlock()
	state := rt.controlStateLocked(key)
	if state.active == nil {
		return fmt.Sprintf("Codex 状态：空闲\n排队任务：%d", len(state.queue))
	}
	threadID := state.active.task.NativeThreadID
	if threadID == "" {
		threadID = "正在创建"
	}
	return fmt.Sprintf("Codex 状态：%s\nThread：%s\n控制人：%s\n排队任务：%d",
		state.active.task.Status, threadID, state.active.task.ControllerID, len(state.queue))
}

func (e *Engine) stopRemoteTask(ctx context.Context, rt *channelRuntime, msg *Message, data map[string]string) {
	key := ResolveConversationKey(msg)
	rt.controlMu.Lock()
	state := rt.controlStateLocked(key)
	active := state.active
	rt.controlMu.Unlock()
	if active == nil {
		_ = rt.platform.Reply(ctx, msg, "当前没有活动任务。")
	} else if active.task.ControllerID != msg.UserID && !rt.isAdmin(msg.UserID) {
		_ = rt.platform.Reply(ctx, msg, "只有任务控制人或管理员可以停止当前任务。")
	} else {
		rt.controlMu.Lock()
		active.stopRequested = true
		cancelTask := active.cancel
		session := active.session
		rt.controlMu.Unlock()
		if interactive, ok := session.(InteractiveAgentSession); ok {
			interruptCtx, cancelInterrupt := context.WithTimeout(ctx, 15*time.Second)
			if err := interactive.Interrupt(interruptCtx); err != nil {
				e.log.Warn("interrupt Codex task", "task", active.task.ID, "err", err)
			}
			cancelInterrupt()
		}
		if cancelTask != nil {
			cancelTask()
		}
		_ = rt.platform.Reply(ctx, msg, "已请求停止当前 Codex 任务。")
		e.emit(ctx, HookTaskInterrupted, withTaskData(data, active.task, "interrupted"))
	}
	e.emit(ctx, HookMessageSent, data)
}

func (e *Engine) takeOverRemoteTask(ctx context.Context, rt *channelRuntime, msg *Message, data map[string]string) {
	if !rt.isAdmin(msg.UserID) {
		_ = rt.platform.Reply(ctx, msg, "只有渠道管理员可以接管任务。")
		e.emit(ctx, HookMessageSent, data)
		return
	}
	key := ResolveConversationKey(msg)
	rt.controlMu.Lock()
	state := rt.controlStateLocked(key)
	active := state.active
	if active == nil {
		rt.controlMu.Unlock()
		_ = rt.platform.Reply(ctx, msg, "当前没有活动任务。")
		e.emit(ctx, HookMessageSent, data)
		return
	}
	previous := active.task.ControllerID
	active.task.ControllerID = msg.UserID
	active.task.UpdatedAt = time.Now().UTC()
	task := active.task
	rt.controlMu.Unlock()
	_ = e.updateRemoteTask(context.Background(), task)
	audit := withTaskData(data, task, "controller_changed")
	audit["previous_controller_id"] = previous
	audit["resolved_by"] = msg.UserID
	e.emit(ctx, HookTaskTakenOver, audit)
	_ = rt.platform.Reply(ctx, msg, "已接管当前 Codex 任务。")
	e.emit(ctx, HookMessageSent, audit)
}

func (e *Engine) clearRemoteQueue(ctx context.Context, rt *channelRuntime, msg *Message, data map[string]string, confirmed bool) {
	key := ResolveConversationKey(msg)
	confirmKey := key + ":" + msg.UserID
	rt.controlMu.Lock()
	state := rt.controlStateLocked(key)
	admin := rt.isAdmin(msg.UserID)
	count := 0
	for _, task := range state.queue {
		if admin || task.task.ControllerID == msg.UserID {
			count++
		}
	}
	if !confirmed {
		rt.clearConfirm[confirmKey] = time.Now().Add(time.Minute)
		rt.controlMu.Unlock()
		_ = rt.platform.Reply(ctx, msg, fmt.Sprintf("将取消 %d 个排队任务；一分钟内发送 /queue clear confirm 确认。", count))
		e.emit(ctx, HookMessageSent, data)
		return
	}
	if expiry := rt.clearConfirm[confirmKey]; expiry.IsZero() || time.Now().After(expiry) {
		rt.controlMu.Unlock()
		_ = rt.platform.Reply(ctx, msg, "确认已过期，请重新发送 /queue clear。")
		e.emit(ctx, HookMessageSent, data)
		return
	}
	delete(rt.clearConfirm, confirmKey)
	queued := make([]*runtimeChannelTask, 0, count)
	remaining := make([]*runtimeChannelTask, 0, len(state.queue)-count)
	for _, task := range state.queue {
		if admin || task.task.ControllerID == msg.UserID {
			queued = append(queued, task)
		} else {
			remaining = append(remaining, task)
		}
	}
	state.queue = remaining
	rt.controlMu.Unlock()
	now := time.Now().UTC()
	for _, task := range queued {
		task.task.Status = ChannelTaskCancelled
		task.task.Error = "cancelled before start"
		task.task.Prompt = ""
		task.task.FinishedAt = now
		_ = e.updateRemoteTask(context.Background(), task.task)
	}
	_ = rt.platform.Reply(ctx, msg, fmt.Sprintf("已取消 %d 个排队任务。", len(queued)))
	e.emit(ctx, HookMessageSent, data)
}

func (e *Engine) openRemoteThread(ctx context.Context, rt *channelRuntime, msg *Message, data map[string]string) {
	key := ResolveConversationKey(msg)
	rt.controlMu.Lock()
	state := rt.controlStateLocked(key)
	threadID := ""
	if state.active != nil {
		threadID = state.active.task.NativeThreadID
	}
	rt.controlMu.Unlock()
	if threadID == "" && e.conversations != nil {
		conversations, _ := e.conversations.ListConversations(ctx, rt.scope(), false)
		for _, conversation := range conversations {
			if conversation.ConversationKey == key {
				threadID = conversation.NativeSessionID
				break
			}
		}
	}
	if threadID == "" {
		_ = rt.platform.Reply(ctx, msg, "此渠道会话尚未绑定 Codex thread。")
		e.emit(ctx, HookMessageSent, data)
		return
	}
	opener, ok := rt.agent.(NativeThreadOpener)
	if !ok {
		_ = rt.platform.Reply(ctx, msg, "当前环境不支持 Codex deep link。\n请在本机运行：codex resume "+threadID)
		e.emit(ctx, HookMessageSent, data)
		return
	}
	opened, fallback, err := opener.OpenNativeThread(ctx, threadID)
	if fallback == "" {
		fallback = "codex resume " + threadID
	}
	switch {
	case err != nil:
		_ = rt.platform.Reply(ctx, msg, "未能打开 Codex App："+err.Error()+"\n请在本机运行："+fallback)
	case opened:
		_ = rt.platform.Reply(ctx, msg, "已在本机 Codex App 中打开当前 thread。")
	default:
		_ = rt.platform.Reply(ctx, msg, "当前环境不支持 Codex deep link。\n请在本机运行："+fallback)
	}
	e.emit(ctx, HookMessageSent, data)
}

func CodexThreadDeepLink(threadID string) string {
	return "codex://threads/" + strings.TrimSpace(threadID)
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
				return "Codex 需要敏感输入。请在本机 AgentNexus 控制台处理；该内容不会显示或接收于渠道。"
			}
		}
		return "Codex 正等待补充信息，请在支持交互卡片的客户端或本机控制台处理。"
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
	if !local && msg.UserID != record.ControllerID && !rt.isAdmin(msg.UserID) {
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
			_ = rt.platform.Reply(ctx, msg, "Codex 交互已处理。")
		}
	} else {
		_ = rt.platform.Reply(ctx, msg, "Codex 交互已处理。")
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
