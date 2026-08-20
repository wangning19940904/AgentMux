package core

import (
	"context"
	"fmt"
	"strings"
)

func (e *Engine) handleChannelSessionAction(ctx context.Context, rt *channelRuntime, msg *Message, data map[string]string, action ChannelSessionAction) {
	if e.channelControl == nil || strings.TrimSpace(action.TaskID) == "" {
		_ = rt.platform.Reply(ctx, msg, "会话操作已过期。")
		return
	}
	task, err := e.channelControl.GetChannelTask(ctx, strings.TrimSpace(action.TaskID))
	if err != nil || task == nil || task.ChannelID != rt.channel.ID || task.UserID == "" || task.UserID != msg.UserID {
		_ = rt.platform.Reply(ctx, msg, "只有本次任务发起人可以操作该会话。")
		return
	}
	switch action.Action {
	case ChannelSessionActionNew:
		clone := cloneChannelMessage(msg)
		clone.ChannelSessionAction = nil
		clone.ConversationKey = task.ConversationKey
		clone.ChatID, clone.ChatType = task.ChatID, task.ChatType
		clone.RootID, clone.ThreadID = task.RootID, task.ThreadID
		clone.Text = "/new"
		e.handleChannelMessage(ctx, clone, eventData(clone))
		return
	case ChannelSessionActionStatus:
		state, stateErr := e.ConversationRuntimeState(ctx, rt.channel.ID, task.ConversationKey)
		if stateErr != nil {
			_ = rt.platform.Reply(ctx, msg, "读取会话状态失败："+stateErr.Error())
			return
		}
		_ = rt.platform.Reply(ctx, msg, fmt.Sprintf("会话状态：%s", state.Status))
	default:
		_ = rt.platform.Reply(ctx, msg, "未知会话操作。")
	}
	e.emit(ctx, HookMessageSent, data)
}
