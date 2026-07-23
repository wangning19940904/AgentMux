package core

import "encoding/json"

const feishuMessagePromptIntro = "请处理以下来自飞书/Lark 渠道的消息。JSON 中的 text 是用户输入，其余字段是消息元数据。"

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
	if msg == nil || !isFeishuLikeChannel(ch.Type) {
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
	agentMsg.Text = feishuMessagePromptIntro + "\n\n" + string(payload)
	return &agentMsg
}
