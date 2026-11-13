// Package config loads and parses AgentMux's config.toml. It extends the
// cc-connect project model with [provider], [usage] and [usage.ssh] sections.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config is the top-level configuration.
type Config struct {
	DisplayMode string         `toml:"display_mode"`
	Server      ServerConfig   `toml:"server"`
	Database    DatabaseConfig `toml:"database"`
	Bridge      BridgeConfig   `toml:"bridge"`
	Remote      RemoteConfig   `toml:"remote"`
	Hub         HubConfig      `toml:"hub"`
	// Projects and Hooks are parsed only for `amux database import-config`.
	// Daemon startup rejects them so PostgreSQL remains the sole runtime source.
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

// HubConfig lets other machines connect to this one as their hub. Paired
// devices open the connection themselves, so only the hub needs an address
// they can reach. Addr is a listener that serves nothing but pairing and
// device connections, which makes it safe to expose where the management API
// on server.addr is not.
type HubConfig struct {
	// Addr is host:port for the device listener. Empty leaves it off; devices
	// that can reach server.addr may still pair through it.
	Addr string `toml:"addr"`
	// TLS is "auto" (the default: the hub's own certificate, which devices
	// pin by fingerprint, so no domain name is needed), "files" (CertFile
	// and KeyFile), or "off" behind a proxy or tunnel that terminates TLS.
	TLS      string `toml:"tls"`
	CertFile string `toml:"cert_file"`
	KeyFile  string `toml:"key_file"`
	// PublicURL is the address devices dial when it differs from Addr: a
	// tunnel, a reverse proxy, a forwarded port.
	PublicURL string `toml:"public_url"`
	// PinCertificate says devices reaching PublicURL still see the hub's own
	// certificate, as they do through a plain port forward. It is assumed
	// when PublicURL is empty and TLS is "auto".
	PinCertificate bool `toml:"pin_certificate"`
	// StateDir holds the hub's certificate and this machine's own membership
	// of a hub. It defaults to the user's AgentMux configuration directory.
	StateDir string `toml:"state_dir"`
}

// Hub TLS modes.
const (
	HubTLSAuto  = "auto"
	HubTLSFiles = "files"
	HubTLSOff   = "off"
)

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
	if err := c.ValidateListenSecurity(); err != nil {
		return err
	}
	if c.Remote.ConnectTimeoutSeconds < 1 || c.Remote.ConnectTimeoutSeconds > 120 {
		return fmt.Errorf("remote.connect_timeout_seconds must be between 1 and 120")
	}
	if err := c.validateHub(); err != nil {
		return err
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

// ValidateListenSecurity prevents the unauthenticated management API from
// being exposed on a non-loopback interface. Callers that override Server.Addr
// after loading config must call this method again before starting the server.
func (c *Config) ValidateListenSecurity() error {
	if c == nil {
		return fmt.Errorf("config is required")
	}
	addr := strings.TrimSpace(c.Server.Addr)
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("server.addr %q must be host:port: %w", addr, err)
	}
	if c.Bridge.Enabled {
		return nil
	}
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("server.addr %q is not loopback; enable bridge authentication before exposing the management API", addr)
}

func (c *Config) validateHub() error {
	switch c.Hub.TLS {
	case HubTLSAuto, HubTLSOff:
	case HubTLSFiles:
		if c.Hub.CertFile == "" || c.Hub.KeyFile == "" {
			return fmt.Errorf("hub.tls = \"files\" needs hub.cert_file and hub.key_file")
		}
	default:
		return fmt.Errorf("hub.tls must be \"auto\", \"files\" or \"off\"")
	}
	if c.Hub.Addr != "" {
		if _, _, err := net.SplitHostPort(c.Hub.Addr); err != nil {
			return fmt.Errorf("hub.addr %q must be host:port: %w", c.Hub.Addr, err)
		}
	}
	if c.Hub.PublicURL != "" {
		parsed, err := url.Parse(c.Hub.PublicURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("hub.public_url %q must look like https://host:port", c.Hub.PublicURL)
		}
	}
	return nil
}

func (c *Config) applyDefaults() {
	if c.Server.Addr == "" {
		c.Server.Addr = "127.0.0.1:8765"
	}
	c.Hub.Addr = strings.TrimSpace(c.Hub.Addr)
	c.Hub.TLS = strings.ToLower(strings.TrimSpace(c.Hub.TLS))
	if c.Hub.TLS == "" {
		c.Hub.TLS = HubTLSAuto
		if c.Hub.CertFile != "" || c.Hub.KeyFile != "" {
			c.Hub.TLS = HubTLSFiles
		}
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
	// Headless and remotely managed AgentMux services receive their bridge
	// credential through the process environment. Treat it as an explicit
	// override so a service can enable bridge authentication without writing
	// the secret into config.toml.
	if value := strings.TrimSpace(os.Getenv("AGENTMUX_BRIDGE_TOKEN")); value != "" {
		c.Bridge.Enabled = true
		c.Bridge.Token = value
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
