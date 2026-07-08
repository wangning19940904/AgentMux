package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/agentnexus/agentnexus/core"
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

// InsertProxyTrace stores one local-routing request trace.
func (s *Store) InsertProxyTrace(ctx context.Context, trace core.ProxyTrace) error {
	if trace.ID == "" {
		trace.ID = newProxyTraceID()
	}
	if trace.Timestamp.IsZero() {
		trace.Timestamp = time.Now().UTC()
	}
	success := 0
	if trace.Success {
		success = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO proxy_traces
		(id,timestamp,tool,provider_id,provider_name,client_protocol,upstream_protocol,
		 client_model,upstream_model,status_code,success,error,session_id,project_dir)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		trace.ID, trace.Timestamp.Format(time.RFC3339Nano), trace.Tool, trace.ProviderID,
		trace.ProviderName, trace.ClientProtocol, trace.UpstreamProtocol, trace.ClientModel,
		trace.UpstreamModel, trace.StatusCode, success, trace.Error, trace.SessionID, trace.ProjectDir)
	return err
}

// QueryProxyTraces returns recent local-routing traces, optionally filtered.
func (s *Store) QueryProxyTraces(ctx context.Context, tool, sessionID string, limit int) ([]core.ProxyTrace, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	q := `SELECT id,timestamp,tool,provider_id,provider_name,client_protocol,upstream_protocol,
		client_model,upstream_model,status_code,success,error,session_id,project_dir
		FROM proxy_traces`
	args := []any{}
	where := []string{}
	if tool != "" {
		where = append(where, "tool=?")
		args = append(args, tool)
	}
	if sessionID != "" {
		where = append(where, "session_id=?")
		args = append(args, sessionID)
	}
	for i, clause := range where {
		if i == 0 {
			q += " WHERE "
		} else {
			q += " AND "
		}
		q += clause
	}
	q += " ORDER BY timestamp DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.ProxyTrace
	for rows.Next() {
		var trace core.ProxyTrace
		var ts string
		var success int
		if err := rows.Scan(&trace.ID, &ts, &trace.Tool, &trace.ProviderID, &trace.ProviderName,
			&trace.ClientProtocol, &trace.UpstreamProtocol, &trace.ClientModel, &trace.UpstreamModel,
			&trace.StatusCode, &success, &trace.Error, &trace.SessionID, &trace.ProjectDir); err != nil {
			return nil, err
		}
		trace.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		trace.Success = success != 0
		out = append(out, trace)
	}
	return out, rows.Err()
}

func newProxyTraceID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err == nil {
		return "ptrace-" + hex.EncodeToString(buf)
	}
	return "ptrace-" + strconv.FormatInt(time.Now().UnixNano(), 10)
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
