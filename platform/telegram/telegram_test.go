package telegram

import (
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

func TestRuntimeSettingsKeyboardStoresCallbackActions(t *testing.T) {
	p := &Platform{}
	rows := telegramRuntimeSettingsKeyboard(p, core.RuntimeSettingsPickerState{
		Scope:        core.RuntimeSettingsScopeConversation,
		Settings:     core.RuntimeSettings{Model: "gpt-5"},
		Capabilities: core.RuntimeSettingsCapabilities{Models: []core.RuntimeOption{{Value: "gpt-5"}, {Value: "gpt-5-mini"}}},
	})
	if len(rows) == 0 || len(rows[0]) == 0 {
		t.Fatal("expected inline keyboard rows")
	}
	token := rows[0][0].CallbackData
	if len(token) < 4 || token[:3] != "rs:" {
		t.Fatalf("callback data = %q", token)
	}
	p.mu.Lock()
	_, ok := p.pickerActions[token[3:]]
	p.mu.Unlock()
	if !ok {
		t.Fatal("keyboard callback action was not registered")
	}
}

func TestHelpKeyboardExposesCommandCallbacks(t *testing.T) {
	state := core.HelpCardState{
		AgentName: "代码助手", RuntimeName: "codex", Introduction: "你好，我是代码助手。",
		Commands: []core.HelpCommand{
			{Command: "/model", Description: "切换模型", Actionable: true},
			{Command: "/clear", Description: "清除上下文", Actionable: true},
			{Command: "/effort", Description: "切换思考强度"},
		},
	}
	rows := telegramHelpKeyboard(state)
	if len(rows) != 1 || len(rows[0]) != 2 {
		t.Fatalf("help keyboard = %+v", rows)
	}
	if rows[0][0].CallbackData != "hc:model" || rows[0][1].CallbackData != "hc:clear" {
		t.Fatalf("help callback data = %+v", rows[0])
	}
}
