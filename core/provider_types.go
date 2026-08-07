package core

import (
	"context"
	"strings"
	"time"
)

// Provider is an LLM provider configuration that maps to one or more coding
// tools (Claude Code, Codex, Gemini, ...). Provider management is ported from
// cc-switch: a Provider can be written to a tool's live config and switched
// atomically.
type Provider struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Preset          string            `json:"preset,omitempty"`
	Category        string            `json:"category,omitempty"` // official, third_party, custom
	BaseURL         string            `json:"base_url"`
	APIKeyEnv       string            `json:"api_key_env,omitempty"`
	APIKey          string            `json:"api_key,omitempty"` // write-only; saved to OS secrets, never SQLite/API responses
	APIKeyAvailable bool              `json:"api_key_available"`
	APIKeyIssue     string            `json:"api_key_issue,omitempty"`
	Model           string            `json:"model,omitempty"`
	Extra           map[string]string `json:"extra,omitempty"`
	SettingsConfig  map[string]any    `json:"settings_config,omitempty"`
	Meta            ProviderMeta      `json:"meta,omitempty"`
	Enabled         bool              `json:"enabled"`
	// InFailoverQueue marks this provider as a local-routing failover
	// candidate; SortIndex orders the queue (cc-switch semantics).
	InFailoverQueue bool      `json:"in_failover_queue,omitempty"`
	SortIndex       int       `json:"sort_index,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// ProviderMeta carries tool-specific routing hints without forcing the core
// provider row to know every downstream config shape.
type ProviderMeta struct {
	APIFormat       string   `json:"api_format,omitempty"`     // anthropic, openai_responses, openai_chat
	CodexWireAPI    string   `json:"codex_wire_api,omitempty"` // responses, chat
	SupportedModels []string `json:"supported_models,omitempty"`
	// Explicit runtime controls for custom providers. They are exposed only
	// when the selected adapter can carry the corresponding protocol field.
	SupportedReasoningEfforts []string `json:"supported_reasoning_efforts,omitempty"`
	DefaultReasoningEffort    string   `json:"default_reasoning_effort,omitempty"`
	SupportedServiceTiers     []string `json:"supported_service_tiers,omitempty"`
	DefaultServiceTier        string   `json:"default_service_tier,omitempty"`
	SupportedAPIFormats       []string `json:"supported_api_formats,omitempty"`
	SupportedProtocols        []string `json:"supported_protocols,omitempty"`
	// ClaudeAuthScheme selects the env credential key written for Claude Code:
	// "auth_token" (ANTHROPIC_AUTH_TOKEN, cc-switch's third-party default) or
	// "api_key" (ANTHROPIC_API_KEY, official direct). Empty = auto: official
	// providers use api_key, everything else auth_token.
	ClaudeAuthScheme string `json:"claude_auth_scheme,omitempty"`
	// Claude Code tiered model overrides (ANTHROPIC_DEFAULT_*_MODEL).
	ClaudeSonnetModel     string               `json:"claude_sonnet_model,omitempty"`
	ClaudeOpusModel       string               `json:"claude_opus_model,omitempty"`
	ClaudeHaikuModel      string               `json:"claude_haiku_model,omitempty"`
	ClaudeDesktopMode     string               `json:"claude_desktop_mode,omitempty"` // direct, proxy
	ClaudeDesktopModels   []ClaudeDesktopModel `json:"claude_desktop_models,omitempty"`
	ClaudeDesktopAuthMode string               `json:"claude_desktop_auth_mode,omitempty"` // bearer
}

// RouteMetaFromProvider extracts tool-route policy from legacy provider meta.
// Provider meta still carries these fields for presets/backward compatibility;
// route meta owns the editable values for an active tool binding.
func RouteMetaFromProvider(meta ProviderMeta) ProviderMeta {
	return ProviderMeta{
		ClaudeAuthScheme:      meta.ClaudeAuthScheme,
		ClaudeSonnetModel:     meta.ClaudeSonnetModel,
		ClaudeOpusModel:       meta.ClaudeOpusModel,
		ClaudeHaikuModel:      meta.ClaudeHaikuModel,
		ClaudeDesktopMode:     meta.ClaudeDesktopMode,
		ClaudeDesktopModels:   meta.ClaudeDesktopModels,
		ClaudeDesktopAuthMode: meta.ClaudeDesktopAuthMode,
	}
}

func RouteMetaIsZero(meta ProviderMeta) bool {
	return meta.ClaudeAuthScheme == "" &&
		meta.ClaudeSonnetModel == "" &&
		meta.ClaudeOpusModel == "" &&
		meta.ClaudeHaikuModel == "" &&
		meta.ClaudeDesktopMode == "" &&
		len(meta.ClaudeDesktopModels) == 0 &&
		meta.ClaudeDesktopAuthMode == ""
}

// MergeRouteMeta overlays route-owned policy onto provider capabilities.
func MergeRouteMeta(base, route ProviderMeta) ProviderMeta {
	out := base
	if route.ClaudeAuthScheme != "" {
		out.ClaudeAuthScheme = route.ClaudeAuthScheme
	}
	if route.ClaudeSonnetModel != "" {
		out.ClaudeSonnetModel = route.ClaudeSonnetModel
	}
	if route.ClaudeOpusModel != "" {
		out.ClaudeOpusModel = route.ClaudeOpusModel
	}
	if route.ClaudeHaikuModel != "" {
		out.ClaudeHaikuModel = route.ClaudeHaikuModel
	}
	if route.ClaudeDesktopMode != "" {
		out.ClaudeDesktopMode = route.ClaudeDesktopMode
	}
	if len(route.ClaudeDesktopModels) > 0 {
		out.ClaudeDesktopModels = route.ClaudeDesktopModels
	}
	if route.ClaudeDesktopAuthMode != "" {
		out.ClaudeDesktopAuthMode = route.ClaudeDesktopAuthMode
	}
	return out
}

// ProviderWithRouteMeta returns a shallow provider copy with route policy
// applied. The original provider is left untouched.
func ProviderWithRouteMeta(p *Provider, route ProviderMeta) *Provider {
	if p == nil {
		return nil
	}
	copy := *p
	copy.Meta = MergeRouteMeta(p.Meta, route)
	return &copy
}

// ClaudeDesktopModel is the subset of Claude Desktop's 3P profile model entry
// AgentMux needs for direct- and proxy-mode routing.
type ClaudeDesktopModel struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	// UpstreamModel maps this Claude Desktop route id to the real upstream
	// model when the profile points at the local routing proxy.
	UpstreamModel string `json:"upstream_model,omitempty"`
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
	SwitchRoute(ctx context.Context, route ProviderRoute) error
	// Clear removes the active route for a tool.
	Clear(ctx context.Context, tool string) error
	// Active returns the currently enabled provider for a tool.
	Active(ctx context.Context, tool string) (*Provider, error)
}

// ProviderRoute is the read model for Router's active tool -> provider table.
type ProviderRoute struct {
	Tool            string       `json:"tool"`
	ProviderID      string       `json:"provider_id"`
	ProviderName    string       `json:"provider_name,omitempty"`
	BaseURL         string       `json:"base_url,omitempty"`
	APIKeyEnv       string       `json:"api_key_env,omitempty"`
	APIKeyAvailable bool         `json:"api_key_available"`
	APIKeyIssue     string       `json:"api_key_issue,omitempty"`
	Model           string       `json:"model,omitempty"`
	APIFormat       string       `json:"api_format,omitempty"`
	Meta            ProviderMeta `json:"meta,omitempty"`
	Configured      bool         `json:"configured"`
}

// NormalizeProviderTool collapses UI/runtime aliases onto the canonical tool
// families used by live config and local routing.
func NormalizeProviderTool(tool string) string {
	switch strings.TrimSpace(tool) {
	case "claude", "claudecode", "claudecode-cli", "claude-code-cli":
		return "claudecode"
	case "claude-desktop", "claudecode-desktop", "claude-code-desktop":
		return "claude-desktop"
	case "codex", "codex-cli", "codex-app", "codex-desktop", "codex-app-server":
		return "codex"
	default:
		return strings.TrimSpace(tool)
	}
}

// ProviderModelOptions returns the selectable models advertised by a provider:
// its default model first, then supported_models, de-duplicated.
func ProviderModelOptions(p *Provider) []string {
	if p == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(model string) {
		model = strings.TrimSpace(model)
		if model == "" || seen[model] {
			return
		}
		seen[model] = true
		out = append(out, model)
	}
	add(p.Model)
	for _, model := range p.Meta.SupportedModels {
		add(model)
	}
	return out
}

// ProxyTrace records one request that passed through AgentMux local routing.
type ProxyTrace struct {
	ID               string    `json:"id"`
	RequestID        string    `json:"request_id,omitempty"`
	TraceID          string    `json:"trace_id,omitempty"`
	ParentSpanID     string    `json:"parent_span_id,omitempty"`
	Attempt          int       `json:"attempt,omitempty"`
	ParentAttemptID  string    `json:"parent_attempt_id,omitempty"`
	Timestamp        time.Time `json:"timestamp"`
	StartedAt        time.Time `json:"started_at,omitempty"`
	Tool             string    `json:"tool"`
	ProviderID       string    `json:"provider_id"`
	ProviderName     string    `json:"provider_name,omitempty"`
	ClientProtocol   string    `json:"client_protocol"`
	UpstreamProtocol string    `json:"upstream_protocol"`
	ClientModel      string    `json:"client_model,omitempty"`
	UpstreamModel    string    `json:"upstream_model,omitempty"`
	StatusCode       int       `json:"status_code,omitempty"`
	Success          bool      `json:"success"`
	Error            string    `json:"error,omitempty"`
	SessionID        string    `json:"session_id,omitempty"`
	ProjectDir       string    `json:"project_dir,omitempty"`
	TTFTMs           int64     `json:"ttft_ms,omitempty"`
	DurationMs       int64     `json:"duration_ms,omitempty"`
	StreamComplete   bool      `json:"stream_complete"`
	FinishReason     string    `json:"finish_reason,omitempty"`
	InputTokens      int64     `json:"input_tokens,omitempty"`
	OutputTokens     int64     `json:"output_tokens,omitempty"`
	CacheReadTokens  int64     `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64     `json:"cache_write_tokens,omitempty"`
	RequestBytes     int64     `json:"request_bytes,omitempty"`
	ResponseBytes    int64     `json:"response_bytes,omitempty"`
	CostUSD          float64   `json:"cost_usd,omitempty"`
}
