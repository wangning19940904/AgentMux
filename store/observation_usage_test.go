package store

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentnexus/agentnexus/core"
)

func TestQueryObservationUsageSelectsHighestPrioritySourceWithoutDroppingAttempts(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	base := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	record := func(traceID, spanID, source, runtime, request string, attempt int, tokens int64) {
		t.Helper()
		if err := st.RecordObservation(ctx, core.ObservationEnvelope{
			EventID: "event-" + spanID, Time: base.Add(time.Duration(tokens) * time.Second),
			TraceID: traceID, SpanID: spanID, Kind: "model.request", Lifecycle: core.ObservationLifecycleEnd,
			RuntimeID: runtime, SessionID: "session", Source: source, Quality: core.ObservationQualityComplete,
			Status: core.ObservationStatusOK, Model: &core.ObservationModel{Resolved: "model", RequestID: request, Attempt: attempt},
			Usage: &core.ObservationUsage{InputTokens: tokens, TotalTokens: tokens, CostUSD: float64(tokens) / 1000},
		}); err != nil {
			t.Fatal(err)
		}
	}
	// The same logical request is visible internally, through Proxy and from a
	// transcript trace reconstructed with a different trace ID. Only the
	// authoritative internal record should count.
	record("trace-shared", "span-internal", "agentnexus.internal", "codex", "req-shared", 1, 100)
	record("trace-shared", "span-proxy", "agentnexus.proxy", "codex", "req-shared", 1, 100)
	record("trace-backfill", "span-transcript", "transcript", "codex", "req-shared", 1, 100)
	// A second real request in the same turn must not be dropped merely because
	// its source rank matches the first request.
	record("trace-shared", "span-internal-second", "agentnexus.internal", "codex", "req-second", 1, 7)
	// A proxy-only trace with two failover attempts keeps both attempts.
	record("trace-retry", "span-attempt-1", "agentnexus.proxy", "claudecode", "req-retry", 1, 20)
	record("trace-retry", "span-attempt-2", "agentnexus.proxy", "claudecode", "req-retry", 2, 30)

	records, err := st.QueryObservationUsage(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 4 {
		t.Fatalf("records = %+v", records)
	}
	var total int64
	seen := map[string]bool{}
	for _, item := range records {
		total += item.InputTokens
		seen[item.RequestID] = true
	}
	if total != 157 || !seen["req-shared:attempt:1"] || !seen["req-second:attempt:1"] || !seen["req-retry:attempt:1"] || !seen["req-retry:attempt:2"] {
		t.Fatalf("materialized usage = %+v", records)
	}
}

func TestObservationTraceSummaryDeduplicatesSourcesPerRequest(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "trace-summary.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	base := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	record := func(eventID, spanID, source, requestID string, input int64) {
		t.Helper()
		if err := st.RecordObservation(ctx, core.ObservationEnvelope{
			EventID: eventID, Time: base, TraceID: "trace-summary", SpanID: spanID,
			Kind: "model.request", Lifecycle: core.ObservationLifecycleEnd,
			RuntimeID: "codex", Source: source, Quality: core.ObservationQualityComplete,
			Status: core.ObservationStatusOK,
			Model:  &core.ObservationModel{Resolved: "model", RequestID: requestID, Attempt: 1},
			Usage:  &core.ObservationUsage{InputTokens: input, TotalTokens: input, CostUSD: float64(input) / 1000},
		}); err != nil {
			t.Fatal(err)
		}
	}
	record("internal-1", "internal-span-1", "agentnexus.internal", "request-1", 100)
	record("otel-1", "otel-span-1", "otel.codex", "request-1", 100)
	record("proxy-1", "proxy-span-1", "agentnexus.proxy", "request-1", 100)
	record("internal-2", "internal-span-2", "agentnexus.internal", "request-2", 7)

	trace, err := st.GetObservationTrace(ctx, "trace-summary")
	if err != nil {
		t.Fatal(err)
	}
	if trace == nil || trace.Usage.InputTokens != 107 || trace.Usage.TotalTokens != 107 || math.Abs(trace.Usage.CostUSD-0.107) > 1e-9 {
		t.Fatalf("trace summary = %+v", trace)
	}
}

func TestDailyUsageSurvivesDetailedTraceRetention(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "daily.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	created := time.Date(2025, 12, 1, 12, 0, 0, 0, time.UTC)
	if err := st.RecordObservation(ctx, core.ObservationEnvelope{
		EventID: "daily-event", TraceID: "daily-trace", SpanID: "daily-span", Time: created,
		Kind: "model.request", Lifecycle: core.ObservationLifecycleEnd, Status: core.ObservationStatusOK,
		AgentID: "agent", RuntimeID: "claude", Source: "agentnexus.internal",
		Model: &core.ObservationModel{Resolved: "claude-model", RequestID: "daily-request", Attempt: 1},
		Usage: &core.ObservationUsage{InputTokens: 90, OutputTokens: 10, TotalTokens: 100, CostUSD: 0.05},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.MaterializeObservationDailyUsage(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CleanupObservationRetention(ctx, created.Add(181*24*time.Hour), 180*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if trace, err := st.GetObservationTrace(ctx, "daily-trace"); err != nil || trace != nil {
		t.Fatalf("expired trace = %+v, %v", trace, err)
	}
	records, err := st.QueryObservationDailyUsage(ctx, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].InputTokens != 90 || records[0].Requests != 1 || records[0].CostUSD != 0.05 {
		t.Fatalf("daily records = %+v", records)
	}
}
