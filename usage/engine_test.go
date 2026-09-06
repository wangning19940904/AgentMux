package usage

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

func TestCollectBackfillsHistoricalClientsWithoutReplayingUsage(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for path, content := range map[string]string{
		".codex/archived_sessions/rollout-app.jsonl": `{"type":"session_meta","payload":{"id":"app","originator":"Codex Desktop","source":"exec"}}`,
		".codex/sessions/rollout-cli.jsonl":          `{"type":"session_meta","payload":{"id":"cli","originator":"codex-tui","source":"cli"}}`,
		".claude/projects/p/claude.jsonl":            `{"sessionId":"claude-app","entrypoint":"claude-desktop"}`,
	} {
		path = filepath.Join(home, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	eng := NewEngine(&config.Config{Usage: config.UsageConfig{Sources: []string{"claude", "codex"}, CacheDir: t.TempDir(), Offline: true}}, st, nil)
	records := []core.UsageRecord{
		{Source: "codex", SessionID: "app", Timestamp: old, InputTokens: 100, CostUSD: 1},
		{Source: "codex", SessionID: "cli", Timestamp: old, InputTokens: 200, CostUSD: 2},
		{Source: "claude", SessionID: "claude-app", Timestamp: old, InputTokens: 300, CostUSD: 3},
		{Source: "codex", SessionID: "app", Host: "remote", Timestamp: old, InputTokens: 400, CostUSD: 4},
		{Source: "codex", SessionID: "missing-file", Timestamp: old, InputTokens: 500, CostUSD: 5},
		{Source: "codex", SessionID: "app", RuntimeID: "codex", RequestID: "live", Timestamp: old.Add(time.Second), InputTokens: 600, CostUSD: 6},
	}
	if err := st.UpsertUsage(ctx, records); err != nil {
		t.Fatal(err)
	}
	before, err := eng.Report(ctx, "daily", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	// A current checkpoint excludes every fixture from normal collection.
	// Historical repair must still read the archived headers and old metadata.
	for i := 0; i < 2; i++ {
		if err := eng.Collect(ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	after, err := eng.Report(ctx, "daily", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Totals, after.Totals) || !reflect.DeepEqual(before.BySource, after.BySource) {
		t.Fatalf("client backfill changed amounts: before=%+v after=%+v", before, after)
	}
	got, err := st.QueryUsage(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"app": "codex-app", "cli": "codex", "claude-app": "claude-desktop", "missing-file": ""}
	for _, rec := range got {
		expected := want[rec.SessionID]
		if rec.Host == "remote" {
			expected = ""
		}
		if rec.RequestID == "live" {
			expected = "codex"
		}
		if rec.RuntimeID != expected {
			t.Fatalf("client metadata = %+v, want %q", rec, expected)
		}
	}
	breakdown := map[string]int64{}
	for _, row := range after.ByRuntime {
		breakdown[row.Runtime] = row.Tokens
	}
	if !reflect.DeepEqual(breakdown, map[string]int64{"codex-app": 100, "codex": 800, "claude-desktop": 300, "codex-unknown": 900}) {
		t.Fatalf("report client breakdown = %v", breakdown)
	}
	// The identical remote session ID receives only remote metadata.
	if err := eng.backfillUsageRuntimes(ctx, "codex", filepath.Join(home, ".codex"), "remote"); err != nil {
		t.Fatal(err)
	}
	got, err = st.QueryUsage(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range got {
		if rec.Host == "remote" && rec.RuntimeID != "codex-app" {
			t.Fatalf("remote backfill = %+v", rec)
		}
	}
}

func TestInitialCollectSinceResumesFromCheckpoint(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	eng := NewEngine(&config.Config{}, st, nil)
	backfill := 180 * 24 * time.Hour

	// No checkpoint: fall back to the full backfill window.
	full := eng.initialCollectSince(ctx, backfill)
	if delta := time.Since(full) - backfill; delta < -time.Minute || delta > time.Minute {
		t.Fatalf("without checkpoint, since should be now-backfill, got %v", full)
	}

	// Recent checkpoint: resume from checkpoint minus the one-hour overlap.
	checkpoint := time.Now().UTC().Add(-24 * time.Hour)
	if err := st.SetSetting(ctx, usageCollectCheckpointKey, checkpoint.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	resume := eng.initialCollectSince(ctx, backfill)
	if resume.Before(checkpoint.Add(-time.Hour-time.Minute)) || resume.After(checkpoint.Add(-time.Hour+time.Minute)) {
		t.Fatalf("recent checkpoint should resume near checkpoint-1h, got %v", resume)
	}

	// Stale checkpoint older than the window must not widen it past backfill.
	stale := time.Now().UTC().Add(-2 * backfill)
	if err := st.SetSetting(ctx, usageCollectCheckpointKey, stale.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	clamped := eng.initialCollectSince(ctx, backfill)
	if delta := time.Since(clamped) - backfill; delta < -time.Minute || delta > time.Minute {
		t.Fatalf("stale checkpoint must clamp to now-backfill, got %v", clamped)
	}
}

func TestReportRangeUsesCanonicalRowsFiltersDatesAndReprices(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := &config.Config{Usage: config.UsageConfig{CacheDir: t.TempDir(), Offline: true}}
	eng := NewEngine(cfg, st, nil)
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	records := []core.UsageRecord{
		{Source: "codex", SessionID: "s1", Model: "gpt-5", Timestamp: base, InputTokens: 100, CostUSD: 999},
		{Source: "codex", SessionID: "s2", Model: "gpt-5", Timestamp: base.Add(24 * time.Hour), InputTokens: 200, OutputTokens: 10, CostUSD: 999},
		{Source: "codex", SessionID: "s3", Model: "gpt-5", Timestamp: base.Add(48 * time.Hour), InputTokens: 300, CostUSD: 999},
	}
	if err := st.UpsertUsage(ctx, records); err != nil {
		t.Fatal(err)
	}

	report, err := eng.ReportRange(ctx, "daily", base.Add(24*time.Hour), base.Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if report.Totals.Records != 1 || report.Totals.InputTokens != 200 || report.Totals.OutputTokens != 10 {
		t.Fatalf("totals = %+v", report.Totals)
	}
	// gpt-5 fallback: $1.25/M input + $10/M output. The stored $999
	// sentinel must not leak into the calibrated report.
	wantCost := float64(200)*1.25/1e6 + float64(10)*10/1e6
	if math.Abs(report.Totals.CostUSD-wantCost) > 1e-12 {
		t.Fatalf("cost = %.12f, want %.12f", report.Totals.CostUSD, wantCost)
	}
}

func TestReportRangeInLocationBucketsByRequestedTimezone(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	eng := NewEngine(&config.Config{Usage: config.UsageConfig{CacheDir: t.TempDir(), Offline: true}}, st, nil)
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	record := core.UsageRecord{
		Source: "codex", SessionID: "timezone", Model: "gpt-5",
		Timestamp: time.Date(2026, 8, 29, 16, 30, 0, 0, time.UTC), InputTokens: 10,
	}
	if err := st.UpsertUsage(ctx, []core.UsageRecord{record}); err != nil {
		t.Fatal(err)
	}
	since := time.Date(2026, 8, 30, 0, 0, 0, 0, location)
	until := since.AddDate(0, 0, 1)
	report, err := eng.ReportRangeInLocation(ctx, "daily", since, until, location)
	if err != nil {
		t.Fatal(err)
	}
	if report.Timezone != "Asia/Shanghai" || len(report.Buckets) != 1 || report.Buckets[0].Key != "2026-08-30" {
		t.Fatalf("report = %+v", report)
	}
}
