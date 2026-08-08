package discord

import (
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/wangning19940904/AgentMux/core"
)

func TestRuntimeSettingsComponentsExposeNativeSelects(t *testing.T) {
	components := discordRuntimeSettingsComponents(core.RuntimeSettingsPickerState{
		Scope:                 core.RuntimeSettingsScopeAgent,
		Settings:              core.RuntimeSettings{Model: "gpt-5", ReasoningEffort: "high", ServiceTier: "priority", ApprovalMode: core.ApprovalModeManual},
		AgentDefaultsEditable: true,
		Capabilities: core.RuntimeSettingsCapabilities{
			Models:           []core.RuntimeOption{{Value: "gpt-5"}},
			ReasoningEfforts: []core.RuntimeOption{{Value: "high"}},
			ServiceTiers:     []core.RuntimeOption{{Value: "priority"}},
			ApprovalModes:    []core.RuntimeOption{{Value: core.ApprovalModeManual}},
		},
	})
	if len(components) != 5 { // scope + model + effort + tier + approval
		t.Fatalf("component rows = %d, want 5", len(components))
	}
}

func TestHelpComponentsExposeCommandButtons(t *testing.T) {
	state := core.HelpCardState{
		AgentName: "代码助手", RuntimeName: "codex", Introduction: "你好，我是代码助手。",
		Commands: []core.HelpCommand{
			{Command: "/model", Description: "切换模型", Actionable: true},
			{Command: "/clear", Description: "清除上下文", Actionable: true},
			{Command: "/effort", Description: "切换思考强度"},
		},
	}
	if text := discordHelpText(state); !strings.Contains(text, "代码助手") || !strings.Contains(text, "/effort") {
		t.Fatalf("help text = %q", text)
	}
	rows := discordHelpComponents(state)
	if len(rows) != 1 {
		t.Fatalf("help rows = %d, want 1", len(rows))
	}
	row, ok := rows[0].(discordgo.ActionsRow)
	if !ok || len(row.Components) != 2 {
		t.Fatalf("help row = %#v", rows[0])
	}
	model := row.Components[0].(discordgo.Button)
	clear := row.Components[1].(discordgo.Button)
	if model.CustomID != "agentmux_help_model" || model.Style != discordgo.PrimaryButton ||
		clear.CustomID != "agentmux_help_clear" || clear.Style != discordgo.DangerButton {
		t.Fatalf("help buttons = %#v", row.Components)
	}
}
