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
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Preset     string            `json:"preset,omitempty"`
	BaseURL    string            `json:"base_url"`
	APIKeyEnv  string            `json:"api_key_env,omitempty"`
	Model      string            `json:"model,omitempty"`
	Tools      []string          `json:"tools"` // claudecode, codex, gemini...
	Extra      map[string]string `json:"extra,omitempty"`
	Enabled    bool              `json:"enabled"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

// ProviderManager handles provider CRUD, switching and live-config sync.
type ProviderManager interface {
	List(ctx context.Context) ([]*Provider, error)
	Get(ctx context.Context, id string) (*Provider, error)
	Upsert(ctx context.Context, p *Provider) error
	Delete(ctx context.Context, id string) error
	// Switch enables provider id for the given tool and writes the tool's
	// live config (e.g. ~/.claude/settings.json) atomically.
	Switch(ctx context.Context, id, tool string) error
	// Active returns the currently enabled provider for a tool.
	Active(ctx context.Context, tool string) (*Provider, error)
}

// UsageRecord is a single normalized usage row produced by a parser. Every
// data-source adapter (Claude, Codex, Cursor, Gemini, ...) normalizes into
// this shape so the aggregator and clients share one schema.
type UsageRecord struct {
	Source           string    `json:"source"`  // claude, codex, cursor...
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
