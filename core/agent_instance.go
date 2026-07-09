package core

import "time"

// AgentChannelBinding connects an Agent instance to an inbound or outbound
// messaging surface. Secret-like config values are redacted before the API
// returns config-derived bindings.
type AgentChannelBinding struct {
	ID     string            `json:"id"`
	Type   string            `json:"type"`
	Name   string            `json:"name,omitempty"`
	ChatID string            `json:"chat_id,omitempty"`
	Status string            `json:"status,omitempty"`
	Config map[string]string `json:"config,omitempty"`
}

// AgentSchedule describes a scheduled prompt for an Agent instance. The cron
// string is stored as-is so different schedulers can interpret it later.
type AgentSchedule struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Cron    string `json:"cron"`
	Prompt  string `json:"prompt"`
	Enabled bool   `json:"enabled"`
}

// AgentInstance is the product-level object users manage in AgentNexus. It
// wraps a local coding-agent runtime with routing, channels, memory, tools and
// governance bindings.
type AgentInstance struct {
	ID              string                `json:"id"`
	Name            string                `json:"name"`
	RuntimeID       string                `json:"runtime_id"`
	WorkDir         string                `json:"work_dir,omitempty"`
	SystemPrompt    string                `json:"system_prompt,omitempty"`
	ProviderTool    string                `json:"provider_tool,omitempty"`
	ProviderID      string                `json:"provider_id,omitempty"`
	ProviderName    string                `json:"provider_name,omitempty"`
	DefaultModel    string                `json:"default_model,omitempty"`
	MemoryScope     string                `json:"memory_scope,omitempty"`
	Env             map[string]string     `json:"env,omitempty"`
	ChannelBindings []AgentChannelBinding `json:"channel_bindings,omitempty"`
	Schedules       []AgentSchedule       `json:"schedules,omitempty"`
	MCPServers      []string              `json:"mcp_servers,omitempty"`
	Skills          []string              `json:"skills,omitempty"`
	CLIs            []string              `json:"clis,omitempty"`
	Enabled         bool                  `json:"enabled"`
	Source          string                `json:"source,omitempty"` // manual, console, config.toml
	CreatedAt       time.Time             `json:"created_at"`
	UpdatedAt       time.Time             `json:"updated_at"`
}
