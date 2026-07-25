package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

type SQLiteMigrationOptions struct {
	Source            string
	ObservationsSince time.Time
	BackupPath        string
	BatchSize         int
	DryRun            bool
	Resume            bool
}

type SQLiteMigrationTable struct {
	Name     string `json:"name"`
	Selected int64  `json:"selected"`
	Copied   int64  `json:"copied"`
	Verified int64  `json:"verified"`
}

type SQLiteMigrationReport struct {
	Source      string                 `json:"source"`
	Backup      string                 `json:"backup,omitempty"`
	SourceBytes int64                  `json:"source_bytes"`
	DryRun      bool                   `json:"dry_run"`
	Tables      []SQLiteMigrationTable `json:"tables"`
	StartedAt   time.Time              `json:"started_at"`
	FinishedAt  time.Time              `json:"finished_at"`
}

var sqliteMigrationTables = []string{
	"providers", "active_provider", "usage_records", "settings", "memory_entries", "mcp_servers",
	"guard_policies", "agent_instances", "proxy_config", "proxy_live_backup", "proxy_traces",
	"channels", "triggers", "conversations", "channel_tasks", "channel_interactions",
	"observation_traces", "observation_spans", "observation_events", "observation_data_keys",
	"observation_payloads", "observation_payload_chunks", "observation_daily_usage",
	"observation_ingest_cursors", "observation_export_outbox", "observation_insights",
	"observation_integration_ownership", "observation_resource_leases",
}

// MigrateSQLite streams a stopped legacy SQLite store into an empty PostgreSQL
// schema. The source is never modified; the non-dry-run path first creates a
// consistent VACUUM INTO snapshot that includes committed WAL content.
func (s *Store) MigrateSQLite(ctx context.Context, options SQLiteMigrationOptions) (SQLiteMigrationReport, error) {
	report := SQLiteMigrationReport{Source: options.Source, DryRun: options.DryRun, StartedAt: time.Now().UTC()}
	if !s.IsPostgres() {
		return report, errors.New("sqlite migration target must be PostgreSQL")
	}
	source, err := filepath.Abs(strings.TrimSpace(options.Source))
	if err != nil || source == "" {
		return report, errors.New("sqlite migration source is required")
	}
	info, err := os.Stat(source)
	if err != nil {
		return report, fmt.Errorf("stat sqlite source: %w", err)
	}
	report.Source, report.SourceBytes = source, info.Size()
	if options.BatchSize <= 0 || options.BatchSize > 5000 {
		options.BatchSize = 5000
	}
	if options.ObservationsSince.IsZero() {
		options.ObservationsSince = time.Now().UTC().Add(-30 * 24 * time.Hour)
	}
	sourceDB, err := sql.Open("sqlite", source+"?_pragma=busy_timeout(30000)&_pragma=query_only(1)")
	if err != nil {
		return report, err
	}
	defer func() { _ = sourceDB.Close() }()
	if err := sourceDB.PingContext(ctx); err != nil {
		return report, fmt.Errorf("open sqlite source: %w", err)
	}

	collectTables := func() error {
		report.Tables = nil
		for _, table := range sqliteMigrationTables {
			exists, err := sqliteTableExists(ctx, sourceDB, table)
			if err != nil {
				return err
			}
			if !exists {
				continue
			}
			where, args := sqliteMigrationFilter(table, options.ObservationsSince)
			var count int64
			if err := sourceDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteIdentifier(table)+where, args...).Scan(&count); err != nil {
				return fmt.Errorf("count sqlite table %s: %w", table, err)
			}
			report.Tables = append(report.Tables, SQLiteMigrationTable{Name: table, Selected: count})
		}
		return nil
	}
	if err := collectTables(); err != nil {
		return report, err
	}
	if options.DryRun {
		report.FinishedAt = time.Now().UTC()
		return report, nil
	}
	if !options.Resume {
		if err := s.requireEmptyMigrationTarget(ctx); err != nil {
			return report, err
		}
		if _, err := s.writer.ExecContext(ctx, `DELETE FROM settings WHERE key='channels_triggers_migrated'`); err != nil {
			return report, err
		}
	}
	backupPath := options.BackupPath
	if strings.TrimSpace(backupPath) == "" {
		backupPath = source + ".backup-" + time.Now().UTC().Format("20060102T150405Z")
	}
	backupPath, err = filepath.Abs(backupPath)
	if err != nil {
		return report, err
	}
	if _, err := os.Stat(backupPath); err == nil {
		if strings.TrimSpace(options.BackupPath) == "" {
			return report, fmt.Errorf("sqlite backup already exists: %s", backupPath)
		}
		report.Backup = backupPath
	} else if !os.IsNotExist(err) {
		return report, err
	} else {
		if err := createSQLiteBackup(ctx, source, backupPath); err != nil {
			return report, err
		}
		report.Backup = backupPath
	}
	if err := sourceDB.Close(); err != nil {
		return report, err
	}
	sourceDB, err = sql.Open("sqlite", backupPath+"?_pragma=busy_timeout(30000)&_pragma=query_only(1)")
	if err != nil {
		return report, err
	}
	if err := sourceDB.PingContext(ctx); err != nil {
		return report, fmt.Errorf("open sqlite migration backup: %w", err)
	}
	if err := collectTables(); err != nil {
		return report, err
	}

	for index := range report.Tables {
		item := &report.Tables[index]
		if item.Selected == 0 {
			continue
		}
		columns, err := sqlitePostgresCommonColumns(ctx, sourceDB, s.db.DB, item.Name)
		if err != nil {
			return report, err
		}
		if len(columns) == 0 {
			return report, fmt.Errorf("no common columns for %s", item.Name)
		}
		where, args := sqliteMigrationFilter(item.Name, options.ObservationsSince)
		query := `SELECT ` + joinQuotedIdentifiers(columns) + ` FROM ` + quoteIdentifier(item.Name) + where
		rows, err := sourceDB.QueryContext(ctx, query, args...)
		if err != nil {
			return report, fmt.Errorf("read sqlite table %s: %w", item.Name, err)
		}
		batch := make([][]any, 0, options.BatchSize)
		for rows.Next() {
			values := make([]any, len(columns))
			dest := make([]any, len(columns))
			for column := range values {
				dest[column] = &values[column]
			}
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				return report, fmt.Errorf("scan sqlite table %s: %w", item.Name, err)
			}
			for column, value := range values {
				if bytesValue, ok := value.([]byte); ok && bytesValue == nil {
					// modernc SQLite represents a zero-length BLOB as a typed
					// nil slice. pgx otherwise encodes that as SQL NULL.
					values[column] = []byte{}
				}
			}
			batch = append(batch, values)
			if len(batch) == options.BatchSize {
				copied, err := s.copyPostgresBatch(ctx, item.Name, columns, batch)
				if err != nil {
					rows.Close()
					return report, err
				}
				item.Copied += copied
				batch = batch[:0]
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return report, err
		}
		rows.Close()
		if len(batch) > 0 {
			copied, err := s.copyPostgresBatch(ctx, item.Name, columns, batch)
			if err != nil {
				return report, err
			}
			item.Copied += copied
		}
	}
	for index := range report.Tables {
		item := &report.Tables[index]
		if item.Selected == 0 {
			continue
		}
		where, args := sqliteMigrationFilter(item.Name, options.ObservationsSince)
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteIdentifier(item.Name)+where, args...).Scan(&item.Verified); err != nil {
			return report, fmt.Errorf("verify postgres table %s: %w", item.Name, err)
		}
		if item.Verified < item.Selected {
			return report, fmt.Errorf("verify %s: selected %d rows, PostgreSQL has %d", item.Name, item.Selected, item.Verified)
		}
	}
	if _, err := s.writer.ExecContext(ctx, `ANALYZE`); err != nil {
		return report, err
	}
	if err := s.migrateAgentBindings(); err != nil {
		return report, err
	}
	report.FinishedAt = time.Now().UTC()
	return report, nil
}

func sqliteMigrationFilter(table string, since time.Time) (string, []any) {
	cutoff := observationTime(since.UTC())
	traceSelection := `SELECT trace_id FROM observation_traces
		WHERE started_at>=? OR status IN ('running','unset')
		OR trace_id IN (SELECT trace_id FROM observation_events WHERE timestamp>=?)
		OR trace_id IN (SELECT trace_id FROM observation_spans WHERE started_at>=?)`
	traceArgs := func() []any { return []any{cutoff, cutoff, cutoff} }
	switch table {
	case "observation_traces":
		return ` WHERE trace_id IN (` + traceSelection + `)`, traceArgs()
	case "observation_spans":
		return ` WHERE trace_id IN (` + traceSelection + `)`, traceArgs()
	case "observation_events":
		return ` WHERE trace_id IN (` + traceSelection + `)`, traceArgs()
	case "observation_payloads":
		return ` WHERE payload_id IN (
			SELECT payload_id FROM observation_events WHERE payload_id<>'' AND trace_id IN (` + traceSelection + `)
			UNION SELECT payload_id FROM observation_spans WHERE payload_id<>'' AND trace_id IN (` + traceSelection + `)
		)`, append(traceArgs(), traceArgs()...)
	case "observation_payload_chunks":
		return ` WHERE payload_id IN (
			SELECT payload_id FROM observation_events WHERE payload_id<>'' AND trace_id IN (` + traceSelection + `)
			UNION SELECT payload_id FROM observation_spans WHERE payload_id<>'' AND trace_id IN (` + traceSelection + `)
		)`, append(traceArgs(), traceArgs()...)
	case "observation_data_keys":
		return ` WHERE key_id IN (
			SELECT key_id FROM observation_payloads WHERE payload_id IN (
				SELECT payload_id FROM observation_events WHERE payload_id<>'' AND trace_id IN (` + traceSelection + `)
			)
		)`, traceArgs()
	case "observation_daily_usage":
		return ` WHERE day>=?`, []any{since.UTC().Format("2006-01-02")}
	case "observation_export_outbox", "observation_insights":
		return ` WHERE created_at>=?`, []any{cutoff}
	default:
		return "", nil
	}
}

func sqliteTableExists(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var exists bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)`, table).Scan(&exists)
	return exists, err
}

func createSQLiteBackup(ctx context.Context, source, destination string) error {
	db, err := sql.Open("sqlite", source+"?_pragma=busy_timeout(30000)")
	if err != nil {
		return err
	}
	defer db.Close()
	escaped := strings.ReplaceAll(destination, "'", "''")
	if _, err := db.ExecContext(ctx, `VACUUM INTO '`+escaped+`'`); err != nil {
		return fmt.Errorf("create consistent sqlite backup: %w", err)
	}
	return nil
}

func sqlitePostgresCommonColumns(ctx context.Context, source, target *sql.DB, table string) ([]string, error) {
	rows, err := source.QueryContext(ctx, `PRAGMA table_info(`+quoteIdentifier(table)+`)`)
	if err != nil {
		return nil, err
	}
	sourceColumns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return nil, err
		}
		sourceColumns[name] = true
	}
	rows.Close()
	targetRows, err := target.QueryContext(ctx, `SELECT column_name FROM information_schema.columns
		WHERE table_schema=current_schema() AND table_name=$1 ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, err
	}
	defer targetRows.Close()
	var columns []string
	for targetRows.Next() {
		var name string
		if err := targetRows.Scan(&name); err != nil {
			return nil, err
		}
		if sourceColumns[name] {
			columns = append(columns, name)
		}
	}
	return columns, targetRows.Err()
}

func (s *Store) copyPostgresBatch(ctx context.Context, table string, columns []string, rows [][]any) (int64, error) {
	sqlConn, err := s.writer.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer sqlConn.Close()
	var copied int64
	err = sqlConn.Raw(func(driverConn any) error {
		conn, ok := driverConn.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("unexpected PostgreSQL driver connection %T", driverConn)
		}
		tx, err := conn.Conn().Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		stage := "amux_migrate_" + strings.ReplaceAll(table, "-", "_")
		if _, err := tx.Exec(ctx, `CREATE TEMP TABLE `+quoteIdentifier(stage)+` ON COMMIT DROP AS
			SELECT `+joinQuotedIdentifiers(columns)+` FROM `+quoteIdentifier(table)+` WITH NO DATA`); err != nil {
			return err
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{stage}, columns, pgx.CopyFromRows(rows)); err != nil {
			return fmt.Errorf("copy sqlite batch into %s staging: %w", table, err)
		}
		command, err := tx.Exec(ctx, `INSERT INTO `+quoteIdentifier(table)+` (`+joinQuotedIdentifiers(columns)+`)
			SELECT `+joinQuotedIdentifiers(columns)+` FROM `+quoteIdentifier(stage)+` ON CONFLICT DO NOTHING`)
		if err != nil {
			return fmt.Errorf("merge sqlite batch into %s: %w", table, err)
		}
		copied = command.RowsAffected()
		return tx.Commit(ctx)
	})
	return copied, err
}

func (s *Store) requireEmptyMigrationTarget(ctx context.Context) error {
	for _, table := range sqliteMigrationTables {
		var exists bool
		if err := s.db.QueryRowContext(ctx, `SELECT to_regclass(?) IS NOT NULL`, table).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			continue
		}
		var count int
		query := `SELECT COUNT(*) FROM ` + quoteIdentifier(table)
		if table == "settings" {
			query += ` WHERE key<>'channels_triggers_migrated'`
		}
		if err := s.db.QueryRowContext(ctx, query).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return fmt.Errorf("PostgreSQL target is not empty (%s has rows); refusing a non-atomic merge", table)
		}
	}
	return nil
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func joinQuotedIdentifiers(values []string) string {
	quoted := make([]string, len(values))
	for index, value := range values {
		quoted[index] = quoteIdentifier(value)
	}
	return strings.Join(quoted, ",")
}

func sortMigrationTables(report *SQLiteMigrationReport) {
	sort.SliceStable(report.Tables, func(i, j int) bool { return report.Tables[i].Name < report.Tables[j].Name })
}
