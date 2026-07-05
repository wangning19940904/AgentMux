package core

import (
	"context"
	"time"
)

// Provider is an LLM provider configuration that maps to one or more coding
// tools (Claude Code, Codex, Gemini, ...). Provider management is ported from
// cc-switch: a Provider can be written to a tool's live config and switched
// atomically.
type Provider struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Preset         string            `json:"preset,omitempty"`
	Category       string            `json:"category,omitempty"` // official, third_party, custom
	BaseURL        string            `json:"base_url"`
	APIKeyEnv      string            `json:"api_key_env,omitempty"`
	APIKey         string            `json:"api_key,omitempty"` // write-only; injected into APIKeyEnv and never persisted
	Model          string            `json:"model,omitempty"`
	Tools          []string          `json:"tools"` // claudecode, claude-desktop, codex, gemini...
	Extra          map[string]string `json:"extra,omitempty"`
	SettingsConfig map[string]any    `json:"settings_config,omitempty"`
	Meta           ProviderMeta      `json:"meta,omitempty"`
	Enabled        bool              `json:"enabled"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

// ProviderMeta carries tool-specific routing hints without forcing the core
// provider row to know every downstream config shape.
type ProviderMeta struct {
	APIFormat             string               `json:"api_format,omitempty"`          // anthropic, openai_responses, openai_chat
	CodexWireAPI          string               `json:"codex_wire_api,omitempty"`      // responses, chat
	ClaudeDesktopMode     string               `json:"claude_desktop_mode,omitempty"` // direct
	ClaudeDesktopModels   []ClaudeDesktopModel `json:"claude_desktop_models,omitempty"`
	ClaudeDesktopAuthMode string               `json:"claude_desktop_auth_mode,omitempty"` // bearer
}

// ClaudeDesktopModel is the subset of Claude Desktop's 3P profile model entry
// AgentNexus needs for direct-mode routing.
type ClaudeDesktopModel struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

// ProviderManager handles provider CRUD, switching and live-config sync.
type ProviderManager interface {
	List(ctx context.Context) ([]*Provider, error)
	Get(ctx context.Context, id string) (*Provider, error)
	Upsert(ctx context.Context, p *Provider) error
	Delete(ctx context.Context, id string) error
	// ActiveRoutes returns every tool -> provider binding recorded by Router.
	ActiveRoutes(ctx context.Context) ([]ProviderRoute, error)
	// Switch enables provider id for the given tool and writes the tool's
	// live config (e.g. ~/.claude/settings.json) atomically.
	Switch(ctx context.Context, id, tool string) error
	// Clear removes the active route for a tool.
	Clear(ctx context.Context, tool string) error
	// Active returns the currently enabled provider for a tool.
	Active(ctx context.Context, tool string) (*Provider, error)
}

// ProviderRoute is the read model for Router's active tool -> provider table.
type ProviderRoute struct {
	Tool         string `json:"tool"`
	ProviderID   string `json:"provider_id"`
	ProviderName string `json:"provider_name,omitempty"`
	BaseURL      string `json:"base_url,omitempty"`
	APIKeyEnv    string `json:"api_key_env,omitempty"`
	Model        string `json:"model,omitempty"`
	APIFormat    string `json:"api_format,omitempty"`
	Configured   bool   `json:"configured"`
}

// UsageRecord is a single normalized usage row produced by a parser. Every
// data-source adapter (Claude, Codex, Cursor, Gemini, ...) normalizes into
// this shape so the aggregator and clients share one schema.
type UsageRecord struct {
	Source           string    `json:"source"` // claude, codex, cursor...
	SessionID        string    `json:"session_id"`
	Project          string    `json:"project"`
	Model            string    `json:"model"`
	Timestamp        time.Time `json:"timestamp"`
	InputTokens      int64     `json:"input_tokens"`
	OutputTokens     int64     `json:"output_tokens"`
	CacheReadTokens  int64     `json:"cache_read_tokens"`
	CacheWriteTokens int64     `json:"cache_write_tokens"`
	Tool             string    `json:"tool,omitempty"` // tool_use name
	CostUSD          float64   `json:"cost_usd"`
	Host             string    `json:"host,omitempty"` // "" = local, else SSH host
}

// UsageCollector reads a local (or remote-synced) data source and yields
// normalized usage records.
type UsageCollector interface {
	// Source returns the collector id, e.g. "claude".
	Source() string
	// Collect scans the data source and returns records since the given time
	// (zero = all). Implementations must be read-only.
	Collect(ctx context.Context, since time.Time) ([]UsageRecord, error)
}
