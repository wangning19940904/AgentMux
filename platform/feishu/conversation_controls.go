package feishu

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/wangning19940904/AgentMux/core"
)

const queueControlAction = "channel_queue_control"
const conversationModeAction = "conversation_mode"

type conversationControlClient interface {
	QueueTaskCard(context.Context, *core.Message, core.ChannelTask, string) (string, error)
	ModeCard(context.Context, *core.Message, core.ConversationModeState) error
	ChatInformation(context.Context, string, bool) (*larkim.GetChatRespData, error)
	CreateSessionChat(context.Context, string, string, string) (string, error)
}
type cachedConversationChat struct {
	info *larkim.GetChatRespData
	at   time.Time
}

func (p *Platform) ReplyQueueTask(ctx context.Context, msg *core.Message, task core.ChannelTask, text string) (string, error) {
	c, ok := p.client.(conversationControlClient)
	if !ok {
		return "", fmt.Errorf("任务卡片不可用")
	}
	return c.QueueTaskCard(ctx, msg, task, text)
}
func (p *Platform) ReplyConversationMode(ctx context.Context, msg *core.Message, state core.ConversationModeState) error {
	c, ok := p.client.(conversationControlClient)
	if !ok {
		return fmt.Errorf("模式选择卡不可用")
	}
	return c.ModeCard(ctx, msg, state)
}
func (p *Platform) ConversationChat(ctx context.Context, id string) (core.ConversationChatInfo, error) {
	c, ok := p.client.(conversationControlClient)
	if !ok {
		return core.ConversationChatInfo{}, nil
	}
	info, err := c.ChatInformation(ctx, id, false)
	if err != nil {
		return core.ConversationChatInfo{}, err
	}
	return core.ConversationChatInfo{Topic: info.ChatMode != nil && *info.ChatMode == "topic", OwnerID: stringPtr(info.OwnerId)}, nil
}
func (p *Platform) CanManageConversationChat(ctx context.Context, id, user string) (bool, error) {
	c, ok := p.client.(conversationControlClient)
	if !ok {
		return false, fmt.Errorf("无法读取群管理权限")
	}
	info, err := c.ChatInformation(ctx, id, true)
	if err != nil {
		return false, err
	}
	if stringPtr(info.OwnerId) == user {
		return true, nil
	}
	for _, id := range info.UserManagerIdList {
		if id == user {
			return true, nil
		}
	}
	return false, nil
}
func (p *Platform) CreateConversationGroup(ctx context.Context, user, title, key string) (string, error) {
	c, ok := p.client.(conversationControlClient)
	if !ok {
		return "", &core.ConversationGroupRejectedError{Reason: "此连接不支持建群"}
	}
	return c.CreateSessionChat(ctx, user, title, key)
}
func stringPtr(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (c *larkClient) ChatInformation(ctx context.Context, id string, fresh bool) (*larkim.GetChatRespData, error) {
	if cached, ok := c.chatInfo.Load(id); ok && !fresh {
		v := cached.(cachedConversationChat)
		if time.Since(v.at) < time.Minute {
			return v.info, nil
		}
	}
	resp, err := c.api.Im.Chat.Get(ctx, larkim.NewGetChatReqBuilder().ChatId(id).UserIdType("open_id").Build())
	if err != nil {
		return nil, err
	}
	if !resp.Success() || resp.Data == nil {
		return nil, fmt.Errorf("读取群信息失败：%s", resp.Msg)
	}
	c.chatInfo.Store(id, cachedConversationChat{info: resp.Data, at: time.Now()})
	return resp.Data, nil
}
func (c *larkClient) CreateSessionChat(ctx context.Context, user, title, key string) (string, error) {
	digest := sha256.Sum256([]byte(key))
	uuid := hex.EncodeToString(digest[:16])
	if title == "" {
		title = "新任务"
	}
	req := larkim.NewCreateChatReqBuilder().UserIdType("open_id").Uuid(uuid).Body(
		larkim.NewCreateChatReqBodyBuilder().Name("AgentMux · " + title).OwnerId(user).UserIdList([]string{user}).ChatMode("group").ChatType("private").Build()).Build()
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		resp, err := c.api.Im.Chat.Create(ctx, req)
		if err == nil && resp.Success() && resp.Data != nil && resp.Data.ChatId != nil {
			return *resp.Data.ChatId, nil
		}
		if err == nil && !resp.Success() {
			// Rate/service errors are retriable with the SAME request UUID.
			if resp.Code != 99991400 && resp.Code != 99991663 && resp.Code != 99991500 {
				return "", &core.ConversationGroupRejectedError{Reason: resp.Msg}
			}
			err = fmt.Errorf("建群服务暂不可用：%s", resp.Msg)
		}
		if err == nil {
			err = fmt.Errorf("建群响应缺少群标识")
		}
		last = err
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 200 * time.Millisecond):
		}
	}
	return "", last
}

func (c *larkClient) sendControlCard(ctx context.Context, msg *core.Message, content, existing string) (string, error) {
	if existing != "" {
		resp, err := c.api.Im.Message.Patch(ctx, larkim.NewPatchMessageReqBuilder().MessageId(existing).Body(larkim.NewPatchMessageReqBodyBuilder().Content(content).Build()).Build())
		if err != nil {
			return "", err
		}
		if !resp.Success() {
			return "", fmt.Errorf("更新卡片失败：%s", resp.Msg)
		}
		return existing, nil
	}
	if shouldReplyInThread(msg) {
		return c.replyMessage(ctx, msg.ID, larkim.MsgTypeInteractive, content)
	}
	resp, err := c.api.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().ReceiveIdType("chat_id").Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(msg.ChatID).MsgType(larkim.MsgTypeInteractive).Content(content).Build()).Build())
	if err != nil {
		return "", err
	}
	if !resp.Success() || resp.Data == nil || resp.Data.MessageId == nil {
		return "", fmt.Errorf("发送卡片失败：%s", resp.Msg)
	}
	return *resp.Data.MessageId, nil
}
func (c *larkClient) QueueTaskCard(ctx context.Context, msg *core.Message, task core.ChannelTask, text string) (string, error) {
	return c.sendControlCard(ctx, msg, buildQueueTaskCard(msg, task, text), task.ControlCardID)
}
func buildQueueTaskCard(msg *core.Message, task core.ChannelTask, text string) string {
	title := "等待执行"
	detail := ""
	color := "blue"
	switch task.Status {
	case core.ChannelTaskQueued:
		detail = fmt.Sprintf("排队第 %d 项 · 当前任务结束后自动执行", task.QueuePosition)
	case core.ChannelTaskRunning, core.ChannelTaskWaitingInput:
		title = "执行中"
	case core.ChannelTaskSteering:
		title = "正在调整方向"
	case core.ChannelTaskSteered:
		title = "已追加到当前任务"
		color = "green"
	case core.ChannelTaskCancelled:
		title = "已取消"
		color = "grey"
	case core.ChannelTaskSucceeded:
		title = "已完成"
		color = "green"
	case core.ChannelTaskSteerUnknown:
		title = "追加结果待确认"
		color = "orange"
	default:
		title = "任务已结束"
		color = "grey"
	}
	summary := []rune(strings.TrimSpace(text))
	if len(summary) > 240 {
		summary = append(summary[:240], []rune("…")...)
	}
	elements := []map[string]any{{"tag": "markdown", "content": escapeControlText(string(summary))}, {"tag": "markdown", "content": detail}}
	if task.Error != "" {
		elements = append(elements, map[string]any{"tag": "markdown", "content": escapeControlText(task.Error)})
	}
	if task.Status == core.ChannelTaskQueued {
		buttons := []map[string]any{}
		for _, action := range []struct{ kind, label string }{{core.ChannelTaskActionSteer, "调整方向"}, {core.ChannelTaskActionCancel, "取消"}} {
			if action.kind == core.ChannelTaskActionSteer && !task.CanSteer {
				continue
			}
			value := map[string]any{modelPickerActionKey: queueControlAction, "task_id": task.ID, "nonce": task.ControlNonce, "action": action.kind, "chat_id": msg.ChatID, "chat_type": msg.ChatType, "conversation_key": task.ConversationKey}
			buttons = append(buttons, modelPickerButton(action.label, "default", value))
		}
		elements = append(elements, controlButtonRow(buttons))
		if !task.CanSteer {
			elements = append(elements, map[string]any{"tag": "markdown", "content": "当前任务暂不支持调整方向，消息会按顺序执行。"})
		}
	}
	// Use the same schema-2 callback buttons as runtime settings cards.
	b, _ := json.Marshal(map[string]any{"schema": "2.0", "config": map[string]any{"wide_screen_mode": true}, "header": map[string]any{"template": color, "title": map[string]any{"tag": "plain_text", "content": title}}, "body": map[string]any{"elements": elements}})
	return string(b)
}
func (c *larkClient) ModeCard(ctx context.Context, msg *core.Message, state core.ConversationModeState) error {
	_, err := c.sendControlCard(ctx, msg, buildConversationModeCard(msg, state), "")
	return err
}
func buildConversationModeCard(msg *core.Message, state core.ConversationModeState) string {
	labels := []struct{ mode, label string }{{"chat", "群内连续会话"}, {"chat-topic", "顶层连续，话题独立"}, {"new-topic", "每条消息新话题"}}
	title := "群聊模式"
	if state.Private {
		title = "私聊模式"
		labels = []struct{ mode, label string }{{"chat", "连续会话"}, {"thread", "每条消息独立话题"}, {"group", "每条消息创建会话群"}}
	}
	elements := []map[string]any{{"tag": "markdown", "content": "当前模式：**" + state.Mode + "**\n" + state.Notice}}
	for _, option := range labels {
		value := map[string]any{modelPickerActionKey: conversationModeAction, "mode": option.mode, "user_id": state.UserID, "chat_id": msg.ChatID, "chat_type": msg.ChatType}
		elements = append(elements, controlButtonRow([]map[string]any{modelPickerButton(option.label, "default", value)}))
	}
	b, _ := json.Marshal(map[string]any{"schema": "2.0", "config": map[string]any{"wide_screen_mode": true}, "header": map[string]any{"title": map[string]any{"tag": "plain_text", "content": title}}, "body": map[string]any{"elements": elements}})
	return string(b)
}

func controlButtonRow(buttons []map[string]any) map[string]any {
	columns := make([]map[string]any, 0, len(buttons))
	for _, button := range buttons {
		columns = append(columns, map[string]any{"tag": "column", "width": "weighted", "weight": 1, "elements": []map[string]any{button}})
	}
	return map[string]any{"tag": "column_set", "flex_mode": "none", "columns": columns}
}
func escapeControlText(text string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "*", `\*`, "_", `\_`, "`", "\\`", "[", `\[`, "]", `\]`).Replace(text)
}
