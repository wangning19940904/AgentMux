// Package config loads and parses AgentNexus's config.toml. It extends the
// cc-connect project model with [provider], [usage] and [usage.ssh] sections.
package config

import (
	"fmt"
	"os"
	"regexp"

	"github.com/BurntSushi/toml"
)

// Config is the top-level configuration.
type Config struct {
	DisplayMode string         `toml:"display_mode"`
	Server      ServerConfig   `toml:"server"`
	Bridge      BridgeConfig   `toml:"bridge"`
	Projects    []ProjectConfig `toml:"projects"`
	Hooks       []HookConfig   `toml:"hooks"`
	Provider    ProviderConfig `toml:"provider"`
	Usage       UsageConfig    `toml:"usage"`
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

// ProjectConfig pairs one agent with one or more platforms.
type ProjectConfig struct {
	Name         string           `toml:"name"`
	Agent        string           `toml:"agent"`
	WorkDir      string           `toml:"work_dir"`
	SystemPrompt string           `toml:"system_prompt"`
	Env          map[string]string `toml:"env"`
	Platforms    []map[string]any `toml:"platforms"`
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
	Sources   []string        `toml:"sources"`
	CacheDir  string          `toml:"cache_dir"`
	Offline   bool            `toml:"offline"`
	SSHTargets []SSHTarget    `toml:"ssh"`
}

// SSHTarget describes a remote machine to collect usage from.
type SSHTarget struct {
	Name     string   `toml:"name"`
	Host     string   `toml:"host"`
	Port     int      `toml:"port"`
	User     string   `toml:"user"`
	KeyPath  string   `toml:"key_path"`
	Password string   `toml:"password"`
	Sources  []string `toml:"sources"`
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
	var c Config
	if _, err := toml.Decode(expanded, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	c.applyDefaults()
	return &c, nil
}

func (c *Config) validate() error {
	if c.Bridge.Enabled && c.Bridge.Token == "" {
		return fmt.Errorf("bridge enabled but no token set (refusing to start: security)")
	}
	for _, p := range c.Projects {
		if p.Name == "" {
			return fmt.Errorf("project with empty name")
		}
		if p.Agent == "" {
			return fmt.Errorf("project %q has no agent", p.Name)
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
	if len(c.Usage.Sources) == 0 {
		c.Usage.Sources = []string{"claude", "codex", "cursor", "gemini"}
	}
}

// Default returns a config with defaults applied (used when no config file
// exists for store-only commands).
func Default() *Config {
	c := &Config{}
	c.applyDefaults()
	return c
}
