package core

import (
	"fmt"
	"strings"
)

type runtimeSettingsCommand struct {
	Setting RuntimeSetting
	Value   string
	List    bool
	Reset   bool
}

func parseRuntimeSettingsCommand(text string) (runtimeSettingsCommand, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return runtimeSettingsCommand{}, false
	}
	var setting RuntimeSetting
	switch fields[0] {
	case "/model":
		setting = RuntimeSettingModel
	case "/effort":
		setting = RuntimeSettingReasoningEffort
	case "/fast":
		setting = RuntimeSettingServiceTier
	case "/approval", "/permissions", "/yolo":
		setting = RuntimeSettingApprovalMode
	default:
		return runtimeSettingsCommand{}, false
	}
	if len(fields) == 1 || fields[1] == "list" || fields[1] == "current" || fields[1] == "status" || fields[1] == "help" {
		return runtimeSettingsCommand{Setting: setting, List: true}, true
	}
	if fields[1] == "reset" {
		return runtimeSettingsCommand{Setting: setting, Reset: true}, true
	}
	value := fields[1]
	if setting == RuntimeSettingApprovalMode {
		value = strings.ToLower(value)
	}
	if fields[0] == "/yolo" {
		switch value {
		case "on", "enable", "enabled":
			value = ApprovalModeYolo
		case "off", "disable", "disabled":
			value = ApprovalModeManual
		}
	}
	if setting == RuntimeSettingServiceTier {
		resolved, reset := resolveFastCommand(value)
		if reset {
			return runtimeSettingsCommand{Setting: setting, Reset: true}, true
		}
		value = resolved
	}
	return runtimeSettingsCommand{Setting: setting, Value: value}, true
}

func formatApprovalModeCommandResult(settings RuntimeSettingsSession, reset bool) string {
	current := settings.CurrentRuntimeSettings().ApprovalMode
	label := runtimeSettingDisplay(current)
	if current != "" {
		label = runtimeOptionLabel(current) + "（" + current + "）"
	}
	action := "审批模式已切换为："
	if reset {
		action = "审批模式已恢复为默认值："
	}
	lines := []string{action + label, "作用范围：当前会话"}
	if current == ApprovalModeYolo {
		lines = append(lines, "⚠️ 当前会话将跳过审批并使用该运行时提供的最高权限。", "恢复手动审批：/yolo off")
	} else {
		lines = append(lines, "切换完全免审批：/yolo on")
	}
	return strings.Join(lines, "\n")
}

func formatApprovalModeCommandError(err error, settings RuntimeSettingsSession) string {
	options := runtimeOptionValues(settings.RuntimeSettingsCapabilities().ApprovalModes)
	text := "无法切换审批模式：" + err.Error()
	if len(options) > 0 {
		text += "\n可用模式：" + strings.Join(options, ", ")
	}
	return text + "\n用法：/approval <模式>"
}

func resolveFastCommand(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "off", "normal", "default", "reset":
		return "", true
	case "on", "fast":
		return "priority", false
	default:
		return value, false
	}
}

func formatRuntimeSettingsStatus(settings RuntimeSettingsSession) string {
	current := settings.CurrentRuntimeSettings()
	defaults := settings.DefaultRuntimeSettings()
	caps := settings.RuntimeSettingsCapabilities()
	lines := []string{
		"Current model: " + runtimeSettingDisplay(current.Model),
		"Default model: " + runtimeSettingDisplay(defaults.Model),
	}
	if len(caps.ReasoningEfforts) > 0 {
		lines = append(lines,
			"Thinking effort: "+runtimeSettingDisplay(current.ReasoningEffort),
			"Default effort: "+runtimeSettingDisplay(defaults.ReasoningEffort),
		)
	}
	if len(caps.ServiceTiers) > 0 {
		lines = append(lines,
			"Speed: "+runtimeSettingDisplay(current.ServiceTier),
			"Default speed: "+runtimeSettingDisplay(defaults.ServiceTier),
		)
	}
	if len(caps.ApprovalModes) > 0 {
		lines = append(lines,
			"Approval mode: "+runtimeSettingDisplay(current.ApprovalMode),
			"Default approval: "+runtimeSettingDisplay(defaults.ApprovalMode),
		)
	}
	sections := []string{strings.Join(lines, "\n")}
	if len(caps.Models) > 0 {
		sections = append(sections, "Available models:\n- "+strings.Join(runtimeOptionValues(caps.Models), "\n- "))
	}
	if len(caps.ReasoningEfforts) > 0 {
		sections = append(sections, "Thinking strengths: "+strings.Join(runtimeOptionValues(caps.ReasoningEfforts), ", "))
	}
	if len(caps.ServiceTiers) > 0 {
		sections = append(sections, "Speed modes: "+strings.Join(runtimeOptionValues(caps.ServiceTiers), ", "))
	}
	if len(caps.ApprovalModes) > 0 {
		sections = append(sections, "Approval modes: "+strings.Join(runtimeOptionValues(caps.ApprovalModes), ", "))
	}
	return strings.Join(sections, "\n\n")
}

func runtimeSettingsPickerState(settings RuntimeSettingsSession, scope RuntimeSettingsScope, agentDefaults RuntimeSettings, agentEditable bool) RuntimeSettingsPickerState {
	if scope == "" {
		scope = RuntimeSettingsScopeConversation
	}
	current := settings.CurrentRuntimeSettings()
	if scope == RuntimeSettingsScopeAgent {
		current = agentDefaults
	}
	current, caps := RuntimeSettingsView(settings, current)
	unsupported := map[RuntimeSetting]string{}
	if len(caps.Models) == 0 {
		unsupported[RuntimeSettingModel] = "当前运行时未提供可选模型"
	}
	if len(caps.ReasoningEfforts) == 0 {
		unsupported[RuntimeSettingReasoningEffort] = "当前运行时不支持思考强度"
	}
	if len(caps.ServiceTiers) == 0 {
		unsupported[RuntimeSettingServiceTier] = "当前运行时不支持快速模式"
	}
	if len(caps.ApprovalModes) == 0 {
		unsupported[RuntimeSettingApprovalMode] = "当前运行时不支持审批模式切换"
	}
	state := RuntimeSettingsPickerState{
		Scope:                 scope,
		Settings:              current,
		RuntimeDefaults:       settings.DefaultRuntimeSettings(),
		Capabilities:          caps,
		AgentDefaultsEditable: agentEditable,
		Unsupported:           unsupported,
	}
	if current.ApprovalMode == ApprovalModeManual {
		if _, interactive := settings.(InteractiveAgentSession); !interactive && len(caps.ApprovalModes) > 0 {
			state.Hint = "当前运行时不能在渠道中暂停并逐次审批；手动模式会拦截工具。需要执行工具时请选择智能自动审批或 YOLO。"
		}
	}
	return state
}

func applyRuntimeSettingsAction(settings RuntimeSettingsSession, action RuntimeSettingsAction, defaults *RuntimeSettings) error {
	if action.Setting == RuntimeSettingScope {
		return nil
	}
	if action.Scope == RuntimeSettingsScopeAgent {
		if defaults == nil {
			return fmt.Errorf("Agent defaults are not editable here")
		}
		if action.Reset {
			defaults.Set(action.Setting, "")
			return nil
		}
		_, capabilities := RuntimeSettingsView(settings, *defaults)
		// A model change is always validated against the runtime's full model
		// catalog; other settings use the model-refined view so fixed dimensions
		// cannot be changed independently.
		if action.Setting == RuntimeSettingModel {
			capabilities = settings.RuntimeSettingsCapabilities()
		}
		if err := ValidateRuntimeSetting(capabilities, action.Setting, action.Value); err != nil {
			return err
		}
		defaults.Set(action.Setting, action.Value)
		return nil
	}
	if action.Reset {
		return settings.ResetRuntimeSetting(action.Setting)
	}
	return settings.SetRuntimeSetting(action.Setting, action.Value)
}

func runtimeSettingDisplay(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(runtime default)"
	}
	return value
}

func runtimeOptionValues(options []RuntimeOption) []string {
	values := make([]string, 0, len(options))
	for _, option := range options {
		values = append(values, option.Value)
	}
	return values
}
