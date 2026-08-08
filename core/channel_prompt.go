package core

import "encoding/json"

const feishuMessagePromptIntro = "请处理以下来自飞书/Lark 渠道的消息。JSON 中的 text 是用户输入，其余字段是消息元数据。"

const channelInteractionContract = `渠道执行约束：
- 当前请求来自异步消息渠道，无法给终端命令提供实时 stdin/TTY；工具中间输出、本地文件路径和后台进程状态不会自动发送给用户。
- 任何需要用户扫码、打开链接、授权、输入验证码或确认后才能继续的命令，都必须拆成多个 turn，禁止在当前 turn 前台阻塞等待用户。
- 优先使用 --no-wait、--json、device-flow 等非阻塞接口。若工具只提供阻塞命令，必须以有界超时的后台进程运行并完整重定向 stdin/stdout/stderr；读取到 URL/验证码后立即结束当前 turn。
- 当前 turn 的最终回复必须包含未经改写的操作链接及验证码；如生成二维码或文件，必须通过渠道可访问的上传/发送能力交付，不能只回复本地路径。明确请用户完成后再回复，然后交还控制权。
- 用户在后续消息中表示“已完成”后，再检查状态或执行第二阶段；不要重复启动同一个初始化/登录流程。
- 例如：lark-cli config init --new 必须后台启动并在取得链接后立即回复；lark-cli auth login 应优先使用 --no-wait --json，并在下一个 turn 完成轮询。`

// ChannelDefaultMessagePrompt returns the static prefix AgentMux injects in
// front of every inbound message for channels that need structured routing or
// execution guidance. The console uses this same value for prompt previews so
// displayed defaults cannot drift from runtime behavior.
func ChannelDefaultMessagePrompt(ch Channel) string {
	if !isFeishuLikeChannel(ch.Type) {
		return ""
	}
	return feishuMessagePromptIntro + "\n\n" + channelInteractionContract
}

type feishuMessagePrompt struct {
	MessageID    string `json:"message_id"`
	ChatID       string `json:"chat_id"`
	ChatType     string `json:"chat_type"`
	SenderOpenID string `json:"sender_open_id"`
	Text         string `json:"text"`
	MentionedBot bool   `json:"mentioned_bot"`
	MentionAll   bool   `json:"mention_all"`
	Platform     string `json:"platform"`
	Project      string `json:"project"`
}

// channelMessageForAgent returns the message submitted to the bound Agent.
// Feishu/Lark turns carry their routing and sender metadata in a structured
// prompt; other channel types keep their existing plain-text behavior.
func channelMessageForAgent(ch Channel, msg *Message) *Message {
	if msg == nil {
		return msg
	}
	prompt := ChannelDefaultMessagePrompt(ch)
	if prompt == "" {
		return msg
	}

	payload, err := json.Marshal(feishuMessagePrompt{
		MessageID:    msg.ID,
		ChatID:       msg.ChatID,
		ChatType:     msg.ChatType,
		SenderOpenID: msg.UserID,
		Text:         msg.Text,
		MentionedBot: msg.MentionedBot,
		MentionAll:   msg.MentionAll,
		Platform:     msg.Platform,
		Project:      msg.Project,
	})
	if err != nil {
		return msg
	}

	agentMsg := *msg
	agentMsg.Text = prompt + "\n\n" + string(payload)
	return &agentMsg
}
