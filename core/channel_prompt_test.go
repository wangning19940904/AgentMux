package core

import "testing"

func TestChannelMessageForAgentInjectsFeishuMetadata(t *testing.T) {
	msg := &Message{
		ID:           "om_1",
		ChatID:       "oc_1",
		ChatType:     "group",
		UserID:       "ou_sender",
		Text:         "@机器人 帮我处理\n这个问题",
		MentionedBot: true,
		MentionAll:   false,
		Platform:     "feishu",
		Project:      "channel:c1",
	}

	got := channelMessageForAgent(Channel{Type: "feishu"}, msg)
	want := feishuMessagePromptIntro + `

{"message_id":"om_1","chat_id":"oc_1","chat_type":"group","sender_open_id":"ou_sender","text":"@机器人 帮我处理\n这个问题","mentioned_bot":true,"mention_all":false,"platform":"feishu","project":"channel:c1"}`
	if got == msg {
		t.Fatal("Feishu Agent message must be a copy")
	}
	if got.Text != want {
		t.Fatalf("Agent prompt = %q, want %q", got.Text, want)
	}
	if msg.Text != "@机器人 帮我处理\n这个问题" {
		t.Fatalf("original message text was mutated: %q", msg.Text)
	}
}

func TestChannelMessageForAgentLeavesOtherChannelsUnchanged(t *testing.T) {
	msg := &Message{Text: "hello", Platform: "telegram"}
	if got := channelMessageForAgent(Channel{Type: "telegram"}, msg); got != msg {
		t.Fatalf("non-Feishu message was copied or changed: %+v", got)
	}
}
