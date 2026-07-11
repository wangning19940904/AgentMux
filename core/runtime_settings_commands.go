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
	default:
		return runtimeSettingsCommand{}, false
	}
	if len(fields) == 1 || fields[1] == "list" || fields[1] == "current" {
		return runtimeSettingsCommand{Setting: setting, List: true}, true
	}
	if fields[1] == "reset" {
		return runtimeSettingsCommand{Setting: setting, Reset: true}, true
	}
	value := fields[1]
	if setting == RuntimeSettingServiceTier {
		resolved, reset := resolveFastCommand(value)
		if reset {
			return runtimeSettingsCommand{Setting: setting, Reset: true}, true
		}
		value = resolved
	}
	return runtimeSettingsCommand{Setting: setting, Value: value}, true
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
	caps := settings.RuntimeSettingsCapabilities()
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
	return RuntimeSettingsPickerState{
		Scope:                 scope,
		Settings:              current,
		RuntimeDefaults:       settings.DefaultRuntimeSettings(),
		Capabilities:          caps,
		AgentDefaultsEditable: agentEditable,
		Unsupported:           unsupported,
	}
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
		if err := ValidateRuntimeSetting(settings.RuntimeSettingsCapabilities(), action.Setting, action.Value); err != nil {
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
