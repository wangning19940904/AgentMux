package core

import (
	"context"
	"strings"
)

func (e *Engine) handleChannelFeedback(ctx context.Context, rt *channelRuntime, msg *Message, data map[string]string, action ChannelFeedbackAction) {
	semantic := strings.ToLower(strings.TrimSpace(action.Semantic))
	if e.feedbackStore == nil {
		_ = rt.platform.Reply(ctx, msg, "反馈存储暂不可用。")
		return
	}
	if !ValidFeedbackSemantic(semantic) || strings.TrimSpace(action.TaskID) == "" || strings.TrimSpace(action.Nonce) == "" || strings.TrimSpace(msg.UserID) == "" {
		_ = rt.platform.Reply(ctx, msg, "反馈无效或已过期。")
		return
	}
	feedback := ChannelFeedback{
		ID: NewChannelControlID("feedback"), TaskID: strings.TrimSpace(action.TaskID),
		ChannelID: rt.channel.ID, UserID: strings.TrimSpace(msg.UserID), Semantic: semantic,
	}
	recorded, err := e.feedbackStore.SubmitChannelFeedback(ctx, feedback, strings.TrimSpace(action.Nonce))
	if err != nil {
		e.emit(ctx, HookError, withError(data, err))
		_ = rt.platform.Reply(ctx, msg, "记录反馈失败，请稍后重试。")
		return
	}
	if !recorded {
		_ = rt.platform.Reply(ctx, msg, "反馈未生效：卡片已过期，或你不是本次任务的发起人。")
		return
	}
	feedbackData := withTaskData(data, ChannelTask{ID: feedback.TaskID, ChannelID: feedback.ChannelID}, "feedback")
	feedbackData["feedback_semantic"] = semantic
	feedbackData["feedback_user_id"] = feedback.UserID
	e.emit(ctx, HookFeedbackReceived, feedbackData)
	_ = rt.platform.Reply(ctx, msg, feedbackAcknowledgement(semantic))
}

func feedbackAcknowledgement(semantic string) string {
	switch semantic {
	case FeedbackPositive:
		return "已记录：结论可用。谢谢反馈！"
	case FeedbackProgress:
		return "已记录：有效推进。谢谢反馈！"
	case FeedbackNegative:
		return "已记录：结论有误。后续可在 AgentMux 控制台补充原因。"
	default:
		return "反馈已记录。"
	}
}
