package core

// RuntimeSettingsScope selects whether a picker action mutates the active
// conversation or the persisted defaults used by future conversations.
type RuntimeSettingsScope string

const (
	RuntimeSettingsScopeConversation RuntimeSettingsScope = "conversation"
	RuntimeSettingsScopeAgent        RuntimeSettingsScope = "agent"
)

// RuntimeSettingsPickerState is the transport-neutral state rendered by
// interactive channel cards and command-menu fallbacks.
type RuntimeSettingsPickerState struct {
	Scope                 RuntimeSettingsScope        `json:"scope"`
	Settings              RuntimeSettings             `json:"settings"`
	RuntimeDefaults       RuntimeSettings             `json:"runtime_defaults"`
	Capabilities          RuntimeSettingsCapabilities `json:"capabilities"`
	AgentDefaultsEditable bool                        `json:"agent_defaults_editable"`
	Unsupported           map[RuntimeSetting]string   `json:"unsupported,omitempty"`
	Notice                string                      `json:"notice,omitempty"`
}

// RuntimeSettingsAction is attached to an inbound Message produced by an
// interactive control. The original picker message id stays in Message.ID so
// the platform can update that message in place after the mutation.
type RuntimeSettingsAction struct {
	Scope   RuntimeSettingsScope `json:"scope"`
	Setting RuntimeSetting       `json:"setting"`
	Value   string               `json:"value,omitempty"`
	Reset   bool                 `json:"reset,omitempty"`
}
