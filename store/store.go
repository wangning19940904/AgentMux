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
	provider_id TEXT
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
	memory_scope TEXT,
	env TEXT,
	channel_bindings TEXT,
	schedules TEXT,
	mcp_servers TEXT,
	skills TEXT,
	enabled INTEGER DEFAULT 1,
	source TEXT,
	created_at TEXT,
	updated_at TEXT
);`
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
	} {
		if err := s.ensureColumn("providers", col.name, col.def); err != nil {
			return err
		}
	}
	return nil
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
		(source,session_id,project,model,timestamp,input_tokens,output_tokens,
		 cache_read_tokens,cache_write_tokens,tool,cost_usd,host)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range recs {
		if _, err := stmt.ExecContext(ctx, r.Source, r.SessionID, r.Project, r.Model,
			r.Timestamp.Format(time.RFC3339Nano), r.InputTokens, r.OutputTokens,
			r.CacheReadTokens, r.CacheWriteTokens, r.Tool, r.CostUSD, r.Host); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// QueryUsage returns usage records since the given time (zero = all).
func (s *Store) QueryUsage(ctx context.Context, since time.Time) ([]core.UsageRecord, error) {
	q := `SELECT source,session_id,project,model,timestamp,input_tokens,output_tokens,
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
		if err := rows.Scan(&r.Source, &r.SessionID, &r.Project, &r.Model, &ts,
			&r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheWriteTokens,
			&r.Tool, &r.CostUSD, &r.Host); err != nil {
			return nil, err
		}
		r.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		out = append(out, r)
	}
	return out, rows.Err()
}
