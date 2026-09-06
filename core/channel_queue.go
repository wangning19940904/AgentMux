package core

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"
)

// QueueReplier renders or patches a task's durable queue control card. The
// task id and nonce identify an exact item; prompts are never callback values.
type QueueReplier interface {
	ReplyQueueTask(context.Context, *Message, ChannelTask, string) (string, error)
}

func (e *Engine) refreshQueueCards(rt *channelRuntime, changed *runtimeChannelTask) {
	if rt == nil || changed == nil || changed.msg == nil {
		return
	}
	rt.queueCardMu.Lock()
	defer rt.queueCardMu.Unlock()
	rt.controlMu.Lock()
	state := rt.controlStateLocked(changed.task.ConversationKey)
	items := append([]*runtimeChannelTask{}, state.queue...)
	found := false
	for _, item := range items {
		if item == changed {
			found = true
		}
	}
	if !found {
		items = append(items, changed)
	}
	rt.controlMu.Unlock()
	replier, hasCards := rt.platform.(QueueReplier)
	for _, item := range items {
		rt.controlMu.Lock()
		task := item.task
		if task.ControlNonce == "" {
			rt.controlMu.Unlock()
			continue
		}
		task.QueuePosition = 0
		task.CanSteer = false
		for i, q := range state.queue {
			if q == item {
				task.QueuePosition = i + 1
			}
		}
		if task.Status == ChannelTaskQueued && state.active != nil && state.active.task.ID == task.TargetTaskID && !state.steering {
			_, task.CanSteer = state.active.session.(InteractiveAgentSession)
		}
		content := item.msg.Text
		msg := cloneChannelMessage(item.msg)
		rt.controlMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if hasCards {
			cardID, err := replier.ReplyQueueTask(ctx, msg, task, content)
			if err == nil && cardID != "" && task.ControlCardID == "" {
				rt.controlMu.Lock()
				item.task.ControlCardID = cardID
				err = e.updateRemoteTask(ctx, item.task)
				rt.controlMu.Unlock()
			}
			if err != nil {
				e.log.Warn("queue card", "task", task.ID, "err", err)
				if item == changed && task.Status == ChannelTaskQueued {
					_ = rt.platform.Reply(ctx, msg, fmt.Sprintf("已排队（第 %d 项）。卡片暂不可用，可用 /steer-task %s 调整方向，或 /queue cancel %s 取消。", task.QueuePosition, task.ID, task.ID))
				}
			}
		} else if task.Status == ChannelTaskQueued && task.ControlCardID == "" {
			_ = rt.platform.Reply(ctx, msg, fmt.Sprintf("已排队（第 %d 项，%s）。可用 /steer-task %s 调整方向，或 /queue cancel %s 取消。", task.QueuePosition, task.ID, task.ID, task.ID))
			rt.controlMu.Lock()
			item.task.ControlCardID = "text"
			_ = e.updateRemoteTask(ctx, item.task)
			rt.controlMu.Unlock()
		}
		cancel()
	}
}

func (e *Engine) controlQueuedTask(ctx context.Context, rt *channelRuntime, msg *Message, action ChannelTaskAction) error {
	key := ResolveConversationKey(msg)
	manager := rt.isChatManager(ctx, msg)
	rt.controlMu.Lock()
	state := rt.controlStateLocked(key)
	var item *runtimeChannelTask
	index := -1
	for i, q := range state.queue {
		if q.task.ID == action.TaskID {
			item = q
			index = i
			break
		}
	}
	if item == nil || item.task.Status != ChannelTaskQueued {
		rt.controlMu.Unlock()
		return fmt.Errorf("该排队项已开始、已处理或已过期")
	}
	if msg.InteractionMessageID != "" && (action.Nonce == "" || subtle.ConstantTimeCompare([]byte(action.Nonce), []byte(item.task.ControlNonce)) != 1) {
		rt.controlMu.Unlock()
		return fmt.Errorf("无效的任务操作")
	}
	if item.task.UserID != msg.UserID && !manager {
		rt.controlMu.Unlock()
		return fmt.Errorf("只有消息发起人、群主或群管理员可以操作此排队项")
	}
	if action.Action == ChannelTaskActionCancel {
		previous := item.task
		item.task.Status = ChannelTaskCancelled
		item.task.FinishedAt = time.Now().UTC()
		item.task.Prompt = ""
		if err := e.updateRemoteTask(ctx, item.task); err != nil {
			item.task = previous
			rt.controlMu.Unlock()
			return err
		}
		state.queue = append(state.queue[:index], state.queue[index+1:]...)
		next := e.startNextRemoteLocked(rt, state)
		rt.controlMu.Unlock()
		e.refreshQueueCards(rt, item)
		if next != nil {
			go e.runRemoteTask(rt.runCtx, rt, next, eventData(next.msg))
		}
		return nil
	}
	active := state.active
	if action.Action != ChannelTaskActionSteer || active == nil || active.task.ID != item.task.TargetTaskID {
		rt.controlMu.Unlock()
		return fmt.Errorf("原任务已结束，消息保留排队，不会追加到其他任务")
	}
	if active.task.ControllerID != msg.UserID && !manager {
		rt.controlMu.Unlock()
		return fmt.Errorf("只有当前任务发起人、群主或群管理员可以调整方向")
	}
	session, ok := active.session.(InteractiveAgentSession)
	if !ok || (active.generation != nil && active.generation.retired.Load()) {
		rt.controlMu.Unlock()
		return fmt.Errorf("当前运行时无法调整方向，消息保留排队")
	}
	if state.steering {
		rt.controlMu.Unlock()
		return fmt.Errorf("正在处理另一条追加，请稍后重试")
	}
	previous := item.task
	item.task.TurnID = session.ActiveTurnID()
	item.task.Status = ChannelTaskSteering
	if err := e.updateRemoteTask(ctx, item.task); err != nil {
		item.task = previous
		rt.controlMu.Unlock()
		return err
	}
	state.steering = true
	rt.controlMu.Unlock()
	go e.refreshQueueCards(rt, item)
	steerCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	input := channelMessageForAgent(rt.channel, item.msg)
	err := session.Steer(steerCtx, input.Text)
	cancel()
	rt.controlMu.Lock()
	var rejected *SteerRejectedError
	switch {
	case err == nil:
		item.task.Status = ChannelTaskSteered
		item.task.Prompt = ""
		item.task.FinishedAt = time.Now().UTC()
	case errors.As(err, &rejected):
		item.task.Status = ChannelTaskQueued
		item.task.Error = "追加被拒绝，保留排队：" + err.Error()
	default:
		item.task.Status = ChannelTaskSteerUnknown
		item.task.Error = "追加结果待确认，不会自动重试：" + err.Error()
		item.task.Prompt = ""
	}
	persistErr := e.updateRemoteTask(context.Background(), item.task)
	if persistErr != nil && item.task.Status == ChannelTaskQueued {
		item.task.Status = ChannelTaskSteerUnknown
		item.task.Error = "追加状态保存失败，停止自动执行"
	}
	if item.task.Status != ChannelTaskQueued {
		for i, q := range state.queue {
			if q == item {
				state.queue = append(state.queue[:i], state.queue[i+1:]...)
				break
			}
		}
	}
	state.steering = false
	next := e.startNextRemoteLocked(rt, state)
	snapshot := item.task
	rt.controlMu.Unlock()
	if err == nil {
		e.emit(ctx, HookTaskSteered, withTaskData(eventData(msg), snapshot, "steered"))
	}
	e.refreshQueueCards(rt, item)
	if next != nil {
		go e.runRemoteTask(rt.runCtx, rt, next, eventData(next.msg))
	}
	if persistErr != nil {
		return persistErr
	}
	if err != nil {
		return fmt.Errorf("%s", snapshot.Error)
	}
	return nil
}

func (e *Engine) handleQueueCommand(ctx context.Context, rt *channelRuntime, msg *Message, text string) bool {
	if text == "/queue" {
		rt.controlMu.Lock()
		state := rt.controlStateLocked(ResolveConversationKey(msg))
		lines := []string{"等待队列："}
		for i, q := range state.queue {
			summary := []rune(q.msg.Text)
			if len(summary) > 120 {
				summary = summary[:120]
			}
			lines = append(lines, fmt.Sprintf("%d. %s — %s", i+1, q.task.ID, string(summary)))
		}
		if len(state.queue) == 0 {
			lines = append(lines, "暂无等待任务。")
		}
		rt.controlMu.Unlock()
		_ = rt.platform.Reply(ctx, msg, strings.Join(lines, "\n"))
		return true
	}
	action := ChannelTaskAction{}
	if strings.HasPrefix(text, "/queue cancel ") {
		action = ChannelTaskAction{TaskID: strings.TrimSpace(strings.TrimPrefix(text, "/queue cancel ")), Action: ChannelTaskActionCancel}
	}
	if strings.HasPrefix(text, "/steer-task ") {
		action = ChannelTaskAction{TaskID: strings.TrimSpace(strings.TrimPrefix(text, "/steer-task ")), Action: ChannelTaskActionSteer}
	}
	if strings.HasPrefix(text, "/steer ") {
		rt.controlMu.Lock()
		active := rt.controlStateLocked(ResolveConversationKey(msg)).active
		rt.controlMu.Unlock()
		if active == nil {
			_ = rt.platform.Reply(ctx, msg, "当前没有可调整方向的任务。")
			return true
		}
		prompt := strings.TrimSpace(strings.TrimPrefix(text, "/steer "))
		item, err := e.enqueueRemoteTask(ctx, rt, msg, prompt)
		if err != nil {
			_ = rt.platform.Reply(ctx, msg, err.Error())
			return true
		}
		action = ChannelTaskAction{TaskID: item.task.ID, Action: ChannelTaskActionSteer}
	}
	if action.Action == "" {
		return false
	}
	if err := e.controlQueuedTask(ctx, rt, msg, action); err != nil {
		_ = rt.platform.Reply(ctx, msg, err.Error())
	}
	return true
}
