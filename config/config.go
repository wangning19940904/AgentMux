// Package config loads and parses AgentMux's config.toml. It extends the
// cc-connect project model with [provider], [usage] and [usage.ssh] sections.
package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config is the top-level configuration.
type Config struct {
	DisplayMode   string              `toml:"display_mode"`
	Server        ServerConfig        `toml:"server"`
	Database      DatabaseConfig      `toml:"database"`
	Bridge        BridgeConfig        `toml:"bridge"`
	Remote        RemoteConfig        `toml:"remote"`
	Projects      []ProjectConfig     `toml:"projects"`
	Hooks         []HookConfig        `toml:"hooks"`
	Provider      ProviderConfig      `toml:"provider"`
	Usage         UsageConfig         `toml:"usage"`
	Observability ObservabilityConfig `toml:"observability"`
}

// DatabaseConfig configures the PostgreSQL runtime store.
type DatabaseConfig struct {
	URL                   string `toml:"url"`
	MaxOpenConnections    int    `toml:"max_open_connections"`
	MaxIdleConnections    int    `toml:"max_idle_connections"`
	ConnectionMaxLifetime string `toml:"connection_max_lifetime"`
}

// ServerConfig configures the HTTP/WS management server.
type ServerConfig struct {
	Addr string `toml:"addr"`
}

// BridgeConfig configures the external bridge protocol. Token is required when
// enabled (security hardening borrowed from cc-connect v1.3.3).
type BridgeConfig struct {
	Enabled bool   `toml:"enabled"`
	Token   string `toml:"token"`
}

// RemoteConfig controls the local SSH control-plane client. Remote host
// profiles are intentionally stored outside config.toml so they can be
// managed from the Console without rewriting the daemon configuration.
type RemoteConfig struct {
	HostsFile             string `toml:"hosts_file"`
	ConnectTimeoutSeconds int    `toml:"connect_timeout_seconds"`
}

// ProjectConfig pairs one agent with one or more platforms.
type ProjectConfig struct {
	Name            string            `toml:"name"`
	Agent           string            `toml:"agent"`
	WorkDir         string            `toml:"work_dir"`
	WorkspaceMode   string            `toml:"workspace_mode"`
	WorktreeBaseRef string            `toml:"worktree_base_ref"`
	SessionBackend  string            `toml:"session_backend"`
	SystemPrompt    string            `toml:"system_prompt"`
	DefaultModel    string            `toml:"default_model"`
	Env             map[string]string `toml:"env"`
	Platforms       []map[string]any  `toml:"platforms"`
}

// HookConfig is a single lifecycle hook.
type HookConfig struct {
	Event   string `toml:"event"`
	Type    string `toml:"type"`
	Command string `toml:"command"`
	URL     string `toml:"url"`
}

// ProviderConfig configures provider management.
type ProviderConfig struct {
	ProxyAddr string `toml:"proxy_addr"`
	Failover  bool   `toml:"failover"`
}

// UsageConfig configures the token-usage engine.
type UsageConfig struct {
	Sources    []string    `toml:"sources"`
	CacheDir   string      `toml:"cache_dir"`
	Offline    bool        `toml:"offline"`
	SSHTargets []SSHTarget `toml:"ssh"`
}

// ObservabilityConfig controls local trace capture, encrypted content
// retention, transcript backfill, and optional remote exporters.
type ObservabilityConfig struct {
	Enabled              bool   `toml:"enabled"`
	CaptureContent       string `toml:"capture_content"`
	ContentRetentionDays int    `toml:"content_retention_days"`
	DetailRetentionDays  int    `toml:"detail_retention_days"`
	BackfillDays         int    `toml:"backfill_days"`
	// MasterKeyEnv is primarily for non-macOS deployments. macOS uses
	// Keychain when this is empty; other platforms fall back to metadata-only.
	MasterKeyEnv string                        `toml:"master_key_env"`
	Exporters    []ObservabilityExporterConfig `toml:"exporters"`
}

// ObservabilityExporterConfig configures an isolated exporter queue. OTLP
// content export remains opt-in per exporter.
type ObservabilityExporterConfig struct {
	Name           string            `toml:"name"`
	Type           string            `toml:"type"`
	Enabled        bool              `toml:"enabled"`
	Endpoint       string            `toml:"endpoint"`
	Protocol       string            `toml:"protocol"`
	Headers        map[string]string `toml:"headers"`
	IncludeContent bool              `toml:"include_content"`
	TimeoutSeconds int               `toml:"timeout_seconds"`
	QueueSize      int               `toml:"queue_size"`
}

// SSHTarget describes a remote machine to collect usage from.
type SSHTarget struct {
	Name     string            `toml:"name"`
	Host     string            `toml:"host"`
	Port     int               `toml:"port"`
	User     string            `toml:"user"`
	KeyPath  string            `toml:"key_path"`
	Password string            `toml:"password"`
	Sources  []string          `toml:"sources"`
	Paths    map[string]string `toml:"paths"` // source -> remote path
}

var envRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Load reads and parses the config at path, expanding ${ENV} placeholders.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	expanded := envRe.ReplaceAllStringFunc(string(raw), func(m string) string {
		name := envRe.FindStringSubmatch(m)[1]
		return os.Getenv(name)
	})
	// Observability is an in-process, encrypted feature and is enabled for new
	// and existing configs unless the user explicitly opts out.
	c := Config{Observability: ObservabilityConfig{Enabled: true}}
	if _, err := toml.Decode(expanded, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) validate() error {
	if !strings.HasPrefix(c.Database.URL, "postgres://") && !strings.HasPrefix(c.Database.URL, "postgresql://") {
		return fmt.Errorf("database.url must use postgres:// or postgresql://")
	}
	if _, err := time.ParseDuration(c.Database.ConnectionMaxLifetime); err != nil {
		return fmt.Errorf("database.connection_max_lifetime: %w", err)
	}
	if c.Database.MaxIdleConnections > c.Database.MaxOpenConnections {
		return fmt.Errorf("database.max_idle_connections cannot exceed max_open_connections")
	}
	if c.Bridge.Enabled && c.Bridge.Token == "" {
		return fmt.Errorf("bridge enabled but no token set (refusing to start: security)")
	}
	if c.Remote.ConnectTimeoutSeconds < 1 || c.Remote.ConnectTimeoutSeconds > 120 {
		return fmt.Errorf("remote.connect_timeout_seconds must be between 1 and 120")
	}
	for _, p := range c.Projects {
		if p.Name == "" {
			return fmt.Errorf("project with empty name")
		}
		if p.Agent == "" {
			return fmt.Errorf("project %q has no agent", p.Name)
		}
		switch strings.ToLower(strings.TrimSpace(p.WorkspaceMode)) {
		case "", "shared", "worktree":
		default:
			return fmt.Errorf("project %q has invalid workspace_mode %q", p.Name, p.WorkspaceMode)
		}
		switch strings.ToLower(strings.TrimSpace(p.SessionBackend)) {
		case "", "structured", "tmux":
		default:
			return fmt.Errorf("project %q has invalid session_backend %q", p.Name, p.SessionBackend)
		}
	}
	switch c.Observability.CaptureContent {
	case "off", "metadata", "full":
	default:
		return fmt.Errorf("observability.capture_content must be off, metadata, or full")
	}
	for _, exporter := range c.Observability.Exporters {
		if exporter.Enabled && exporter.Endpoint == "" {
			return fmt.Errorf("observability exporter %q enabled without endpoint", exporter.Name)
		}
		if exporter.Type != "otlp_http" {
			return fmt.Errorf("observability exporter %q has unsupported type %q", exporter.Name, exporter.Type)
		}
		if exporter.Protocol != "http/json" {
			return fmt.Errorf("observability exporter %q has unsupported protocol %q (use http/json)", exporter.Name, exporter.Protocol)
		}
	}
	return nil
}

func (c *Config) applyDefaults() {
	if c.Server.Addr == "" {
		c.Server.Addr = "127.0.0.1:8765"
	}
	if c.DisplayMode == "" {
		c.DisplayMode = "normal"
	}
	if c.Remote.ConnectTimeoutSeconds == 0 {
		c.Remote.ConnectTimeoutSeconds = 10
	}
	if value := os.Getenv("AGENTMUX_DATABASE_URL"); value != "" {
		c.Database.URL = value
	}
	if c.Database.URL == "" {
		c.Database.URL = "postgresql:///agentmux?host=/tmp&sslmode=disable"
	}
	if c.Database.MaxOpenConnections <= 0 {
		c.Database.MaxOpenConnections = 12
	}
	if c.Database.MaxIdleConnections < 0 {
		c.Database.MaxIdleConnections = 0
	} else if c.Database.MaxIdleConnections == 0 {
		c.Database.MaxIdleConnections = 4
	}
	if c.Database.ConnectionMaxLifetime == "" {
		c.Database.ConnectionMaxLifetime = "30m"
	}
	if len(c.Usage.Sources) == 0 {
		c.Usage.Sources = []string{"claude", "codex", "cursor", "gemini"}
	}
	if c.Observability.CaptureContent == "" {
		c.Observability.CaptureContent = "full"
	}
	if c.Observability.ContentRetentionDays <= 0 {
		c.Observability.ContentRetentionDays = 30
	}
	if c.Observability.DetailRetentionDays <= 0 {
		c.Observability.DetailRetentionDays = 30
	}
	if c.Observability.BackfillDays <= 0 {
		c.Observability.BackfillDays = 30
	}
	for index := range c.Observability.Exporters {
		exporter := &c.Observability.Exporters[index]
		if exporter.Name == "" {
			exporter.Name = fmt.Sprintf("otlp-%d", index+1)
		}
		if exporter.Type == "" {
			exporter.Type = "otlp_http"
		}
		if exporter.Protocol == "" {
			exporter.Protocol = "http/json"
		}
		if exporter.TimeoutSeconds <= 0 {
			exporter.TimeoutSeconds = 10
		}
		if exporter.QueueSize <= 0 {
			exporter.QueueSize = 10000
		}
	}
}

// Default returns a config with defaults applied (used when no config file
// exists for store-only commands).
func Default() *Config {
	c := &Config{Observability: ObservabilityConfig{Enabled: true}}
	c.applyDefaults()
	return c
}
