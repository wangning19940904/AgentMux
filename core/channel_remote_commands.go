package core

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func (e *Engine) handleRemoteCommand(ctx context.Context, rt *channelRuntime, msg *Message, data map[string]string, text string) bool {
	if e.handleQueueCommand(ctx, rt, msg, text) {
		e.emit(ctx, HookMessageSent, data)
		return true
	}
	lower := strings.ToLower(text)
	switch lower {
	case "/status":
		_ = rt.platform.Reply(ctx, msg, rt.remoteStatus(ResolveConversationKey(msg)))
		e.emit(ctx, HookMessageSent, data)
		return true
	case "停止", "/stop":
		e.stopRemoteTask(ctx, rt, msg, data, "")
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
			_ = rt.platform.Reply(ctx, msg, "已加入任务队列。")
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

func (e *Engine) handleRemoteTaskAction(ctx context.Context, rt *channelRuntime, msg *Message, data map[string]string, action ChannelTaskAction) {
	switch action.Action {
	case ChannelTaskActionSteer, ChannelTaskActionCancel:
		if err := e.controlQueuedTask(ctx, rt, msg, action); err != nil {
			_ = rt.platform.Reply(ctx, msg, err.Error())
		}
	case ChannelTaskActionStop:
		e.stopRemoteTask(ctx, rt, msg, data, action.TaskID)
	default:
		_ = rt.platform.Reply(ctx, msg, "不支持的任务操作。")
		e.emit(ctx, HookMessageSent, data)
	}
}

func (e *Engine) ensureRemoteConversation(ctx context.Context, rt *channelRuntime, msg *Message) (*Conversation, error) {
	_, workDir, opts := rt.agentSnapshot()
	if opts.WorkDir == "" {
		opts.WorkDir = workDir
	}
	conversation, _, err := e.prepareConversation(ctx, rt.scope(), msg.ChatID, msg.ChatType,
		ResolveConversationKey(msg), opts, workDir)
	return conversation, err
}

func (e *Engine) listRemoteThreads(ctx context.Context, rt *channelRuntime, msg *Message) {
	agent, _, _ := rt.agentSnapshot()
	catalog, ok := agent.(NativeThreadAgent)
	if !ok {
		_ = rt.platform.Reply(ctx, msg, "当前运行时不支持会话 列表。")
		return
	}
	conversation, err := e.ensureRemoteConversation(ctx, rt, msg)
	if err != nil {
		_ = rt.platform.Reply(ctx, msg, "读取会话失败："+err.Error())
		return
	}
	threads, err := catalog.ListNativeThreads(ctx, conversation.WorkDir)
	if err != nil {
		_ = rt.platform.Reply(ctx, msg, "读取原生会话失败："+err.Error())
		return
	}
	rt.controlMu.Lock()
	rt.threadLists[conversation.ConversationKey] = append([]NativeThread(nil), threads...)
	rt.controlMu.Unlock()
	if len(threads) == 0 {
		_ = rt.platform.Reply(ctx, msg, "当前目录没有可绑定的原生会话。")
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
		agent, _, _ := rt.agentSnapshot()
		catalog, ok := agent.(NativeThreadAgent)
		if !ok {
			_ = rt.platform.Reply(ctx, msg, "当前运行时不支持会话 绑定。")
			return
		}
		conversation, err := e.ensureRemoteConversation(ctx, rt, msg)
		if err != nil {
			_ = rt.platform.Reply(ctx, msg, "读取会话失败："+err.Error())
			return
		}
		threads, err = catalog.ListNativeThreads(ctx, conversation.WorkDir)
		if err != nil {
			_ = rt.platform.Reply(ctx, msg, "读取原生会话失败："+err.Error())
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
	_ = rt.platform.Reply(ctx, msg, "已绑定原生会话："+threadID)
}

func (rt *channelRuntime) remoteStatus(key string) string {
	rt.controlMu.Lock()
	defer rt.controlMu.Unlock()
	state := rt.controlStateLocked(key)
	if state.active == nil {
		return fmt.Sprintf("任务状态：空闲\n排队任务：%d", len(state.queue))
	}
	threadID := state.active.task.NativeThreadID
	if threadID == "" {
		threadID = "正在创建"
	}
	return fmt.Sprintf("任务状态：%s\nThread：%s\n控制人：%s\n排队任务：%d",
		state.active.task.Status, threadID, state.active.task.ControllerID, len(state.queue))
}

func (e *Engine) stopRemoteTask(ctx context.Context, rt *channelRuntime, msg *Message, data map[string]string, expectedTaskID string) {
	key := ResolveConversationKey(msg)
	manager := rt.isChatManager(ctx, msg)
	rt.controlMu.Lock()
	state := rt.controlStateLocked(key)
	active := state.active
	if active == nil {
		rt.controlMu.Unlock()
		_ = rt.platform.Reply(ctx, msg, "当前没有活动任务。")
	} else if expectedTaskID != "" && active.task.ID != expectedTaskID {
		rt.controlMu.Unlock()
		_ = rt.platform.Reply(ctx, msg, "该任务已结束或不再是当前任务。")
	} else if active.task.ControllerID != msg.UserID && !manager {
		rt.controlMu.Unlock()
		_ = rt.platform.Reply(ctx, msg, "只有任务控制人或管理员可以停止当前任务。")
	} else {
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
		_ = rt.platform.Reply(ctx, msg, "已请求停止当前任务。")
		e.emit(ctx, HookTaskInterrupted, withTaskData(data, active.task, "interrupted"))
	}
	e.emit(ctx, HookMessageSent, data)
}

func (e *Engine) takeOverRemoteTask(ctx context.Context, rt *channelRuntime, msg *Message, data map[string]string) {
	if !rt.isChatManager(ctx, msg) {
		_ = rt.platform.Reply(ctx, msg, "只有群主或群管理员可以接管任务。")
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
	_ = rt.platform.Reply(ctx, msg, "已接管当前任务。")
	e.emit(ctx, HookMessageSent, audit)
}

func (e *Engine) clearRemoteQueue(ctx context.Context, rt *channelRuntime, msg *Message, data map[string]string, confirmed bool) {
	key := ResolveConversationKey(msg)
	confirmKey := key + ":" + msg.UserID
	admin := rt.isChatManager(ctx, msg)
	rt.controlMu.Lock()
	state := rt.controlStateLocked(key)
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
	remaining := make([]*runtimeChannelTask, 0, len(state.queue))
	for _, task := range state.queue {
		if task.task.Status != ChannelTaskQueued || !(admin || task.task.ControllerID == msg.UserID) {
			remaining = append(remaining, task)
			continue
		}
		previous := task.task
		task.task.Status = ChannelTaskCancelled
		task.task.Error = "cancelled before start"
		task.task.Prompt = ""
		task.task.FinishedAt = time.Now().UTC()
		if err := e.updateRemoteTask(ctx, task.task); err != nil {
			task.task = previous
			remaining = append(remaining, task)
			continue
		}
		queued = append(queued, task)
	}
	state.queue = remaining
	next := e.startNextRemoteLocked(rt, state)
	rt.controlMu.Unlock()
	for _, task := range queued {
		if task.msg != nil {
			e.refreshQueueCards(rt, task)
		}
	}
	if next != nil {
		go e.runRemoteTask(rt.runCtx, rt, next, eventData(next.msg))
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
	agent, _, _ := rt.agentSnapshot()
	opener, ok := agent.(NativeThreadOpener)
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
		_ = rt.platform.Reply(ctx, msg, "请在本机运行："+fallback)
	}
	e.emit(ctx, HookMessageSent, data)
}
