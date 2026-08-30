package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const DefaultPostgresURL = "postgresql:///agentmux?host=/tmp&sslmode=disable"

type Dialect string

const (
	DialectSQLite   Dialect = "sqlite"
	DialectPostgres Dialect = "postgres"
)

// DatabaseConfig controls the PostgreSQL pools used by the runtime store.
type DatabaseConfig struct {
	URL                   string
	MaxOpenConnections    int
	MaxIdleConnections    int
	ConnectionMaxLifetime time.Duration
}

func DefaultDatabaseConfig() DatabaseConfig {
	return DatabaseConfig{
		URL:                   DefaultPostgresURL,
		MaxOpenConnections:    12,
		MaxIdleConnections:    4,
		ConnectionMaxLifetime: 30 * time.Minute,
	}
}

type DatabaseStatus struct {
	Driver                 string        `json:"driver"`
	Ready                  bool          `json:"ready"`
	OpenConnections        int           `json:"open_connections"`
	InUseConnections       int           `json:"in_use_connections"`
	IdleConnections        int           `json:"idle_connections"`
	ObservationConnections int           `json:"observation_connections"`
	PingLatency            time.Duration `json:"ping_latency"`
	Error                  string        `json:"error,omitempty"`
}

func (s *Store) DatabaseStatus(ctx context.Context) DatabaseStatus {
	status := DatabaseStatus{}
	if s == nil || s.db == nil {
		status.Error = "store unavailable"
		return status
	}
	status.Driver = string(s.dialect)
	stats := s.db.Stats()
	status.OpenConnections = stats.OpenConnections
	status.InUseConnections = stats.InUse
	status.IdleConnections = stats.Idle
	if s.observe != nil {
		status.ObservationConnections = s.observe.Stats().OpenConnections
	}
	started := time.Now()
	pingCtx, cancel := context.WithTimeout(ctx, time.Second)
	err := s.db.PingContext(pingCtx)
	cancel()
	status.PingLatency = time.Since(started)
	status.Ready = err == nil
	if err != nil {
		status.Error = err.Error()
	}
	return status
}

// dbHandle keeps the existing Store implementation compact while allowing its
// SQLite-style placeholders to be safely rebound for PostgreSQL.
type dbHandle struct {
	*sql.DB
	dialect Dialect
}

func (d *dbHandle) query(query string) string {
	if d != nil && d.dialect == DialectPostgres {
		return rebindPostgres(query)
	}
	return query
}

func (d *dbHandle) Exec(query string, args ...any) (sql.Result, error) {
	return d.DB.Exec(d.query(query), args...)
}

func (d *dbHandle) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.DB.ExecContext(ctx, d.query(query), args...)
}

func (d *dbHandle) Query(query string, args ...any) (*sql.Rows, error) {
	return d.DB.Query(d.query(query), args...)
}

func (d *dbHandle) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.DB.QueryContext(ctx, d.query(query), args...)
}

func (d *dbHandle) QueryRow(query string, args ...any) *sql.Row {
	return d.DB.QueryRow(d.query(query), args...)
}

func (d *dbHandle) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return d.DB.QueryRowContext(ctx, d.query(query), args...)
}

func (d *dbHandle) BeginTx(ctx context.Context, opts *sql.TxOptions) (*dbTx, error) {
	tx, err := d.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &dbTx{Tx: tx, dialect: d.dialect}, nil
}

type dbTx struct {
	*sql.Tx
	dialect Dialect
}

func (t *dbTx) query(query string) string {
	if t != nil && t.dialect == DialectPostgres {
		return rebindPostgres(query)
	}
	return query
}

func (t *dbTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.Tx.ExecContext(ctx, t.query(query), args...)
}

func (t *dbTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.Tx.QueryContext(ctx, t.query(query), args...)
}

func (t *dbTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return t.Tx.QueryRowContext(ctx, t.query(query), args...)
}

func (t *dbTx) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return t.Tx.PrepareContext(ctx, t.query(query))
}

// rebindPostgres replaces parameter markers outside quoted SQL literals.
func rebindPostgres(query string) string {
	var out strings.Builder
	out.Grow(len(query) + 16)
	parameter := 1
	inSingle, inDouble := false, false
	for index := 0; index < len(query); index++ {
		ch := query[index]
		switch ch {
		case '\'':
			if !inDouble {
				if inSingle && index+1 < len(query) && query[index+1] == '\'' {
					out.WriteByte(ch)
					out.WriteByte(query[index+1])
					index++
					continue
				}
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '?':
			if !inSingle && !inDouble {
				fmt.Fprintf(&out, "$%d", parameter)
				parameter++
				continue
			}
		}
		out.WriteByte(ch)
	}
	return out.String()
}
