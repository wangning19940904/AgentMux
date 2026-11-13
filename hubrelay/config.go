// Package hubrelay is the TCP relay that runs on the public VPS in front of a
// primary and a standby hub (docs/architecture/hub-and-super-agents.md §4.2).
// Each hub reaches the VPS through its own SSH reverse tunnel; the relay grants
// one of them a short lease, forwards the public port to that hub as raw TCP
// (devices pin the hub certificate, so TLS is not terminated here), and moves
// the lease to the standby when the primary stops renewing it.
package hubrelay

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Backend is one hub as seen from the VPS: the loopback end of its tunnel.
type Backend struct {
	Name string `toml:"name"`
	Addr string `toml:"addr"`
}

// Config is the relay's TOML configuration.
type Config struct {
	Listen        string        `toml:"listen"`
	Control       string        `toml:"control"`
	StateFile     string        `toml:"state_file"`
	Secret        string        `toml:"secret"`
	SecretFile    string        `toml:"secret_file"`
	Fingerprint   string        `toml:"fingerprint"`
	LeaseTTL      time.Duration `toml:"lease_ttl"`
	RenewInterval time.Duration `toml:"renew_interval"`
	Backends      []Backend     `toml:"backends"`
}

const (
	defaultListen        = "0.0.0.0:443"
	defaultControl       = "127.0.0.1:18700"
	defaultLeaseTTL      = 15 * time.Second
	defaultRenewInterval = 5 * time.Second
)

// LoadConfig reads and validates a relay configuration. A relative
// secret_file is resolved against the configuration file's directory.
func LoadConfig(path string) (Config, error) {
	var cfg Config
	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return Config{}, fmt.Errorf("hubrelay: read config: %w", err)
	}
	// A misspelt key would otherwise silently fall back to a default.
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return Config{}, fmt.Errorf("hubrelay: unknown config key %q", undecoded[0].String())
	}
	if cfg.SecretFile != "" && !filepath.IsAbs(cfg.SecretFile) {
		cfg.SecretFile = filepath.Join(filepath.Dir(path), cfg.SecretFile)
	}
	if err := cfg.normalize(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// normalize applies defaults, reads secret_file and validates the result.
func (c *Config) normalize() error {
	if c.Listen == "" {
		c.Listen = defaultListen
	}
	if c.Control == "" {
		c.Control = defaultControl
	}
	if c.LeaseTTL == 0 {
		c.LeaseTTL = defaultLeaseTTL
	}
	if c.RenewInterval == 0 {
		c.RenewInterval = defaultRenewInterval
	}
	if c.Secret == "" && c.SecretFile != "" {
		raw, err := os.ReadFile(c.SecretFile)
		if err != nil {
			return fmt.Errorf("hubrelay: read secret_file: %w", err)
		}
		c.Secret = strings.TrimSpace(string(raw))
	}
	c.Fingerprint = normalizeFingerprint(c.Fingerprint)

	switch {
	case c.Secret == "":
		return errors.New("hubrelay: secret or secret_file is required")
	case c.Fingerprint == "":
		return errors.New("hubrelay: fingerprint is required")
	case c.StateFile == "":
		return errors.New("hubrelay: state_file is required")
	case len(c.Backends) == 0:
		return errors.New("hubrelay: at least one backend is required")
	case c.LeaseTTL <= 0 || c.RenewInterval <= 0:
		return errors.New("hubrelay: lease_ttl and renew_interval must be positive")
	case c.RenewInterval >= c.LeaseTTL/2:
		// Two renewals must fit in one lease, or a single slow round would
		// let a healthy primary's lease lapse.
		return fmt.Errorf("hubrelay: renew_interval (%s) must be less than half of lease_ttl (%s)", c.RenewInterval, c.LeaseTTL)
	}
	seen := make(map[string]bool, len(c.Backends))
	for _, b := range c.Backends {
		if b.Name == "" || b.Addr == "" {
			return errors.New("hubrelay: every backend needs a name and an addr")
		}
		if seen[b.Name] {
			return fmt.Errorf("hubrelay: duplicate backend name %q", b.Name)
		}
		seen[b.Name] = true
	}
	return nil
}

// normalizeFingerprint accepts the hex form link.Fingerprint prints as well as
// the colon-separated form some tools show.
func normalizeFingerprint(fp string) string {
	fp = strings.ToLower(strings.TrimSpace(fp))
	return strings.ReplaceAll(fp, ":", "")
}
