package core

import (
	"context"
	"strings"
	"time"
)

// directChannelTurn is the lightweight single-flight guard for channel
// runtimes that do not use durable Codex task control. It prevents repeated
// follow-up messages from spawning overlapping CLI agents while an earlier
// subprocess is still active.
type directChannelTurn struct {
	ctx           context.Context
	controllerID  string
	cancel        context.CancelFunc
	done          chan struct{}
	stopRequested bool
	task          *ChannelTask
	msg           *Message
	runErr        string
}

func (rt *channelRuntime) beginDirectTurn(ctx context.Context, key, controllerID string, cancel context.CancelFunc) (*directChannelTurn, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		key = "default"
	}
	turn := &directChannelTurn{ctx: ctx, controllerID: controllerID, cancel: cancel, done: make(chan struct{})}
	rt.controlMu.Lock()
	defer rt.controlMu.Unlock()
	if rt.directTurns == nil {
		rt.directTurns = map[string]*directChannelTurn{}
	}
	if state := rt.controlTasks[key]; state != nil && state.active != nil {
		return nil, false
	}
	if rt.directTurns[key] != nil {
		return nil, false
	}
	rt.directTurns[key] = turn
	return turn, true
}

func (rt *channelRuntime) finishDirectTurn(key string, turn *directChannelTurn) {
	if turn == nil {
		return
	}
	key = strings.TrimSpace(key)
	if key == "" {
		key = "default"
	}
	var completed *ChannelTask
	rt.controlMu.Lock()
	if rt.directTurns[key] == turn {
		delete(rt.directTurns, key)
		if turn.task != nil {
			now := time.Now().UTC()
			turn.task.FinishedAt = now
			turn.task.UpdatedAt = now
			switch {
			case turn.stopRequested:
				turn.task.Status = ChannelTaskInterrupted
				if turn.task.Error == "" {
					turn.task.Error = "interrupted by task controller"
				}
			case turn.ctx != nil && turn.ctx.Err() != nil:
				turn.task.Status = ChannelTaskInterrupted
				turn.task.Error = turn.ctx.Err().Error()
			case turn.runErr != "":
				turn.task.Status = ChannelTaskFailed
				turn.task.Error = turn.runErr
			default:
				turn.task.Status = ChannelTaskSucceeded
			}
			copy := *turn.task
			completed = &copy
		}
		close(turn.done)
	}
	rt.controlMu.Unlock()
	if completed != nil {
		_ = rt.owner.updateRemoteTask(context.Background(), *completed)
		rt.owner.emit(context.Background(), HookTaskCompleted, withTaskData(eventData(turn.msg), *completed, string(completed.Status)))
	}
	if rt.owner != nil {
		rt.controlMu.Lock()
		state := rt.controlStateLocked(key)
		next := rt.owner.startNextRemoteLocked(rt, state)
		rt.controlMu.Unlock()
		if next != nil {
			rt.owner.refreshQueueCards(rt, next)
			go rt.owner.runRemoteTask(rt.runCtx, rt, next, eventData(next.msg))
		}
	}

}

func (rt *channelRuntime) attachDirectTask(key string, turn *directChannelTurn, task ChannelTask, msg *Message) bool {
	key = normalizedConversationRuntimeKey(key)
	rt.controlMu.Lock()
	defer rt.controlMu.Unlock()
	if rt.directTurns[key] != turn {
		return false
	}
	copy := task
	turn.task = &copy
	turn.msg = cloneChannelMessage(msg)
	return true
}

func (rt *channelRuntime) directTurn(key string) *directChannelTurn {
	key = strings.TrimSpace(key)
	if key == "" {
		key = "default"
	}
	rt.controlMu.Lock()
	defer rt.controlMu.Unlock()
	return rt.directTurns[key]
}

func (e *Engine) handleDirectStop(ctx context.Context, rt *channelRuntime, msg *Message, data map[string]string) bool {
	text := strings.ToLower(strings.TrimSpace(msg.Text))
	if text != "/stop" && text != "停止" {
		return false
	}
	turn := rt.directTurn(ResolveConversationKey(msg))
	if turn == nil {
		_ = rt.platform.Reply(ctx, msg, "当前没有活动任务。")
	} else if turn.controllerID != "" && turn.controllerID != msg.UserID && !rt.isAdmin(msg.UserID) {
		_ = rt.platform.Reply(ctx, msg, "只有当前任务发起人或渠道管理员可以停止任务。")
	} else {
		rt.controlMu.Lock()
		turn.stopRequested = true
		rt.controlMu.Unlock()
		turn.cancel()
		_ = rt.platform.Reply(ctx, msg, "已请求停止当前任务。")
	}
	e.emit(ctx, HookMessageSent, data)
	return true
}

func (e *Engine) handleDirectTaskAction(ctx context.Context, rt *channelRuntime, msg *Message, data map[string]string, action ChannelTaskAction) {
	if action.Action != ChannelTaskActionStop {
		_ = rt.platform.Reply(ctx, msg, "未知任务操作。")
		return
	}
	key := ResolveConversationKey(msg)
	rt.controlMu.Lock()
	turn := rt.directTurns[key]
	if turn == nil || turn.task == nil || turn.task.ID != action.TaskID {
		rt.controlMu.Unlock()
		_ = rt.platform.Reply(ctx, msg, "该任务已结束或卡片已过期。")
		return
	}
	if turn.controllerID != "" && turn.controllerID != msg.UserID && !rt.isAdmin(msg.UserID) {
		rt.controlMu.Unlock()
		_ = rt.platform.Reply(ctx, msg, "只有当前任务发起人或渠道管理员可以停止任务。")
		return
	}
	turn.stopRequested = true
	cancel := turn.cancel
	rt.controlMu.Unlock()
	if cancel != nil {
		cancel()
	}
	_ = rt.platform.Reply(ctx, msg, "已请求停止当前任务。")
	e.emit(ctx, HookTaskInterrupted, withTaskData(data, ChannelTask{ID: action.TaskID, ChannelID: rt.channel.ID, ConversationKey: key}, "interrupted"))
	e.emit(ctx, HookMessageSent, data)
}

func (rt *channelRuntime) cancelDirectTurnForReset(ctx context.Context, key string) {
	turn := rt.directTurn(key)
	if turn == nil {
		return
	}
	turn.cancel()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-turn.done:
	case <-ctx.Done():
	case <-timer.C:
	}
}
