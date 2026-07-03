package parser

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"time"

	"github.com/agentnexus/agentnexus/core"
	_ "modernc.org/sqlite"
)

// cursorCollector reads Cursor's local SQLite store read-only. Cursor stores
// chat/usage data in state databases; schemas vary across versions, so we
// query defensively and skip gracefully when tables/columns are absent.
type cursorCollector struct {
	root string
}

func (c *cursorCollector) Source() string { return "cursor" }

func (c *cursorCollector) candidates() []string {
	if c.root != "" {
		return []string{filepath.Join(c.root, "state.vscdb")}
	}
	home, _ := os.UserHomeDir()
	return []string{
		filepath.Join(home, "Library", "Application Support", "Cursor", "User", "globalStorage", "state.vscdb"),
		filepath.Join(home, ".config", "Cursor", "User", "globalStorage", "state.vscdb"),
	}
}

func (c *cursorCollector) Collect(ctx context.Context, since time.Time) ([]core.UsageRecord, error) {
	var out []core.UsageRecord
	for _, path := range c.candidates() {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		recs := c.query(ctx, path, since)
		out = append(out, recs...)
	}
	return out, nil
}

// query opens the db read-only and extracts any token-usage rows it can find.
// Because Cursor's schema is undocumented and version-dependent, this is a
// best-effort read that returns nothing rather than failing on schema drift.
func (c *cursorCollector) query(ctx context.Context, path string, since time.Time) []core.UsageRecord {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&immutable=1")
	if err != nil {
		return nil
	}
	defer db.Close()

	// Probe for a usage-like table. Cursor versions differ; we look for a
	// key/value ItemTable that may hold aggregated usage JSON.
	rows, err := db.QueryContext(ctx,
		`SELECT key, value FROM ItemTable WHERE key LIKE '%usage%' OR key LIKE '%token%'`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []core.UsageRecord
	for rows.Next() {
		var key string
		var val []byte
		if err := rows.Scan(&key, &val); err != nil {
			continue
		}
		if rec, ok := parseCursorValue(key, val); ok {
			if sinceOK(rec.Timestamp, since) {
				out = append(out, rec)
			}
		}
	}
	return out
}

// parseCursorValue attempts to extract a usage record from a key/value blob.
// Returns ok=false when the blob is not recognizable usage data.
func parseCursorValue(key string, val []byte) (core.UsageRecord, bool) {
	// Cursor does not expose a stable per-call token schema in the local db
	// for all versions; we conservatively return false here so we never emit
	// misleading numbers. The hook is left in place for version-specific
	// extraction to be added without touching the collector contract.
	_ = key
	_ = val
	return core.UsageRecord{}, false
}
