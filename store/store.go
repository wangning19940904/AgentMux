// Package store provides the SQLite single-source-of-truth for AgentNexus:
// providers, sessions, cached usage records and settings.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/agentnexus/agentnexus/core"
	_ "modernc.org/sqlite"
)

// Store wraps a SQLite database connection.
type Store struct {
	db *sql.DB
}

// DefaultPath returns ~/.agentnexus/agentnexus.db.
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".agentnexus", "agentnexus.db")
}

// Open opens (and migrates) the database at path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir db dir: %w", err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(1) // serialize writes, avoids races (cc-switch approach)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS providers (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	preset TEXT,
	category TEXT,
	base_url TEXT,
	api_key_env TEXT,
	model TEXT,
	tools TEXT,
	extra TEXT,
	settings_config TEXT,
	meta TEXT,
	enabled INTEGER DEFAULT 0,
	created_at TEXT,
	updated_at TEXT
);
CREATE TABLE IF NOT EXISTS active_provider (
	tool TEXT PRIMARY KEY,
	provider_id TEXT,
	meta TEXT
);
CREATE TABLE IF NOT EXISTS usage_records (
	source TEXT,
	session_id TEXT,
	project TEXT,
	model TEXT,
	timestamp TEXT,
	input_tokens INTEGER,
	output_tokens INTEGER,
	cache_read_tokens INTEGER,
	cache_write_tokens INTEGER,
	tool TEXT,
	cost_usd REAL,
	host TEXT,
	PRIMARY KEY (source, session_id, timestamp, host)
);
CREATE TABLE IF NOT EXISTS settings (
	key TEXT PRIMARY KEY,
	value TEXT
);
CREATE TABLE IF NOT EXISTS memory_entries (
	id TEXT PRIMARY KEY,
	scope TEXT NOT NULL,
	content TEXT NOT NULL,
	tags TEXT,
	meta TEXT,
	created_at TEXT,
	updated_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_memory_scope ON memory_entries(scope);
CREATE TABLE IF NOT EXISTS mcp_servers (
	name TEXT PRIMARY KEY,
	transport TEXT NOT NULL,
	command TEXT,
	args TEXT,
	url TEXT,
	env TEXT,
	enabled INTEGER DEFAULT 0
);
CREATE TABLE IF NOT EXISTS guard_policies (
	id TEXT PRIMARY KEY,
	tool TEXT NOT NULL,
	action TEXT,
	decision TEXT NOT NULL,
	priority INTEGER DEFAULT 0
);
CREATE TABLE IF NOT EXISTS agent_instances (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	runtime_id TEXT NOT NULL,
	work_dir TEXT,
	system_prompt TEXT,
	provider_tool TEXT,
	provider_id TEXT,
	default_model TEXT,
	default_reasoning_effort TEXT,
	default_service_tier TEXT,
	memory_scope TEXT,
	env TEXT,
	channel_bindings TEXT,
	schedules TEXT,
	mcp_servers TEXT,
	skills TEXT,
	clis TEXT,
	enabled INTEGER DEFAULT 1,
	source TEXT,
	created_at TEXT,
	updated_at TEXT
);
CREATE TABLE IF NOT EXISTS proxy_config (
	tool TEXT PRIMARY KEY,
	enabled INTEGER DEFAULT 0,
	auto_failover INTEGER DEFAULT 0,
	max_retries INTEGER DEFAULT 3,
	failure_threshold INTEGER DEFAULT 4,
	cooldown_seconds INTEGER DEFAULT 60
);
CREATE TABLE IF NOT EXISTS proxy_live_backup (
	tool TEXT PRIMARY KEY,
	original_config TEXT NOT NULL,
	backed_up_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS proxy_traces (
	id TEXT PRIMARY KEY,
	timestamp TEXT NOT NULL,
	tool TEXT,
	provider_id TEXT,
	provider_name TEXT,
	client_protocol TEXT,
	upstream_protocol TEXT,
	client_model TEXT,
	upstream_model TEXT,
	status_code INTEGER,
	success INTEGER DEFAULT 0,
	error TEXT,
	session_id TEXT,
	project_dir TEXT
);
CREATE INDEX IF NOT EXISTS idx_proxy_traces_time ON proxy_traces(timestamp);
CREATE INDEX IF NOT EXISTS idx_proxy_traces_tool_time ON proxy_traces(tool,timestamp);
CREATE INDEX IF NOT EXISTS idx_proxy_traces_session_time ON proxy_traces(session_id,timestamp);
CREATE TABLE IF NOT EXISTS channels (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	type TEXT NOT NULL,
	agent_id TEXT,
	config TEXT,
	enabled INTEGER DEFAULT 0,
	created_at TEXT,
	updated_at TEXT
);
CREATE TABLE IF NOT EXISTS triggers (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	kind TEXT NOT NULL,
	agent_id TEXT,
	channel_id TEXT,
	chat_id TEXT,
	cron_expr TEXT,
	prompt TEXT,
	event TEXT,
	action_type TEXT,
	action_target TEXT,
	token TEXT,
	session_mode TEXT,
	enabled INTEGER DEFAULT 0,
	last_run TEXT,
	last_status TEXT,
	last_error TEXT,
	created_at TEXT,
	updated_at TEXT
);
CREATE TABLE IF NOT EXISTS conversations (
	id TEXT PRIMARY KEY,
	scope TEXT NOT NULL,
	chat_id TEXT NOT NULL,
	chat_type TEXT,
	agent_id TEXT,
	work_dir TEXT,
	native_session_id TEXT,
	title TEXT,
	message_count INTEGER DEFAULT 0,
	created_at TEXT,
	updated_at TEXT,
	last_active_at TEXT,
	ended_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_conversations_scope ON conversations(scope);
CREATE UNIQUE INDEX IF NOT EXISTS idx_conversations_active
	ON conversations(scope, chat_id) WHERE ended_at IS NULL OR ended_at = '';`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	for _, col := range []struct {
		name string
		def  string
	}{
		{"category", "TEXT"},
		{"settings_config", "TEXT"},
		{"meta", "TEXT"},
		{"in_failover_queue", "INTEGER DEFAULT 0"},
		{"sort_index", "INTEGER DEFAULT 0"},
	} {
		if err := s.ensureColumn("providers", col.name, col.def); err != nil {
			return err
		}
	}
	if err := s.ensureColumn("agent_instances", "default_model", "TEXT"); err != nil {
		return err
	}
	if err := s.ensureColumn("agent_instances", "default_reasoning_effort", "TEXT"); err != nil {
		return err
	}
	if err := s.ensureColumn("agent_instances", "default_service_tier", "TEXT"); err != nil {
		return err
	}
	if err := s.ensureColumn("agent_instances", "clis", "TEXT"); err != nil {
		return err
	}
	if err := s.ensureColumn("active_provider", "meta", "TEXT"); err != nil {
		return err
	}
	for _, col := range []struct {
		name string
		def  string
	}{
		{"conversation_id", "TEXT"},
		{"trace_id", "TEXT"},
		{"turn_id", "TEXT"},
		{"request_id", "TEXT"},
		{"runtime_id", "TEXT"},
	} {
		if err := s.ensureColumn("usage_records", col.name, col.def); err != nil {
			return err
		}
	}
	if _, err := s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_usage_request
		ON usage_records(source,request_id,host) WHERE request_id IS NOT NULL AND request_id<>''`); err != nil {
		return err
	}
	for _, col := range []struct {
		name string
		def  string
	}{
		{"request_id", "TEXT"},
		{"trace_id", "TEXT"},
		{"parent_span_id", "TEXT"},
		{"attempt", "INTEGER DEFAULT 0"},
		{"parent_attempt_id", "TEXT"},
		{"started_at", "TEXT"},
		{"ttft_ms", "INTEGER DEFAULT 0"},
		{"duration_ms", "INTEGER DEFAULT 0"},
		{"stream_complete", "INTEGER DEFAULT 0"},
		{"finish_reason", "TEXT"},
		{"input_tokens", "INTEGER DEFAULT 0"},
		{"output_tokens", "INTEGER DEFAULT 0"},
		{"cache_read_tokens", "INTEGER DEFAULT 0"},
		{"cache_write_tokens", "INTEGER DEFAULT 0"},
		{"request_bytes", "INTEGER DEFAULT 0"},
		{"response_bytes", "INTEGER DEFAULT 0"},
		{"cost_usd", "REAL DEFAULT 0"},
	} {
		if err := s.ensureColumn("proxy_traces", col.name, col.def); err != nil {
			return err
		}
	}
	if err := s.migrateObservations(); err != nil {
		return err
	}
	return s.migrateAgentBindings()
}

func (s *Store) ensureColumn(table, name, def string) error {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var colName, colType string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &colName, &colType, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if colName == name {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + name + ` ` + def)
	return err
}

// SetSetting stores a key/value setting.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings(key,value) VALUES(?,?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// GetSetting retrieves a setting; ok is false if absent.
func (s *Store) GetSetting(ctx context.Context, key string) (value string, ok bool, err error) {
	row := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key)
	switch err := row.Scan(&value); err {
	case nil:
		return value, true, nil
	case sql.ErrNoRows:
		return "", false, nil
	default:
		return "", false, err
	}
}

// UpsertUsage inserts usage records, ignoring duplicates by primary key.
func (s *Store) UpsertUsage(ctx context.Context, recs []core.UsageRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR IGNORE INTO usage_records
		(source,session_id,conversation_id,trace_id,turn_id,request_id,runtime_id,project,model,timestamp,input_tokens,output_tokens,
		 cache_read_tokens,cache_write_tokens,tool,cost_usd,host)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range recs {
		if _, err := stmt.ExecContext(ctx, r.Source, r.SessionID, r.ConversationID, r.TraceID, r.TurnID, r.RequestID, r.RuntimeID, r.Project, r.Model,
			r.Timestamp.Format(time.RFC3339Nano), r.InputTokens, r.OutputTokens,
			r.CacheReadTokens, r.CacheWriteTokens, r.Tool, r.CostUSD, r.Host); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// QueryUsage returns usage records since the given time (zero = all).
func (s *Store) QueryUsage(ctx context.Context, since time.Time) ([]core.UsageRecord, error) {
	q := `SELECT source,session_id,conversation_id,trace_id,turn_id,request_id,runtime_id,project,model,timestamp,input_tokens,output_tokens,
		cache_read_tokens,cache_write_tokens,tool,cost_usd,host FROM usage_records`
	args := []any{}
	if !since.IsZero() {
		q += ` WHERE timestamp >= ?`
		args = append(args, since.Format(time.RFC3339Nano))
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.UsageRecord
	for rows.Next() {
		var r core.UsageRecord
		var ts string
		if err := rows.Scan(&r.Source, &r.SessionID, &r.ConversationID, &r.TraceID, &r.TurnID, &r.RequestID, &r.RuntimeID, &r.Project, &r.Model, &ts,
			&r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheWriteTokens,
			&r.Tool, &r.CostUSD, &r.Host); err != nil {
			return nil, err
		}
		r.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		out = append(out, r)
	}
	return out, rows.Err()
}
