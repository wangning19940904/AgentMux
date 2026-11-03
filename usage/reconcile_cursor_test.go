package usage

import (
	"context"
	"math"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

func cursorReconciliationFixture() (core.UsageRecord, core.UsageRecord) {
	dashboard := core.UsageRecord{
		Source: "cursor", RuntimeID: "cursor", SessionID: "native-session", RequestID: "cursor-synthetic",
		Model: "claude-opus-5-5-medium", Timestamp: time.Date(2026, 10, 14, 2, 50, 26, 727000000, time.UTC),
		InputTokens: 4, OutputTokens: 18, CacheReadTokens: 9328, CacheWriteTokens: 20802,
		Provenance: cursorProvenanceDashboard, TokenQuality: core.UsageTokenQualityExact,
		CostKind: core.UsageCostKindRecorded, CostUSD: 0.10625160217285157,
	}
	hook := dashboard
	hook.RequestID, hook.Provenance, hook.Model = "generation-id", cursorProvenanceHook, "claude-opus-5-5"
	hook.Timestamp = dashboard.Timestamp.Add(3516 * time.Millisecond)
	hook.InputTokens, hook.CostKind, hook.CostUSD = 30134, core.UsageCostKindCalculated, 0.2267716
	return dashboard, hook
}

func TestReconcileCursorUsagePreservesRecordedDashboardTokensAndCost(t *testing.T) {
	dashboard, hook := cursorReconciliationFixture()
	for _, input := range []int64{dashboard.InputTokens, hook.InputTokens} {
		hook.InputTokens = input
		records := []core.UsageRecord{dashboard, hook}
		before := append([]core.UsageRecord(nil), records...)
		if got := reconcileCursorUsage(records); !reflect.DeepEqual(got, []core.UsageRecord{dashboard}) {
			t.Fatalf("reconciled records = %+v", got)
		}
		if !reflect.DeepEqual(before, records) {
			t.Fatal("reconciliation changed ledger records")
		}
	}
}

func TestReconcileCursorUsagePreservesUncorroboratedHooks(t *testing.T) {
	for name, mutate := range map[string]func(*core.UsageRecord){
		"host":          func(r *core.UsageRecord) { r.Host = "remote" },
		"session":       func(r *core.UsageRecord) { r.SessionID = "other" },
		"source":        func(r *core.UsageRecord) { r.Source = "claude" },
		"model":         func(r *core.UsageRecord) { r.Model = "claude-opus-5-6" },
		"input":         func(r *core.UsageRecord) { r.InputTokens++ },
		"output":        func(r *core.UsageRecord) { r.OutputTokens++ },
		"cache read":    func(r *core.UsageRecord) { r.CacheReadTokens++ },
		"cache write":   func(r *core.UsageRecord) { r.CacheWriteTokens++ },
		"time":          func(r *core.UsageRecord) { r.Timestamp = r.Timestamp.Add(time.Minute) },
		"unknown usage": func(r *core.UsageRecord) { r.TokenQuality = core.UsageTokenQualityUnknown },
	} {
		t.Run(name, func(t *testing.T) {
			dashboard, hook := cursorReconciliationFixture()
			mutate(&hook)
			records := []core.UsageRecord{dashboard, hook}
			if got := reconcileCursorUsage(records); !reflect.DeepEqual(got, records) {
				t.Fatalf("uncorroborated hook dropped: %+v", got)
			}
		})
	}
	dashboard, hook := cursorReconciliationFixture()
	if got := reconcileCursorUsage([]core.UsageRecord{hook}); !reflect.DeepEqual(got, []core.UsageRecord{hook}) {
		t.Fatalf("unmatched input convention must remain unchanged: %+v", got)
	}
	second := hook
	second.Timestamp = second.Timestamp.Add(time.Second)
	second.RequestID = "second-generation"
	if got := reconcileCursorUsage([]core.UsageRecord{dashboard, hook, second}); !reflect.DeepEqual(got, []core.UsageRecord{dashboard, second}) {
		t.Fatalf("dashboard must replace at most one hook: %+v", got)
	}
	if cursorUsageModel("grok-4.7-high") == cursorUsageModel("grok-4.7") {
		t.Fatal("unknown model variants must not be collapsed")
	}
}

func TestReportReconcilesCursorWithRecordedDashboardAmount(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	eng := NewEngine(&config.Config{Usage: config.UsageConfig{CacheDir: t.TempDir(), Offline: true}}, st, nil)
	dashboard, hook := cursorReconciliationFixture()
	if err := st.UpsertUsage(ctx, []core.UsageRecord{dashboard, hook}); err != nil {
		t.Fatal(err)
	}
	report, err := eng.Report(ctx, "daily", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Totals.InputTokens != 4 || report.Totals.OutputTokens != 18 ||
		report.Totals.CacheReadTokens != 9328 || report.Totals.CacheWriteTokens != 20802 ||
		report.Totals.Records != 1 || math.Abs(report.Totals.CostUSD-dashboard.CostUSD) > 1e-12 {
		t.Fatalf("dashboard amount was not retained: %+v", report.Totals)
	}
	stored, err := st.QueryUsage(ctx, time.Time{})
	if err != nil || len(stored) != 2 {
		t.Fatalf("stored ledger changed: records=%+v err=%v", stored, err)
	}
}
