package slack

import (
	"testing"

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
