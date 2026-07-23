package usage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentnexus/agentnexus/config"
	"github.com/agentnexus/agentnexus/core"
	"github.com/agentnexus/agentnexus/store"
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

func TestMergeUsageRecordsDeduplicatesObservedRequestsAndKeepsLegacyOnlySources(t *testing.T) {
	day := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	observed := []core.UsageRecord{
		{Source: "codex", RequestID: "request-1", Model: "gpt-5", Timestamp: day, InputTokens: 80},
	}
	legacy := []core.UsageRecord{
		{Source: "codex-cli", RequestID: "request-1", Model: "gpt-5", Timestamp: day.Add(time.Second), InputTokens: 80},
		{Source: "codex", RequestID: "request-2", Model: "gpt-5", Timestamp: day.Add(2 * time.Second), InputTokens: 20},
		{Source: "cursor", SessionID: "cursor-session", Model: "cursor-model", Timestamp: day, InputTokens: 30},
	}
	merged := mergeUsageRecords(observed, legacy)
	if len(merged) != 3 {
		t.Fatalf("merged = %+v", merged)
	}
	var total int64
	for _, record := range merged {
		total += record.InputTokens
	}
	if total != 130 {
		t.Fatalf("total = %d, records=%+v", total, merged)
	}
}

func TestMergeUsageRecordsDailyAggregateCoversLegacyRowsOnlyForSameSourceModelDay(t *testing.T) {
	day := time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)
	observed := []core.UsageRecord{
		{Source: "claude", Model: "sonnet", Timestamp: day, InputTokens: 100, Requests: 2},
	}
	legacy := []core.UsageRecord{
		{Source: "claude", Model: "sonnet", Timestamp: day.Add(time.Hour), InputTokens: 40},
		{Source: "claude", Model: "opus", Timestamp: day.Add(time.Hour), InputTokens: 10},
		{Source: "gemini", Model: "sonnet", Timestamp: day.Add(time.Hour), InputTokens: 5},
	}
	merged := mergeUsageRecords(observed, legacy)
	if len(merged) != 3 {
		t.Fatalf("merged = %+v", merged)
	}
}
