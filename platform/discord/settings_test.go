package discord

import (
	"testing"

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
