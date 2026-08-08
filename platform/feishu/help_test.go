package feishu

import (
	"strings"
	"testing"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"github.com/wangning19940904/AgentMux/core"
)

func TestHelpCardContainsIntroductionCommandsAndButtons(t *testing.T) {
	card := buildHelpCard(&core.Message{ChatID: "oc_1", ChatType: "group"}, core.HelpCardState{
		AgentName: "代码助手", RuntimeName: "codex", Introduction: "你好，我是代码助手。",
		Commands: []core.HelpCommand{
			{Command: "/model", Description: "切换模型", Actionable: true},
			{Command: "/clear", Description: "清除上下文", Actionable: true},
			{Command: "/queue <内容>", Description: "加入队列"},
		},
	})
	for _, want := range []string{
		`"schema":"2.0"`, "代码助手 · 帮助", "你好，我是代码助手。", `/queue \u003c内容\u003e`,
		`"agentmux_action":"help_command"`, `"command":"/model"`, `"command":"/clear"`,
	} {
		if !strings.Contains(card, want) {
			t.Fatalf("help card missing %q: %s", want, card)
		}
	}
}

func TestHelpCardButtonBecomesCommandMessage(t *testing.T) {
	client := &larkClient{platform: "feishu"}
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "ou_actor"},
		Context:  &callback.Context{OpenChatID: "fallback_chat", OpenMessageID: "om_help"},
		Action: &callback.CallBackAction{Tag: "button", Value: map[string]interface{}{
			modelPickerActionKey: helpCommandAction,
			"command":            "/clear",
			"chat_id":            "oc_1",
			"chat_type":          "group",
			"conversation_key":   "thread:one",
		}},
	}}
	msg, ok := client.messageFromCardAction("channel:c1", event)
	if !ok || msg == nil || msg.LogOnly {
		t.Fatalf("help action was not recognized: %+v", msg)
	}
	if msg.Text != "/clear" || msg.ChatID != "oc_1" || msg.ChatType != "group" || msg.ConversationKey != "thread:one" {
		t.Fatalf("help action message = %+v", msg)
	}
}

func TestHelpCardRejectsUnknownCallbackCommand(t *testing.T) {
	client := &larkClient{platform: "feishu"}
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Context: &callback.Context{OpenChatID: "oc_1"},
		Action: &callback.CallBackAction{Value: map[string]interface{}{
			modelPickerActionKey: helpCommandAction,
			"command":            "write arbitrary files",
		}},
	}}
	msg, ok := client.messageFromCardAction("channel:c1", event)
	if !ok || msg == nil || !msg.LogOnly || msg.Text != "" {
		t.Fatalf("unknown help callback was dispatched: %+v", msg)
	}
}
