package settingsui

import (
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

func TestGroupsHideUnsupportedSettingsAndExplainScope(t *testing.T) {
	state := core.RuntimeSettingsPickerState{
		Scope:                 core.RuntimeSettingsScopeConversation,
		AgentDefaultsEditable: true,
		Settings:              core.RuntimeSettings{Model: "gpt-5"},
		RuntimeDefaults:       core.RuntimeSettings{Model: "gpt-5"},
		Capabilities:          core.RuntimeSettingsCapabilities{Models: []core.RuntimeOption{{Value: "gpt-5", Label: "GPT-5"}}},
		Unsupported: map[core.RuntimeSetting]string{
			core.RuntimeSettingReasoningEffort: "unsupported",
			core.RuntimeSettingServiceTier:     "unsupported",
		},
	}
	groups := Groups(state)
	if len(groups) != 2 {
		t.Fatalf("groups = %#v, want scope + model only", groups)
	}
	if !strings.Contains(groups[0].Options[0].Label, "立即生效") || !strings.Contains(groups[0].Options[1].Label, "仅新会话") {
		t.Fatalf("scope labels do not explain effect: %#v", groups[0].Options)
	}
	if got := groups[1].Options[0].Label; got != "GPT-5（默认）" {
		t.Fatalf("default model label = %q", got)
	}
}

func TestSummaryTextOmitsUnsupportedSettings(t *testing.T) {
	text := SummaryText(core.RuntimeSettingsPickerState{
		Scope:        core.RuntimeSettingsScopeConversation,
		Settings:     core.RuntimeSettings{Model: "gpt-5", ReasoningEffort: "high", ServiceTier: "priority"},
		Capabilities: core.RuntimeSettingsCapabilities{Models: []core.RuntimeOption{{Value: "gpt-5"}}},
	}, Format{})
	if !strings.Contains(text, "模型：gpt-5") || strings.Contains(text, "思考：") || strings.Contains(text, "速度：") || strings.Contains(text, "审批：") {
		t.Fatalf("summary exposed unsupported settings: %q", text)
	}
}
