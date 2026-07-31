package usage

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

func TestInitialCollectSinceResumesFromCheckpoint(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.db"))
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
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.db"))
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
