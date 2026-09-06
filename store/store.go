// Package store provides AgentMux persistence. PostgreSQL is the production
// runtime store; SQLite remains available for migration and isolated
// compatibility tests.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	_ "modernc.org/sqlite"
)

// Store uses an isolated pool for observation writes so telemetry pressure
// cannot starve task, chat, configuration, or provider traffic.
type Store struct {
	db      *dbHandle
	writer  *dbHandle
	observe *dbHandle
	dialect Dialect
}

type statementExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// DefaultPath returns ~/.agentmux/agentmux.db.
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".agentmux", "agentmux.db")
}

// OpenLegacySQLite opens the retired SQLite store for offline migration and
// isolated compatibility tests. Daemon code must use OpenPostgres.
func OpenLegacySQLite(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir db dir: %w", err)
	}
	writer, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)&_txlock=immediate")
	if err != nil {
		return nil, fmt.Errorf("open db writer: %w", err)
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)

	// journal_mode is established by the single writer connection above. Do not
	// repeat that write-affecting PRAGMA whenever a reader connection opens.
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(30000)")
	if err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("open db reader: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	readHandle := &dbHandle{DB: db, dialect: DialectSQLite}
	writeHandle := &dbHandle{DB: writer, dialect: DialectSQLite}
	s := &Store{db: readHandle, writer: writeHandle, observe: writeHandle, dialect: DialectSQLite}
	if err := s.migrateSQLite(); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// OpenPostgres opens the PostgreSQL-only runtime store. Open remains available
// solely for legacy SQLite migration and isolated compatibility tests.
func OpenPostgres(ctx context.Context, cfg DatabaseConfig) (*Store, error) {
	defaults := DefaultDatabaseConfig()
	if strings.TrimSpace(cfg.URL) == "" {
		cfg.URL = defaults.URL
	}
	if cfg.MaxOpenConnections <= 0 {
		cfg.MaxOpenConnections = defaults.MaxOpenConnections
	}
	if cfg.MaxIdleConnections < 0 {
		cfg.MaxIdleConnections = defaults.MaxIdleConnections
	}
	if cfg.ConnectionMaxLifetime <= 0 {
		cfg.ConnectionMaxLifetime = defaults.ConnectionMaxLifetime
	}
	coreDB, err := sql.Open("pgx", cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("open postgres core pool: %w", err)
	}
	coreDB.SetMaxOpenConns(max(2, cfg.MaxOpenConnections-2))
	coreDB.SetMaxIdleConns(min(cfg.MaxIdleConnections, max(2, cfg.MaxOpenConnections-2)))
	coreDB.SetConnMaxLifetime(cfg.ConnectionMaxLifetime)
	observeDB, err := sql.Open("pgx", cfg.URL)
	if err != nil {
		_ = coreDB.Close()
		return nil, fmt.Errorf("open postgres observation pool: %w", err)
	}
	observeDB.SetMaxOpenConns(2)
	observeDB.SetMaxIdleConns(2)
	observeDB.SetConnMaxLifetime(cfg.ConnectionMaxLifetime)
	coreHandle := &dbHandle{DB: coreDB, dialect: DialectPostgres}
	observeHandle := &dbHandle{DB: observeDB, dialect: DialectPostgres}
	s := &Store{db: coreHandle, writer: coreHandle, observe: observeHandle, dialect: DialectPostgres}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := coreDB.PingContext(pingCtx); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("connect postgres %q: %w", redactDatabaseURL(cfg.URL), err)
	}
	if err := observeDB.PingContext(pingCtx); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("connect postgres observation pool: %w", err)
	}
	if err := s.migratePostgres(ctx); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func redactDatabaseURL(raw string) string {
	if before, after, ok := strings.Cut(raw, "://"); ok {
		if credentials, host, found := strings.Cut(after, "@"); found && strings.Contains(credentials, ":") {
			return before + "://***@" + host
		}
	}
	return raw
}

func (s *Store) IsPostgres() bool {
	return s != nil && s.dialect == DialectPostgres
}

// Close closes the database.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	var errs []error
	if s.db != nil {
		errs = append(errs, s.db.Close())
	}
	if s.writer != nil && s.writer != s.db {
		errs = append(errs, s.writer.Close())
	}
	if s.observe != nil && s.observe != s.db && s.observe != s.writer {
		errs = append(errs, s.observe.Close())
	}
	return errors.Join(errs...)
}

const sqliteCoreSchema = `
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
	provenance TEXT,
	provenance_rank INTEGER DEFAULT 0,
	token_quality TEXT DEFAULT 'exact',
	cost_kind TEXT DEFAULT 'calculated',
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
CREATE TABLE IF NOT EXISTS skill_states (
	name TEXT PRIMARY KEY,
	enabled INTEGER NOT NULL DEFAULT 1,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS agent_instances (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	runtime_id TEXT NOT NULL,
	desktop_thread_id TEXT,
	work_dir TEXT,
	workspace_mode TEXT,
	worktree_base_ref TEXT,
	session_backend TEXT,
	system_prompt TEXT,
	provider_tool TEXT,
	provider_id TEXT,
	default_model TEXT,
	default_reasoning_effort TEXT,
	default_service_tier TEXT,
	default_approval_mode TEXT,
	memory_scope TEXT,
	env TEXT,
	channel_bindings TEXT,
	schedules TEXT,
	mcp_servers TEXT,
	skills TEXT,
	clis TEXT,
	enabled INTEGER DEFAULT 1,
	source TEXT,
	owner_tenant_id TEXT,
	visibility TEXT,
	created_at TEXT,
	updated_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_agent_instances_owner ON agent_instances(owner_tenant_id);
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
	owner_tenant_id TEXT,
	visibility TEXT,
	created_at TEXT,
	updated_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_channels_owner ON channels(owner_tenant_id);
CREATE TABLE IF NOT EXISTS tenants (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	kind TEXT,
	status TEXT NOT NULL,
	note TEXT,
	created_at TEXT,
	updated_at TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_tenants_name ON tenants(name);
CREATE TABLE IF NOT EXISTS tenant_tokens (
	id TEXT PRIMARY KEY,
	tenant_id TEXT NOT NULL,
	name TEXT,
	token_hash TEXT NOT NULL,
	prefix TEXT,
	created_at TEXT,
	last_used_at TEXT,
	expires_at TEXT,
	revoked_at TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_tenant_tokens_hash ON tenant_tokens(token_hash);
CREATE INDEX IF NOT EXISTS idx_tenant_tokens_tenant ON tenant_tokens(tenant_id);
CREATE TABLE IF NOT EXISTS resource_grants (
	tenant_id TEXT NOT NULL,
	resource_type TEXT NOT NULL,
	resource_id TEXT NOT NULL,
	level TEXT NOT NULL,
	created_at TEXT,
	updated_at TEXT,
	PRIMARY KEY (tenant_id, resource_type, resource_id)
);
CREATE INDEX IF NOT EXISTS idx_resource_grants_lookup ON resource_grants(tenant_id, resource_type);
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
	owner_tenant_id TEXT,
	created_at TEXT,
	updated_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_triggers_owner ON triggers(owner_tenant_id);
CREATE TABLE IF NOT EXISTS conversations (
	id TEXT PRIMARY KEY,
	scope TEXT NOT NULL,
	conversation_key TEXT,
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
CREATE TABLE IF NOT EXISTS channel_chat_state (channel_id TEXT NOT NULL,state_key TEXT NOT NULL,value TEXT NOT NULL,PRIMARY KEY(channel_id,state_key));
CREATE TABLE IF NOT EXISTS channel_tasks (
	id TEXT PRIMARY KEY,
	channel_id TEXT NOT NULL,
	conversation_id TEXT,
	conversation_key TEXT NOT NULL,
	chat_id TEXT,
	message_id TEXT,
	chat_type TEXT,
	root_id TEXT,
	thread_id TEXT,
	user_id TEXT,
	controller_id TEXT,
	native_thread_id TEXT,
	turn_id TEXT,
	status TEXT NOT NULL,
	error TEXT,
	delivery_key TEXT,
	delivery_status TEXT,
	delivery_attempts INTEGER DEFAULT 0,
	delivery_error TEXT,
	delivered_at TEXT,
	feedback_nonce TEXT,
	prompt TEXT,
	created_at TEXT,
	started_at TEXT,
	finished_at TEXT,
	updated_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_channel_tasks_conversation
	ON channel_tasks(channel_id, conversation_key, created_at);
CREATE INDEX IF NOT EXISTS idx_channel_tasks_status
	ON channel_tasks(channel_id, status, created_at);
CREATE TABLE IF NOT EXISTS channel_interactions (
	id TEXT PRIMARY KEY,
	task_id TEXT NOT NULL,
	channel_id TEXT NOT NULL,
	conversation_id TEXT,
	conversation_key TEXT NOT NULL,
	controller_id TEXT,
	nonce TEXT NOT NULL,
	message_id TEXT,
	status TEXT NOT NULL,
	request TEXT NOT NULL,
	created_at TEXT,
	expires_at TEXT,
	resolved_at TEXT,
	resolved_by TEXT
);
CREATE INDEX IF NOT EXISTS idx_channel_interactions_pending
	ON channel_interactions(channel_id, conversation_key, status, created_at);
CREATE TABLE IF NOT EXISTS channel_feedback (
	id TEXT PRIMARY KEY,
	task_id TEXT NOT NULL,
	channel_id TEXT NOT NULL,
	conversation_id TEXT,
	user_id TEXT NOT NULL,
	semantic TEXT NOT NULL,
	reason TEXT,
	comment TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE(task_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_channel_feedback_channel_updated
	ON channel_feedback(channel_id, updated_at);
CREATE TABLE IF NOT EXISTS orchestrations (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	status TEXT NOT NULL,
	max_concurrency INTEGER NOT NULL,
	error TEXT,
	owner_tenant_id TEXT,
	created_at TEXT NOT NULL,
	started_at TEXT,
	finished_at TEXT,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_orchestrations_owner ON orchestrations(owner_tenant_id);
CREATE TABLE IF NOT EXISTS orchestration_tasks (
	orchestration_id TEXT NOT NULL,
	id TEXT NOT NULL,
	agent_id TEXT,
	project TEXT,
	input TEXT NOT NULL,
	depends_on TEXT,
	status TEXT NOT NULL,
	output TEXT,
	error TEXT,
	invocation_id TEXT,
	conversation_id TEXT,
	created_at TEXT NOT NULL,
	started_at TEXT,
	finished_at TEXT,
	updated_at TEXT NOT NULL,
	PRIMARY KEY(orchestration_id,id)
);
CREATE INDEX IF NOT EXISTS idx_orchestrations_status_updated
	ON orchestrations(status,updated_at);
CREATE INDEX IF NOT EXISTS idx_orchestration_tasks_status
	ON orchestration_tasks(orchestration_id,status);`

func (s *Store) migrateSQLite() error {
	if _, err := s.writer.Exec(sqliteCoreSchema); err != nil {
		return err
	}
	// Registration codes were removed in contract 1.2. Drop the retired table
	// during legacy SQLite upgrades so stale secrets cannot remain usable.
	if _, err := s.writer.Exec(`DROP TABLE IF EXISTS tenant_enrollments`); err != nil {
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
	if err := s.ensureColumn("agent_instances", "desktop_thread_id", "TEXT"); err != nil {
		return err
	}
	if err := s.ensureColumn("agent_instances", "workspace_mode", "TEXT"); err != nil {
		return err
	}
	if err := s.ensureColumn("agent_instances", "worktree_base_ref", "TEXT"); err != nil {
		return err
	}
	if err := s.ensureColumn("agent_instances", "session_backend", "TEXT"); err != nil {
		return err
	}
	if err := s.ensureColumn("agent_instances", "default_reasoning_effort", "TEXT"); err != nil {
		return err
	}
	if err := s.ensureColumn("agent_instances", "default_service_tier", "TEXT"); err != nil {
		return err
	}
	if err := s.ensureColumn("agent_instances", "default_approval_mode", "TEXT"); err != nil {
		return err
	}
	if err := s.ensureColumn("agent_instances", "clis", "TEXT"); err != nil {
		return err
	}
	// Tenancy ownership columns (see core/tenancy.go).
	for _, owned := range []struct {
		table   string
		columns []string
	}{
		{"agent_instances", []string{"owner_tenant_id", "visibility"}},
		{"channels", []string{"owner_tenant_id", "visibility"}},
		{"triggers", []string{"owner_tenant_id"}},
		{"orchestrations", []string{"owner_tenant_id"}},
	} {
		for _, column := range owned.columns {
			if err := s.ensureColumn(owned.table, column, "TEXT"); err != nil {
				return err
			}
		}
	}
	if err := s.ensureColumn("active_provider", "meta", "TEXT"); err != nil {
		return err
	}
	if err := s.ensureColumn("conversations", "conversation_key", "TEXT"); err != nil {
		return err
	}
	if err := s.ensureColumn("channel_interactions", "message_id", "TEXT"); err != nil {
		return err
	}
	for _, column := range []struct {
		name string
		def  string
	}{
		{"message_id", "TEXT"},
		{"chat_type", "TEXT"},
		{"root_id", "TEXT"},
		{"thread_id", "TEXT"},
		{"delivery_key", "TEXT"},
		{"delivery_status", "TEXT"},
		{"delivery_attempts", "INTEGER DEFAULT 0"},
		{"delivery_error", "TEXT"},
		{"delivered_at", "TEXT"},
		{"feedback_nonce", "TEXT"},
		{"control_json", "TEXT"},
		{"source_message_id", "TEXT"},
	} {
		if err := s.ensureColumn("channel_tasks", column.name, column.def); err != nil {
			return err
		}
	}
	if _, err := s.writer.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_channel_tasks_delivery_key
		ON channel_tasks(delivery_key) WHERE delivery_key IS NOT NULL AND delivery_key <> ''`); err != nil {
		return err
	}
	if _, err := s.writer.Exec(`UPDATE conversations
		SET conversation_key=CASE
			WHEN chat_id LIKE 'chat:%' OR chat_id LIKE 'thread:%' OR chat_id LIKE 'root:%' THEN chat_id
			ELSE 'chat:' || chat_id
		END
		WHERE conversation_key IS NULL OR conversation_key=''`); err != nil {
		return err
	}
	if _, err := s.writer.Exec(`DROP INDEX IF EXISTS idx_conversations_active`); err != nil {
		return err
	}
	if _, err := s.writer.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_conversations_active
		ON conversations(scope, conversation_key) WHERE ended_at IS NULL OR ended_at = ''`); err != nil {
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
		{"provenance", "TEXT"},
		{"provenance_rank", "INTEGER DEFAULT 0"},
		{"token_quality", "TEXT DEFAULT 'exact'"},
		{"cost_kind", "TEXT DEFAULT 'calculated'"},
	} {
		if err := s.ensureColumn("usage_records", col.name, col.def); err != nil {
			return err
		}
	}
	if _, err := s.writer.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_usage_request
		ON usage_records(source,request_id,host) WHERE request_id IS NOT NULL AND request_id<>''`); err != nil {
		return err
	}
	if _, err := s.writer.Exec(`CREATE INDEX IF NOT EXISTS idx_usage_timestamp ON usage_records(timestamp)`); err != nil {
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
	rows, err := s.writer.Query(`PRAGMA table_info(` + table + `)`)
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
	_, err = s.writer.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + name + ` ` + def)
	return err
}

// SetSetting stores a key/value setting.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.writer.ExecContext(ctx,
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

// UpsertUsage inserts usage records. Request-addressable records may be
// upgraded by a higher-ranked source (for example Cursor dashboard data
// replacing a local estimate); legacy transcript identities remain immutable.
func (s *Store) UpsertUsage(ctx context.Context, recs []core.UsageRecord) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	requestStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO usage_records
		(source,session_id,conversation_id,trace_id,turn_id,request_id,runtime_id,project,model,timestamp,input_tokens,output_tokens,
		 cache_read_tokens,cache_write_tokens,tool,cost_usd,host,provenance,provenance_rank,token_quality,cost_kind)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(source,request_id,host) WHERE request_id IS NOT NULL AND request_id<>'' DO UPDATE SET
		 session_id=CASE WHEN excluded.session_id<>'' THEN excluded.session_id ELSE usage_records.session_id END,
		 conversation_id=CASE WHEN excluded.conversation_id<>'' THEN excluded.conversation_id ELSE usage_records.conversation_id END,
		 trace_id=CASE WHEN excluded.trace_id<>'' THEN excluded.trace_id ELSE usage_records.trace_id END,
		 turn_id=CASE WHEN excluded.turn_id<>'' THEN excluded.turn_id ELSE usage_records.turn_id END,
		 runtime_id=CASE WHEN excluded.runtime_id<>'' THEN excluded.runtime_id ELSE usage_records.runtime_id END,
		 project=CASE WHEN excluded.project<>'' THEN excluded.project ELSE usage_records.project END,
		 model=CASE WHEN excluded.model<>'' THEN excluded.model ELSE usage_records.model END,
		 timestamp=excluded.timestamp,input_tokens=excluded.input_tokens,output_tokens=excluded.output_tokens,
		 cache_read_tokens=excluded.cache_read_tokens,cache_write_tokens=excluded.cache_write_tokens,
		 tool=excluded.tool,cost_usd=excluded.cost_usd,provenance=excluded.provenance,
		 provenance_rank=excluded.provenance_rank,token_quality=excluded.token_quality,cost_kind=excluded.cost_kind
		WHERE usage_records.provenance_rank<=excluded.provenance_rank`)
	if err != nil {
		return err
	}
	defer requestStmt.Close()
	legacyStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO usage_records
		(source,session_id,conversation_id,trace_id,turn_id,request_id,runtime_id,project,model,timestamp,input_tokens,output_tokens,
		 cache_read_tokens,cache_write_tokens,tool,cost_usd,host,provenance,provenance_rank,token_quality,cost_kind)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT DO NOTHING`)
	if err != nil {
		return err
	}
	defer legacyStmt.Close()
	for _, r := range recs {
		if r.TokenQuality == "" {
			r.TokenQuality = core.UsageTokenQualityExact
		}
		if r.CostKind == "" {
			r.CostKind = core.UsageCostKindCalculated
		}
		stmt := legacyStmt
		if strings.TrimSpace(r.RequestID) != "" {
			stmt = requestStmt
		}
		if _, err := stmt.ExecContext(ctx, r.Source, r.SessionID, r.ConversationID, r.TraceID, r.TurnID, r.RequestID, r.RuntimeID, r.Project, r.Model,
			r.Timestamp.UTC().Format(time.RFC3339Nano), r.InputTokens, r.OutputTokens,
			r.CacheReadTokens, r.CacheWriteTokens, r.Tool, r.CostUSD, r.Host,
			r.Provenance, r.ProvenanceRank, r.TokenQuality, r.CostKind); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// QueryUsage returns usage records since the given time (zero = all).
func (s *Store) QueryUsage(ctx context.Context, since time.Time) ([]core.UsageRecord, error) {
	return s.QueryUsageRange(ctx, since, time.Time{})
}

const usageRecordSelectColumns = `COALESCE(source,''),COALESCE(session_id,''),COALESCE(conversation_id,''),
	COALESCE(trace_id,''),COALESCE(turn_id,''),COALESCE(request_id,''),COALESCE(runtime_id,''),
	COALESCE(project,''),COALESCE(model,''),COALESCE(timestamp,''),COALESCE(input_tokens,0),
	COALESCE(output_tokens,0),COALESCE(cache_read_tokens,0),COALESCE(cache_write_tokens,0),
	COALESCE(tool,''),COALESCE(cost_usd,0),COALESCE(host,''),COALESCE(provenance,''),
	COALESCE(provenance_rank,0),COALESCE(token_quality,'exact'),COALESCE(cost_kind,'calculated')`

// QueryUsageRequestIndex returns request-addressable records for one source.
// Cursor cloud reconciliation uses this to enrich exact API events with the
// local conversation and project without reading message content.
func (s *Store) QueryUsageRequestIndex(ctx context.Context, source string, since time.Time) (map[string]core.UsageRecord, error) {
	query := `SELECT ` + usageRecordSelectColumns + `
		FROM usage_records WHERE source=? AND request_id IS NOT NULL AND request_id<>''`
	args := []any{source}
	if !since.IsZero() {
		query += ` AND timestamp>=?`
		args = append(args, since.UTC().Format(time.RFC3339Nano))
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]core.UsageRecord{}
	for rows.Next() {
		var record core.UsageRecord
		var timestamp string
		if err := rows.Scan(&record.Source, &record.SessionID, &record.ConversationID, &record.TraceID, &record.TurnID,
			&record.RequestID, &record.RuntimeID, &record.Project, &record.Model, &timestamp,
			&record.InputTokens, &record.OutputTokens, &record.CacheReadTokens, &record.CacheWriteTokens,
			&record.Tool, &record.CostUSD, &record.Host, &record.Provenance, &record.ProvenanceRank,
			&record.TokenQuality, &record.CostKind); err != nil {
			return nil, err
		}
		record.Timestamp, _ = time.Parse(time.RFC3339Nano, timestamp)
		result[record.RequestID] = record
	}
	return result, rows.Err()
}

// QueryUsageRange returns canonical usage records inside [since, until).
func (s *Store) QueryUsageRange(ctx context.Context, since, until time.Time) ([]core.UsageRecord, error) {
	q := `SELECT ` + usageRecordSelectColumns + ` FROM usage_records`
	args := []any{}
	var filters []string
	if !since.IsZero() {
		filters = append(filters, `timestamp >= ?`)
		args = append(args, since.UTC().Format(time.RFC3339Nano))
	}
	if !until.IsZero() {
		filters = append(filters, `timestamp < ?`)
		args = append(args, until.UTC().Format(time.RFC3339Nano))
	}
	if len(filters) > 0 {
		q += ` WHERE ` + strings.Join(filters, ` AND `)
	}
	q += ` ORDER BY timestamp,source,session_id`
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
			&r.Tool, &r.CostUSD, &r.Host, &r.Provenance, &r.ProvenanceRank, &r.TokenQuality, &r.CostKind); err != nil {
			return nil, err
		}
		r.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		out = append(out, r)
	}
	return out, rows.Err()
}
