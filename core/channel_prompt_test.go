package core

import (
	"strings"
	"testing"
)

func TestChannelMessageForAgentInjectsFeishuMetadata(t *testing.T) {
	msg := &Message{
		ID:           "om_1",
		ChatID:       "oc_1",
		ChatType:     "group",
		ChannelID:    "c1",
		UserID:       "ou_sender",
		Text:         "@机器人 帮我处理\n这个问题",
		MentionedBot: true,
		MentionAll:   false,
		Platform:     "feishu",
		Project:      "channel:c1",
	}

	got := channelMessageForAgent(Channel{Type: "feishu"}, msg)
	want := feishuMessagePromptIntro + "\n\n" + channelInteractionContract + `

{"message_id":"om_1","chat_id":"oc_1","chat_type":"group","channel_id":"c1","conversation_key":"root:om_1","delivery_cli":"amux","sender_open_id":"ou_sender","text":"@机器人 帮我处理\n这个问题","mentioned_bot":true,"mention_all":false,"platform":"feishu","project":"channel:c1"}`
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

func TestChannelDefaultMessagePromptMatchesRuntimePrefix(t *testing.T) {
	want := feishuMessagePromptIntro + "\n\n" + channelInteractionContract
	for _, channelType := range []string{"feishu", "lark"} {
		if got := ChannelDefaultMessagePrompt(Channel{Type: channelType}); got != want {
			t.Fatalf("%s default prompt = %q, want %q", channelType, got, want)
		}
	}
	if got := ChannelDefaultMessagePrompt(Channel{Type: "telegram"}); got != "" {
		t.Fatalf("telegram default prompt = %q, want empty", got)
	}
}

func TestChannelInteractionContractRequiresSplitFlow(t *testing.T) {
	for _, want := range []string{"禁止在 shell 前台阻塞", "request_user_input", "已完成", "<delivery_cli> send --channel-id", "统一 turn 超时"} {
		if !strings.Contains(channelInteractionContract, want) {
			t.Fatalf("channel interaction contract missing %q", want)
		}
	}
}

func TestChannelDeliveryCLIPathForDesktopAndDaemon(t *testing.T) {
	desktopExecutable := "/Applications/AgentMux.app/Contents/MacOS/AgentMux"
	wantDesktop := "/Applications/AgentMux.app/Contents/Resources/agentmux-remote/amux-darwin-arm64"
	gotDesktop := channelDeliveryCLIPathFor(desktopExecutable, "darwin", "arm64", func(path string) bool {
		return path == wantDesktop
	})
	if gotDesktop != wantDesktop {
		t.Fatalf("desktop delivery CLI = %q, want %q", gotDesktop, wantDesktop)
	}
	if got := channelDeliveryCLIPathFor("/home/tiger/.agentmux/bin/amux", "linux", "amd64", func(string) bool { return false }); got != "/home/tiger/.agentmux/bin/amux" {
		t.Fatalf("daemon delivery CLI = %q", got)
	}
}
