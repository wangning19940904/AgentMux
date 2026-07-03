package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentnexus/agentnexus/core"
)

func TestProviderCRUDAndActive(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	p := &core.Provider{ID: "p1", Name: "P1", BaseURL: "http://x", Tools: []string{"claudecode"},
		CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := st.UpsertProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetProvider(ctx, "p1")
	if err != nil || got == nil || got.Name != "P1" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if len(got.Tools) != 1 || got.Tools[0] != "claudecode" {
		t.Fatalf("tools = %v", got.Tools)
	}
	if err := st.SetActiveProvider(ctx, "claudecode", "p1"); err != nil {
		t.Fatal(err)
	}
	id, ok, err := st.ActiveProviderID(ctx, "claudecode")
	if err != nil || !ok || id != "p1" {
		t.Fatalf("active = %q,%v,%v", id, ok, err)
	}
}

func TestUsageUpsertDedupAndQuery(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rec := core.UsageRecord{Source: "claude", SessionID: "s", Timestamp: ts, InputTokens: 5}
	// Insert twice; primary key should dedup.
	if err := st.UpsertUsage(ctx, []core.UsageRecord{rec, rec}); err != nil {
		t.Fatal(err)
	}
	got, err := st.QueryUsage(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("rows = %d, want 1 (dedup)", len(got))
	}
	// since filter excludes earlier rows.
	future, _ := st.QueryUsage(ctx, ts.Add(time.Hour))
	if len(future) != 0 {
		t.Fatalf("since filter rows = %d, want 0", len(future))
	}
}
