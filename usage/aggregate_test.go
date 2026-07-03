package usage

import (
	"testing"
	"time"

	"github.com/agentnexus/agentnexus/core"
)

func TestAggregateDailyAndModel(t *testing.T) {
	day1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	recs := []core.UsageRecord{
		{Source: "claude", Model: "opus", Timestamp: day1, InputTokens: 100, OutputTokens: 50, CostUSD: 1.0},
		{Source: "claude", Model: "opus", Timestamp: day1, InputTokens: 200, OutputTokens: 10, CostUSD: 2.0},
		{Source: "codex", Model: "gpt-5", Timestamp: day2, InputTokens: 10, OutputTokens: 5, CostUSD: 0.5},
	}
	r := Aggregate("daily", recs)

	if r.Totals.Records != 3 {
		t.Fatalf("records = %d, want 3", r.Totals.Records)
	}
	if r.Totals.CostUSD != 3.5 {
		t.Fatalf("cost = %v, want 3.5", r.Totals.CostUSD)
	}
	if len(r.Buckets) != 2 {
		t.Fatalf("buckets = %d, want 2", len(r.Buckets))
	}
	if r.Buckets[0].Key != "2026-01-01" || r.Buckets[0].Totals.Records != 2 {
		t.Fatalf("bucket[0] = %+v", r.Buckets[0])
	}
	// By model sorted by cost desc: opus (3.0) before gpt-5 (0.5).
	if len(r.ByModel) != 2 || r.ByModel[0].Model != "opus" {
		t.Fatalf("by_model = %+v", r.ByModel)
	}
}

func TestAggregateSessionAndBlocks(t *testing.T) {
	ts := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	recs := []core.UsageRecord{
		{Source: "claude", SessionID: "s1", Model: "opus", Timestamp: ts, InputTokens: 1},
		{Source: "claude", SessionID: "s2", Model: "opus", Timestamp: ts, InputTokens: 1},
	}
	if r := Aggregate("session", recs); len(r.Buckets) != 2 {
		t.Fatalf("session buckets = %d, want 2", len(r.Buckets))
	}
	if r := Aggregate("blocks", recs); len(r.Buckets) != 1 {
		t.Fatalf("blocks buckets = %d, want 1", len(r.Buckets))
	}
}

func TestParseSince(t *testing.T) {
	if _, err := ParseSince("7d"); err != nil {
		t.Fatalf("7d: %v", err)
	}
	if _, err := ParseSince("2w"); err != nil {
		t.Fatalf("2w: %v", err)
	}
	if _, err := ParseSince("2026-01-01"); err != nil {
		t.Fatalf("date: %v", err)
	}
	if _, err := ParseSince("garbage"); err == nil {
		t.Fatalf("expected error for garbage")
	}
}
