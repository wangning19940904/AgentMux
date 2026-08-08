// Package settingsui builds a platform-neutral intermediate representation of
// the runtime-settings picker (scope switch + model/effort/tier/approval
// selects). Each IM adapter renders these groups with its native widgets
// (inline keyboards, blocks, select menus, cards) instead of re-deriving the
// structure from RuntimeSettingsPickerState.
package settingsui

import (
	"strings"

	"github.com/wangning19940904/AgentMux/core"
)

// Option is one selectable value with its ready-made action payload.
type Option struct {
	Label    string
	Selected bool
	Action   core.RuntimeSettingsAction
}

// Group is one logical select of the picker.
type Group struct {
	ID      string // stable widget id: scope | model | effort | tier | approval
	Title   string
	Setting core.RuntimeSetting
	Options []Option
	// Unsupported carries the runtime's reason when this setting exists but
	// cannot be offered; Options is empty in that case.
	Unsupported string
}

// Groups derives the picker groups for a state: an optional scope switch
// followed by one group per setting that the active runtime can actually
// apply. Unsupported settings stay hidden instead of taking up card space.
func Groups(state core.RuntimeSettingsPickerState) []Group {
	groups := make([]Group, 0, 5)
	if state.AgentDefaultsEditable {
		groups = append(groups, Group{
			ID: "scope", Title: "设置范围", Setting: core.RuntimeSettingScope,
			Options: []Option{
				{
					Label: "当前会话（立即生效）", Selected: state.Scope == core.RuntimeSettingsScopeConversation,
					Action: core.RuntimeSettingsAction{Scope: core.RuntimeSettingsScopeConversation, Setting: core.RuntimeSettingScope},
				},
				{
					Label: "Agent 默认（仅新会话）", Selected: state.Scope == core.RuntimeSettingsScopeAgent,
					Action: core.RuntimeSettingsAction{Scope: core.RuntimeSettingsScopeAgent, Setting: core.RuntimeSettingScope},
				},
			},
		})
	}
	settings := []struct {
		id      string
		title   string
		setting core.RuntimeSetting
		options []core.RuntimeOption
	}{
		{"model", "模型", core.RuntimeSettingModel, state.Capabilities.Models},
		{"effort", "思考强度", core.RuntimeSettingReasoningEffort, state.Capabilities.ReasoningEfforts},
		{"tier", "速度", core.RuntimeSettingServiceTier, state.Capabilities.ServiceTiers},
		{"approval", "审批模式", core.RuntimeSettingApprovalMode, state.Capabilities.ApprovalModes},
	}
	for _, item := range settings {
		if len(item.options) == 0 {
			continue
		}
		selected := state.Settings.Value(item.setting)
		defaultValue := state.RuntimeDefaults.Value(item.setting)
		group := Group{ID: item.id, Title: item.title, Setting: item.setting}
		for _, option := range item.options {
			label := option.Label
			if label == "" {
				label = option.Value
			}
			if defaultValue != "" && option.Value == defaultValue {
				label += "（默认）"
			}
			group.Options = append(group.Options, Option{
				Label: label, Selected: option.Value == selected,
				Action: core.RuntimeSettingsAction{Scope: state.Scope, Setting: item.setting, Value: option.Value},
			})
		}
		groups = append(groups, group)
	}
	return groups
}

// Format controls the inline markup of SummaryText for a platform.
type Format struct {
	Bold func(string) string // nil renders plain
	Code func(string) string // nil renders plain
}

// SummaryText renders the picker header: title, scope and current settings.
// The caller appends state.Notice in its platform-preferred style.
func SummaryText(state core.RuntimeSettingsPickerState, format Format) string {
	bold := format.Bold
	if bold == nil {
		bold = func(s string) string { return s }
	}
	code := format.Code
	if code == nil {
		code = func(s string) string { return s }
	}
	var b strings.Builder
	b.WriteString(bold("运行时设置"))
	b.WriteString("\n范围：" + ScopeLabel(state.Scope))
	if len(state.Capabilities.Models) > 0 {
		b.WriteString("\n模型：" + code(Display(state.Settings.Model)))
	}
	if len(state.Capabilities.ReasoningEfforts) > 0 {
		b.WriteString("\n思考：" + code(Display(state.Settings.ReasoningEffort)))
	}
	if len(state.Capabilities.ServiceTiers) > 0 {
		b.WriteString("\n速度：" + code(Display(state.Settings.ServiceTier)))
	}
	if len(state.Capabilities.ApprovalModes) > 0 {
		b.WriteString("\n审批：" + code(Display(state.Settings.ApprovalMode)))
	}
	return b.String()
}

// ScopeLabel names a picker scope for humans.
func ScopeLabel(scope core.RuntimeSettingsScope) string {
	if scope == core.RuntimeSettingsScopeAgent {
		return "Agent 默认（仅新会话）"
	}
	return "当前会话（立即生效）"
}

// Display substitutes the runtime-default placeholder for blank values.
func Display(value string) string {
	if strings.TrimSpace(value) == "" {
		return "runtime default"
	}
	return value
}
