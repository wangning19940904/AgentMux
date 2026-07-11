package slack

import (
	"testing"

	"github.com/agentnexus/agentnexus/core"
)

func TestRuntimeSettingsBlocksExposeNativeSelections(t *testing.T) {
	blocks := slackRuntimeSettingsBlocks(core.RuntimeSettingsPickerState{
		Scope:                 core.RuntimeSettingsScopeConversation,
		Settings:              core.RuntimeSettings{Model: "gpt-5", ReasoningEffort: "high", ServiceTier: "priority"},
		AgentDefaultsEditable: true,
		Capabilities: core.RuntimeSettingsCapabilities{
			Models:           []core.RuntimeOption{{Value: "gpt-5"}},
			ReasoningEfforts: []core.RuntimeOption{{Value: "high"}},
			ServiceTiers:     []core.RuntimeOption{{Value: "priority"}},
		},
	})
	if len(blocks) != 5 { // summary + scope + model + effort + tier
		t.Fatalf("block count = %d, want 5", len(blocks))
	}
}
