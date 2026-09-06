package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func TestRecollectUsageFillsClientWithoutChangingAmountsOrKnownClient(t *testing.T) {
	st, err := OpenLegacySQLite(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	testUsageClientRepair(t, st)
}

func TestPostgresUsageClientRepair(t *testing.T) {
	testUsageClientRepair(t, openPostgresIntegrationStore(t))
}

func testUsageClientRepair(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	for i, originalRuntime := range []string{"", "claude-unknown", "claudecode"} {
		original := core.UsageRecord{Source: "claude", SessionID: "session", Timestamp: time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC), RuntimeID: originalRuntime, InputTokens: 5, CostUSD: 1}
		if err := st.UpsertUsage(ctx, []core.UsageRecord{original}); err != nil {
			t.Fatal(err)
		}
		replayed := original
		replayed.RuntimeID, replayed.InputTokens, replayed.CostUSD = "claude-desktop", 999, 999
		if err := st.UpsertUsage(ctx, []core.UsageRecord{replayed, replayed}); err != nil {
			t.Fatal(err)
		}
	}
	recs, err := st.QueryUsage(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("replay added usage: %+v", recs)
	}
	for _, rec := range recs {
		want := "claude-desktop"
		if rec.Timestamp.Second() == 2 {
			want = "claudecode"
		}
		if rec.RuntimeID != want || rec.InputTokens != 5 || rec.CostUSD != 1 {
			t.Fatalf("replay changed ledger: %+v", rec)
		}
	}
	legacy := core.UsageRecord{Source: "codex", SessionID: "old", Timestamp: time.Now().UTC(), InputTokens: 7}
	if err := st.UpsertUsage(ctx, []core.UsageRecord{legacy}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := st.BackfillUsageRuntimes(ctx, "codex", "", map[string]string{"old": "codex-app"}); err != nil {
			t.Fatal(err)
		}
	}
	recs, err = st.QueryUsage(ctx, legacy.Timestamp)
	if err != nil || len(recs) != 1 || recs[0].RuntimeID != "codex-app" || recs[0].InputTokens != 7 {
		t.Fatalf("historical repair = %+v, err=%v", recs, err)
	}
}
