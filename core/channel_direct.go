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
	controllerID  string
	cancel        context.CancelFunc
	done          chan struct{}
	stopRequested bool
}

func (rt *channelRuntime) beginDirectTurn(key, controllerID string, cancel context.CancelFunc) (*directChannelTurn, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		key = "default"
	}
	turn := &directChannelTurn{controllerID: controllerID, cancel: cancel, done: make(chan struct{})}
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
	rt.controlMu.Lock()
	if rt.directTurns[key] == turn {
		delete(rt.directTurns, key)
		close(turn.done)
	}
	rt.controlMu.Unlock()
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
		turn.cancel()
		_ = rt.platform.Reply(ctx, msg, "已请求停止当前任务。")
	}
	e.emit(ctx, HookMessageSent, data)
	return true
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
