package discord

import (
	"testing"

	"github.com/agentnexus/agentnexus/core"
)

func TestRuntimeSettingsComponentsExposeNativeSelects(t *testing.T) {
	components := discordRuntimeSettingsComponents(core.RuntimeSettingsPickerState{
		Scope:                 core.RuntimeSettingsScopeAgent,
		Settings:              core.RuntimeSettings{Model: "gpt-5", ReasoningEffort: "high", ServiceTier: "priority"},
		AgentDefaultsEditable: true,
		Capabilities: core.RuntimeSettingsCapabilities{
			Models:           []core.RuntimeOption{{Value: "gpt-5"}},
			ReasoningEfforts: []core.RuntimeOption{{Value: "high"}},
			ServiceTiers:     []core.RuntimeOption{{Value: "priority"}},
		},
	})
	if len(components) != 4 { // scope + model + effort + tier
		t.Fatalf("component rows = %d, want 4", len(components))
	}
}
