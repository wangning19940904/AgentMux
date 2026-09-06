package usage

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func TestAggregateDailyAndModel(t *testing.T) {
	day1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	recs := []core.UsageRecord{
		{Source: "claude", SessionID: "s1", Model: "opus", Timestamp: day1, InputTokens: 100, OutputTokens: 50, CostUSD: 1.0},
		{Source: "claude", SessionID: "s1", Model: "opus", Timestamp: day1, InputTokens: 200, OutputTokens: 10, CostUSD: 2.0},
		{Source: "codex", SessionID: "s2", Model: "gpt-5", Timestamp: day2, InputTokens: 10, OutputTokens: 5, CostUSD: 0.5},
	}
	r := Aggregate("daily", recs)

	if r.Totals.Records != 3 {
		t.Fatalf("records = %d, want 3", r.Totals.Records)
	}
	if r.Totals.Sessions != 2 {
		t.Fatalf("sessions = %d, want 2", r.Totals.Sessions)
	}
	if r.Totals.CostUSD != 3.5 {
		t.Fatalf("cost = %v, want 3.5", r.Totals.CostUSD)
	}
	if len(r.Buckets) != 2 {
		t.Fatalf("buckets = %d, want 2", len(r.Buckets))
	}
	if len(r.Buckets[0].ByRuntime) != 1 || r.Buckets[0].ByRuntime[0].Runtime != "claude-unknown" || r.Buckets[0].ByRuntime[0].Tokens != 360 {
		t.Fatalf("bucket runtime breakdown = %+v", r.Buckets[0].ByRuntime)
	}
	if r.Buckets[0].Key != "2026-01-01" || r.Buckets[0].Totals.Records != 2 || r.Buckets[0].Totals.Sessions != 1 {
		t.Fatalf("bucket[0] = %+v", r.Buckets[0])
	}
	// By model sorted by cost desc: opus (3.0) before gpt-5 (0.5).
	if len(r.ByModel) != 2 || r.ByModel[0].Model != "opus" {
		t.Fatalf("by_model = %+v", r.ByModel)
	}
}

func TestAggregateHourlyBuckets(t *testing.T) {
	start := time.Date(2026, 1, 1, 10, 15, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	recs := []core.UsageRecord{
		{Source: "codex", RuntimeID: "codex", SessionID: "s1", Timestamp: start, InputTokens: 10},
		{Source: "claude", RuntimeID: "claudecode", SessionID: "s2", Timestamp: start.Add(50 * time.Minute), InputTokens: 20},
	}
	report := Aggregate("hourly", recs)
	if len(report.Buckets) != 2 || report.Buckets[0].Key != "2026-01-01 10:00" || report.Buckets[1].Key != "2026-01-01 11:00" {
		t.Fatalf("hourly buckets = %+v", report.Buckets)
	}
	if report.Buckets[0].ByRuntime[0].Runtime != "codex" || report.Buckets[1].ByRuntime[0].Runtime != "claudecode" {
		t.Fatalf("hourly runtime series = %+v", report.Buckets)
	}
}

func TestAggregateSessionsAreDistinctBySourceAndHost(t *testing.T) {
	ts := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	recs := []core.UsageRecord{
		{Source: "codex", SessionID: "shared", Timestamp: ts, InputTokens: 1},
		{Source: "codex", SessionID: "shared", Timestamp: ts.Add(time.Minute), InputTokens: 1},
		{Source: "claude", SessionID: "shared", Timestamp: ts, InputTokens: 1},
		{Source: "codex", Host: "remote", SessionID: "shared", Timestamp: ts, InputTokens: 1},
		{Source: "codex", SessionID: "", Timestamp: ts, InputTokens: 1},
	}
	r := Aggregate("daily", recs)
	if r.Totals.Records != 5 || r.Totals.Sessions != 3 {
		t.Fatalf("totals = %+v, want 5 requests and 3 sessions", r.Totals)
	}
}

func TestAggregateSeparatesEstimatedCoverage(t *testing.T) {
	ts := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	report := Aggregate("daily", []core.UsageRecord{
		{Source: "cursor", SessionID: "one", Model: "cursor", Timestamp: ts, InputTokens: 100, TokenQuality: core.UsageTokenQualityExact},
		{Source: "cursor", SessionID: "two", Model: "cursor", Timestamp: ts.Add(time.Second), InputTokens: 40, OutputTokens: 10, TokenQuality: core.UsageTokenQualityEstimated},
	})
	if report.Totals.EstimatedTokens != 50 || report.Totals.EstimatedRecords != 1 {
		t.Fatalf("totals = %+v", report.Totals)
	}
	if len(report.ByRuntime) != 1 || report.ByRuntime[0].EstimatedTokens != 50 {
		t.Fatalf("by runtime = %+v", report.ByRuntime)
	}
}

func TestAggregateEmptySlicesEncodeAsArrays(t *testing.T) {
	r := Aggregate("daily", nil)
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	for _, want := range []string{`"buckets":[]`, `"by_model":[]`, `"by_source":[]`} {
		if !strings.Contains(got, want) {
			t.Fatalf("json = %s, want %s", got, want)
		}
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
