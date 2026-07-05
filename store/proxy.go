package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"time"
)

// ProxyToolConfig is the per-tool local-routing row (cc-switch proxy_config).
type ProxyToolConfig struct {
	Tool             string `json:"tool"`
	Enabled          bool   `json:"enabled"`
	AutoFailover     bool   `json:"auto_failover"`
	MaxRetries       int    `json:"max_retries"`
	FailureThreshold int    `json:"failure_threshold"`
	CooldownSeconds  int    `json:"cooldown_seconds"`
}

func defaultProxyToolConfig(tool string) ProxyToolConfig {
	return ProxyToolConfig{
		Tool:             tool,
		MaxRetries:       3,
		FailureThreshold: 4,
		CooldownSeconds:  60,
	}
}

// GetProxyToolConfig returns the tool's local-routing config (defaults if unset).
func (s *Store) GetProxyToolConfig(ctx context.Context, tool string) (ProxyToolConfig, error) {
	row := s.db.QueryRowContext(ctx, `SELECT tool,enabled,auto_failover,max_retries,
		failure_threshold,cooldown_seconds FROM proxy_config WHERE tool=?`, tool)
	cfg, err := scanProxyToolConfig(row)
	if err == sql.ErrNoRows {
		return defaultProxyToolConfig(tool), nil
	}
	return cfg, err
}

// ListProxyToolConfigs returns all persisted local-routing rows.
func (s *Store) ListProxyToolConfigs(ctx context.Context) ([]ProxyToolConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tool,enabled,auto_failover,max_retries,
		failure_threshold,cooldown_seconds FROM proxy_config ORDER BY tool`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProxyToolConfig
	for rows.Next() {
		cfg, err := scanProxyToolConfig(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, cfg)
	}
	return out, rows.Err()
}

func scanProxyToolConfig(sc scanner) (ProxyToolConfig, error) {
	var cfg ProxyToolConfig
	var enabled, autoFailover int
	if err := sc.Scan(&cfg.Tool, &enabled, &autoFailover, &cfg.MaxRetries,
		&cfg.FailureThreshold, &cfg.CooldownSeconds); err != nil {
		return cfg, err
	}
	cfg.Enabled = enabled != 0
	cfg.AutoFailover = autoFailover != 0
	return cfg, nil
}

// SetProxyToolConfig upserts a tool's local-routing row.
func (s *Store) SetProxyToolConfig(ctx context.Context, cfg ProxyToolConfig) error {
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 3
	}
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = 4
	}
	if cfg.CooldownSeconds <= 0 {
		cfg.CooldownSeconds = 60
	}
	enabled, autoFailover := 0, 0
	if cfg.Enabled {
		enabled = 1
	}
	if cfg.AutoFailover {
		autoFailover = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO proxy_config
		(tool,enabled,auto_failover,max_retries,failure_threshold,cooldown_seconds)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(tool) DO UPDATE SET enabled=excluded.enabled,
		auto_failover=excluded.auto_failover,max_retries=excluded.max_retries,
		failure_threshold=excluded.failure_threshold,cooldown_seconds=excluded.cooldown_seconds`,
		cfg.Tool, enabled, autoFailover, cfg.MaxRetries, cfg.FailureThreshold, cfg.CooldownSeconds)
	return err
}

// SaveLiveBackup stores the pre-takeover live config blob for a tool.
func (s *Store) SaveLiveBackup(ctx context.Context, tool, blob string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO proxy_live_backup(tool,original_config,backed_up_at)
		VALUES (?,?,?) ON CONFLICT(tool) DO UPDATE SET original_config=excluded.original_config,
		backed_up_at=excluded.backed_up_at`, tool, blob, time.Now().Format(time.RFC3339))
	return err
}

// GetLiveBackup returns the stored live backup for a tool.
func (s *Store) GetLiveBackup(ctx context.Context, tool string) (string, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT original_config FROM proxy_live_backup WHERE tool=?`, tool)
	var blob string
	switch err := row.Scan(&blob); err {
	case nil:
		return blob, true, nil
	case sql.ErrNoRows:
		return "", false, nil
	default:
		return "", false, err
	}
}

// DeleteLiveBackup removes a tool's live backup after restore.
func (s *Store) DeleteLiveBackup(ctx context.Context, tool string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM proxy_live_backup WHERE tool=?`, tool)
	return err
}

const gatewayTokenKey = "claude_desktop_gateway_token"

// GetOrCreateGatewayToken returns the Claude Desktop proxy gateway token,
// generating and persisting a random one on first use (cc-switch semantics).
func (s *Store) GetOrCreateGatewayToken(ctx context.Context) (string, error) {
	if v, ok, err := s.GetSetting(ctx, gatewayTokenKey); err != nil {
		return "", err
	} else if ok && v != "" {
		return v, nil
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := "anx-" + hex.EncodeToString(buf)
	if err := s.SetSetting(ctx, gatewayTokenKey, token); err != nil {
		return "", err
	}
	return token, nil
}
