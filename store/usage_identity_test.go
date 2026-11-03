package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func TestSQLiteUsageIdentityMigrationPreservesLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE usage_records (
 source TEXT,session_id TEXT,project TEXT,model TEXT,timestamp TEXT,
 input_tokens INTEGER,output_tokens INTEGER,cache_read_tokens INTEGER,cache_write_tokens INTEGER,
 tool TEXT,cost_usd REAL,host TEXT,PRIMARY KEY(source,session_id,timestamp,host));
 INSERT INTO usage_records VALUES ('codex','legacy','project','model','2026-10-14T00:00:00Z',10,2,30,0,'',1.5,'');`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	st, err := OpenLegacySQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	before, err := st.QueryUsage(context.Background(), time.Time{})
	if err != nil || len(before) != 1 || before[0].InputTokens != 10 || before[0].CostUSD != 1.5 {
		t.Fatalf("migration lost data: %+v %v", before, err)
	}
	st.Close()
	st, err = OpenLegacySQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	after, err := st.QueryUsage(context.Background(), time.Time{})
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("reopen changed data: %+v %v", after, err)
	}
	testUsageRequestIdentity(t, st)
}

func TestPostgresUsageRequestIdentity(t *testing.T) {
	testUsageRequestIdentity(t, openPostgresIntegrationStore(t))
}

func testUsageRequestIdentity(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	ts := time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC)
	legacy := core.UsageRecord{Source: "codex", SessionID: "same", Timestamp: ts, InputTokens: 10}
	a, b := legacy, legacy
	a.RequestID, b.RequestID = "first", "second"
	a.InputTokens, b.InputTokens = 20, 30
	for i := 0; i < 2; i++ {
		if err := st.UpsertUsage(ctx, []core.UsageRecord{legacy, a, b}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := st.QueryUsage(ctx, ts)
	if err != nil || len(rows) != 3 {
		t.Fatalf("same-time requests lost or duplicated: %+v %v", rows, err)
	}
	var sum int64
	for _, row := range rows {
		sum += row.InputTokens
	}
	if sum != 60 {
		t.Fatalf("tokens=%d", sum)
	}
}
