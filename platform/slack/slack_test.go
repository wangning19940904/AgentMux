package slack

import (
	"strings"
	"testing"

	slackapi "github.com/slack-go/slack"
	"github.com/wangning19940904/AgentMux/core"
)

func TestRuntimeSettingsBlocksExposeNativeSelections(t *testing.T) {
	blocks := slackRuntimeSettingsBlocks(core.RuntimeSettingsPickerState{
		Scope:                 core.RuntimeSettingsScopeConversation,
		Settings:              core.RuntimeSettings{Model: "gpt-5", ReasoningEffort: "high", ServiceTier: "priority", ApprovalMode: core.ApprovalModeManual},
		AgentDefaultsEditable: true,
		Capabilities: core.RuntimeSettingsCapabilities{
			Models:           []core.RuntimeOption{{Value: "gpt-5"}},
			ReasoningEfforts: []core.RuntimeOption{{Value: "high"}},
			ServiceTiers:     []core.RuntimeOption{{Value: "priority"}},
			ApprovalModes:    []core.RuntimeOption{{Value: core.ApprovalModeManual}},
		},
	})
	if len(blocks) != 6 { // summary + scope + model + effort + tier + approval
		t.Fatalf("block count = %d, want 6", len(blocks))
	}
}

func TestHelpBlocksExposeCommandButtons(t *testing.T) {
	state := core.HelpCardState{
		AgentName: "代码助手", RuntimeName: "codex", Introduction: "你好，我是代码助手。",
		Commands: []core.HelpCommand{
			{Command: "/model", Description: "切换模型", Actionable: true},
			{Command: "/clear", Description: "清除上下文", Actionable: true},
			{Command: "/effort", Description: "切换思考强度"},
		},
	}
	blocks := slackHelpBlocks(state)
	if len(blocks) != 3 {
		t.Fatalf("help block count = %d, want 3", len(blocks))
	}
	actions, ok := blocks[2].(*slackapi.ActionBlock)
	if !ok || actions.Elements == nil || len(actions.Elements.ElementSet) != 2 {
		t.Fatalf("help actions = %#v", blocks[2])
	}
	var values []string
	for _, element := range actions.Elements.ElementSet {
		button, ok := element.(*slackapi.ButtonBlockElement)
		if !ok {
			t.Fatalf("help action is %T", element)
		}
		values = append(values, button.Value)
	}
	if got := strings.Join(values, ","); got != "/model,/clear" {
		t.Fatalf("help button values = %q", got)
	}
}
