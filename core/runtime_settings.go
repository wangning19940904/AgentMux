package core

import (
	"fmt"
	"strings"
	"sync"
)

// RuntimeSetting identifies one user-selectable setting of a coding runtime.
type RuntimeSetting string

const (
	RuntimeSettingModel           RuntimeSetting = "model"
	RuntimeSettingReasoningEffort RuntimeSetting = "reasoning_effort"
	RuntimeSettingServiceTier     RuntimeSetting = "service_tier"
	RuntimeSettingApprovalMode    RuntimeSetting = "approval_mode"
	RuntimeSettingScope           RuntimeSetting = "scope"
)

const (
	ApprovalModeManual   = "manual"
	ApprovalModeAutoEdit = "auto_edit"
	ApprovalModeAuto     = "auto"
	ApprovalModePlan     = "plan"
	ApprovalModeYolo     = "yolo"
)

// RuntimeSettings holds the values that can change the behavior of a turn.
// Empty values deliberately mean "let the next lower precedence layer decide".
type RuntimeSettings struct {
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	ServiceTier     string `json:"service_tier,omitempty"`
	ApprovalMode    string `json:"approval_mode,omitempty"`
}

func (s RuntimeSettings) Value(setting RuntimeSetting) string {
	switch setting {
	case RuntimeSettingModel:
		return s.Model
	case RuntimeSettingReasoningEffort:
		return s.ReasoningEffort
	case RuntimeSettingServiceTier:
		return s.ServiceTier
	case RuntimeSettingApprovalMode:
		return s.ApprovalMode
	default:
		return ""
	}
}

func (s *RuntimeSettings) Set(setting RuntimeSetting, value string) {
	value = strings.TrimSpace(value)
	switch setting {
	case RuntimeSettingModel:
		s.Model = value
	case RuntimeSettingReasoningEffort:
		s.ReasoningEffort = value
	case RuntimeSettingServiceTier:
		s.ServiceTier = value
	case RuntimeSettingApprovalMode:
		s.ApprovalMode = value
	}
}

// RuntimeOption describes an exact value accepted by a runtime. Label is
// optional; platforms fall back to Value when the adapter does not provide one.
type RuntimeOption struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
}

// RuntimeSettingsCapabilities lists the controls an adapter can truthfully
// expose. A missing list is intentionally hidden rather than inferred.
type RuntimeSettingsCapabilities struct {
	Models           []RuntimeOption `json:"models,omitempty"`
	ReasoningEfforts []RuntimeOption `json:"reasoning_efforts,omitempty"`
	ServiceTiers     []RuntimeOption `json:"service_tiers,omitempty"`
	ApprovalModes    []RuntimeOption `json:"approval_modes,omitempty"`
}

func (c RuntimeSettingsCapabilities) Options(setting RuntimeSetting) []RuntimeOption {
	switch setting {
	case RuntimeSettingModel:
		return append([]RuntimeOption(nil), c.Models...)
	case RuntimeSettingReasoningEffort:
		return append([]RuntimeOption(nil), c.ReasoningEfforts...)
	case RuntimeSettingServiceTier:
		return append([]RuntimeOption(nil), c.ServiceTiers...)
	case RuntimeSettingApprovalMode:
		return append([]RuntimeOption(nil), c.ApprovalModes...)
	default:
		return nil
	}
}

func (c RuntimeSettingsCapabilities) Supports(setting RuntimeSetting) bool {
	return len(c.Options(setting)) > 0
}

// RuntimeSettingsSession is an optional AgentSession capability for runtimes
// that expose per-conversation model, effort, or service-tier settings.
type RuntimeSettingsSession interface {
	RuntimeSettingsCapabilities() RuntimeSettingsCapabilities
	CurrentRuntimeSettings() RuntimeSettings
	DefaultRuntimeSettings() RuntimeSettings
	SetRuntimeSetting(setting RuntimeSetting, value string) error
	ResetRuntimeSetting(setting RuntimeSetting) error
}

// RuntimeSettingsSelection is the reusable thread-safe state holder used by
// CLI adapters. Defaults come from the Agent instance; overrides live only for
// the current conversation.
type RuntimeSettingsSelection struct {
	mu           sync.Mutex
	defaults     RuntimeSettings
	overrides    RuntimeSettings
	capabilities RuntimeSettingsCapabilities
}

func NewRuntimeSettingsSelection(defaults RuntimeSettings, capabilities RuntimeSettingsCapabilities) *RuntimeSettingsSelection {
	return &RuntimeSettingsSelection{
		defaults:     normalizeRuntimeSettings(defaults),
		capabilities: normalizeRuntimeCapabilities(capabilities),
	}
}

// RuntimeSettingsSelectionFromConfig builds session settings from the shared
// agent configuration map used by CLI and SDK adapters.
func RuntimeSettingsSelectionFromConfig(cfg map[string]any) *RuntimeSettingsSelection {
	defaults := RuntimeSettings{}
	if value, ok := cfg["model"].(string); ok {
		defaults.Model = value
	}
	if value, ok := cfg["reasoning_effort"].(string); ok {
		defaults.ReasoningEffort = value
	}
	if value, ok := cfg["service_tier"].(string); ok {
		defaults.ServiceTier = value
	}
	if value, ok := cfg["approval_mode"].(string); ok {
		defaults.ApprovalMode = value
	}
	return NewRuntimeSettingsSelection(defaults, RuntimeSettingsCapabilities{
		Models:           RuntimeOptions(runtimeSettingValues(cfg["supported_models"])),
		ReasoningEfforts: RuntimeOptions(runtimeSettingValues(cfg["supported_reasoning_efforts"])),
		ServiceTiers:     RuntimeOptions(runtimeSettingValues(cfg["supported_service_tiers"])),
		ApprovalModes:    RuntimeOptions(runtimeSettingValues(cfg["supported_approval_modes"])),
	})
}

func runtimeSettingValues(raw any) []string {
	switch values := raw.(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func (s *RuntimeSettingsSelection) RuntimeSettingsCapabilities() RuntimeSettingsCapabilities {
	if s == nil {
		return RuntimeSettingsCapabilities{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return copyRuntimeCapabilities(s.capabilities)
}

func (s *RuntimeSettingsSelection) CurrentRuntimeSettings() RuntimeSettings {
	if s == nil {
		return RuntimeSettings{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return mergeRuntimeSettings(s.defaults, s.overrides)
}

func (s *RuntimeSettingsSelection) DefaultRuntimeSettings() RuntimeSettings {
	if s == nil {
		return RuntimeSettings{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.defaults
}

func (s *RuntimeSettingsSelection) SetRuntimeSetting(setting RuntimeSetting, value string) error {
	if s == nil {
		return fmt.Errorf("runtime settings are not supported")
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%s is required", setting)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !runtimeOptionContains(s.capabilities.Options(setting), value) {
		if len(s.capabilities.Options(setting)) == 0 {
			return fmt.Errorf("%s is not supported by this runtime", runtimeSettingLabel(setting))
		}
		return fmt.Errorf("%q is not supported for %s", value, runtimeSettingLabel(setting))
	}
	s.overrides.Set(setting, value)
	return nil
}

func (s *RuntimeSettingsSelection) ResetRuntimeSetting(setting RuntimeSetting) error {
	if s == nil {
		return fmt.Errorf("runtime settings are not supported")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.capabilities.Options(setting)) == 0 {
		return fmt.Errorf("%s is not supported by this runtime", runtimeSettingLabel(setting))
	}
	s.overrides.Set(setting, "")
	return nil
}

func mergeRuntimeSettings(defaults, overrides RuntimeSettings) RuntimeSettings {
	current := defaults
	if overrides.Model != "" {
		current.Model = overrides.Model
	}
	if overrides.ReasoningEffort != "" {
		current.ReasoningEffort = overrides.ReasoningEffort
	}
	if overrides.ServiceTier != "" {
		current.ServiceTier = overrides.ServiceTier
	}
	if overrides.ApprovalMode != "" {
		current.ApprovalMode = overrides.ApprovalMode
	}
	return current
}

func RuntimeOptions(values []string) []RuntimeOption {
	seen := map[string]bool{}
	options := make([]RuntimeOption, 0, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		options = append(options, RuntimeOption{Value: value, Label: runtimeOptionLabel(value)})
	}
	return options
}

func runtimeOptionContains(options []RuntimeOption, value string) bool {
	for _, option := range options {
		if option.Value == value {
			return true
		}
	}
	return false
}

// ValidateRuntimeSetting checks a requested value against the capabilities
// currently advertised by the active runtime.
func ValidateRuntimeSetting(capabilities RuntimeSettingsCapabilities, setting RuntimeSetting, value string) error {
	if setting == RuntimeSettingScope {
		return nil
	}
	value = strings.TrimSpace(value)
	options := capabilities.Options(setting)
	if len(options) == 0 {
		return fmt.Errorf("%s is not supported by this runtime", runtimeSettingLabel(setting))
	}
	if value == "" || !runtimeOptionContains(options, value) {
		return fmt.Errorf("%q is not supported for %s", value, runtimeSettingLabel(setting))
	}
	return nil
}

func runtimeSettingLabel(setting RuntimeSetting) string {
	switch setting {
	case RuntimeSettingModel:
		return "model"
	case RuntimeSettingReasoningEffort:
		return "reasoning effort"
	case RuntimeSettingServiceTier:
		return "service tier"
	case RuntimeSettingApprovalMode:
		return "approval mode"
	default:
		return string(setting)
	}
}

func runtimeOptionLabel(value string) string {
	switch strings.ToLower(value) {
	case "priority", "fast":
		return "快速"
	case "default", "standard", "normal", "flex":
		return "普通"
	case ApprovalModeManual:
		return "手动审批"
	case ApprovalModeAutoEdit:
		return "自动批准编辑"
	case ApprovalModeAuto:
		return "智能自动审批"
	case ApprovalModePlan:
		return "只读规划"
	case ApprovalModeYolo:
		return "YOLO（全部允许）"
	default:
		return value
	}
}

func normalizeRuntimeSettings(settings RuntimeSettings) RuntimeSettings {
	settings.Model = strings.TrimSpace(settings.Model)
	settings.ReasoningEffort = strings.TrimSpace(settings.ReasoningEffort)
	settings.ServiceTier = strings.TrimSpace(settings.ServiceTier)
	settings.ApprovalMode = strings.TrimSpace(settings.ApprovalMode)
	return settings
}

func normalizeRuntimeCapabilities(c RuntimeSettingsCapabilities) RuntimeSettingsCapabilities {
	c.Models = normalizeRuntimeOptions(c.Models)
	c.ReasoningEfforts = normalizeRuntimeOptions(c.ReasoningEfforts)
	c.ServiceTiers = normalizeRuntimeOptions(c.ServiceTiers)
	c.ApprovalModes = normalizeRuntimeOptions(c.ApprovalModes)
	return c
}

func normalizeRuntimeOptions(options []RuntimeOption) []RuntimeOption {
	seen := map[string]bool{}
	out := make([]RuntimeOption, 0, len(options))
	for _, option := range options {
		option.Value = strings.TrimSpace(option.Value)
		option.Label = strings.TrimSpace(option.Label)
		if option.Value == "" || seen[option.Value] {
			continue
		}
		seen[option.Value] = true
		if option.Label == "" {
			option.Label = runtimeOptionLabel(option.Value)
		}
		out = append(out, option)
	}
	return out
}

func copyRuntimeCapabilities(c RuntimeSettingsCapabilities) RuntimeSettingsCapabilities {
	return RuntimeSettingsCapabilities{
		Models:           append([]RuntimeOption(nil), c.Models...),
		ReasoningEfforts: append([]RuntimeOption(nil), c.ReasoningEfforts...),
		ServiceTiers:     append([]RuntimeOption(nil), c.ServiceTiers...),
		ApprovalModes:    append([]RuntimeOption(nil), c.ApprovalModes...),
	}
}

// ApprovalModeValuesForRuntime is the truthful, shared capability catalog
// used by adapters, API validation and channel configuration UIs.
func ApprovalModeValuesForRuntime(runtimeID string) []string {
	switch strings.ToLower(strings.TrimSpace(runtimeID)) {
	case "claude", "claudecode", "claude-code", "claudecode-cli", "qoder":
		return []string{ApprovalModeManual, ApprovalModeAutoEdit, ApprovalModeAuto, ApprovalModePlan, ApprovalModeYolo}
	case "codex", "codex-cli":
		return []string{ApprovalModeManual, ApprovalModeAutoEdit, ApprovalModeAuto, ApprovalModePlan, ApprovalModeYolo}
	case "gemini", "opencode", "iflow":
		return []string{ApprovalModeManual, ApprovalModeAutoEdit, ApprovalModePlan, ApprovalModeYolo}
	case "cursor":
		return []string{ApprovalModeManual, ApprovalModeAuto, ApprovalModePlan, ApprovalModeYolo}
	case "kimi":
		// Kimi's current --prompt transport always uses its non-interactive auto
		// policy and rejects --yolo/--plan alongside --prompt.
		return []string{ApprovalModeAuto}
	default:
		return nil
	}
}

func ApprovalModeOptionsForRuntime(runtimeID string) []RuntimeOption {
	return RuntimeOptions(ApprovalModeValuesForRuntime(runtimeID))
}

func ApprovalModeSupported(runtimeID, mode string) bool {
	for _, candidate := range ApprovalModeValuesForRuntime(runtimeID) {
		if candidate == strings.TrimSpace(mode) {
			return true
		}
	}
	return false
}

// RuntimeSettingsForSession returns the richer runtime-settings capability
// when available, with a model-only compatibility adapter for existing agents.
func RuntimeSettingsForSession(session AgentSession) (RuntimeSettingsSession, bool) {
	if settings, ok := session.(RuntimeSettingsSession); ok {
		return settings, true
	}
	if models, ok := session.(ModelSwitchingSession); ok && models.ModelSwitchingSupported() {
		return modelSettingsAdapter{models: models}, true
	}
	return nil, false
}

type modelSettingsAdapter struct{ models ModelSwitchingSession }

func (s modelSettingsAdapter) RuntimeSettingsCapabilities() RuntimeSettingsCapabilities {
	return RuntimeSettingsCapabilities{Models: RuntimeOptions(s.models.SupportedModels())}
}

func (s modelSettingsAdapter) CurrentRuntimeSettings() RuntimeSettings {
	return RuntimeSettings{Model: s.models.CurrentModel()}
}

func (s modelSettingsAdapter) DefaultRuntimeSettings() RuntimeSettings {
	return RuntimeSettings{Model: s.models.DefaultModel()}
}

func (s modelSettingsAdapter) SetRuntimeSetting(setting RuntimeSetting, value string) error {
	if setting != RuntimeSettingModel {
		return fmt.Errorf("%s is not supported by this runtime", runtimeSettingLabel(setting))
	}
	return s.models.SetModel(value)
}

func (s modelSettingsAdapter) ResetRuntimeSetting(setting RuntimeSetting) error {
	if setting != RuntimeSettingModel {
		return fmt.Errorf("%s is not supported by this runtime", runtimeSettingLabel(setting))
	}
	return s.models.ResetModel()
}
