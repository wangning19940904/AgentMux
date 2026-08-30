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

func TestDashboardUsageUpgradesEstimateAndPreservesRecordedCost(t *testing.T) {
	st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "agentmux.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := &config.Config{Usage: config.UsageConfig{Offline: true}}
	engine := NewEngine(cfg, st, nil)
	ts := time.Now().UTC().Truncate(time.Millisecond)
	estimate := core.UsageRecord{
		Source: "cursor", RuntimeID: "cursor", SessionID: "session", RequestID: "request", Model: "cursor", Timestamp: ts,
		InputTokens: 100, OutputTokens: 20, Provenance: cursorProvenanceLocalEstimated,
		ProvenanceRank: cursorRankLocalEstimated, TokenQuality: core.UsageTokenQualityEstimated, CostKind: core.UsageCostKindCalculated,
	}
	if err := engine.recordBatch(context.Background(), []core.UsageRecord{estimate}); err != nil {
		t.Fatal(err)
	}
	exact := estimate
	exact.Timestamp = ts.Add(time.Second)
	exact.InputTokens = 80
	exact.OutputTokens = 10
	exact.Provenance = cursorProvenanceDashboard
	exact.ProvenanceRank = cursorRankDashboard
	exact.TokenQuality = core.UsageTokenQualityExact
	exact.CostKind = core.UsageCostKindRecorded
	exact.CostUSD = 1.23
	if err := engine.recordBatch(context.Background(), []core.UsageRecord{exact}); err != nil {
		t.Fatal(err)
	}
	records, err := st.QueryUsage(context.Background(), time.Time{})
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%+v err=%v", records, err)
	}
	if records[0].Provenance != cursorProvenanceDashboard || records[0].TokenQuality != core.UsageTokenQualityExact {
		t.Fatalf("record = %+v", records[0])
	}
	report, err := engine.Report(context.Background(), "daily", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(report.Totals.CostUSD-1.23) > 1e-9 || report.Totals.Records != 1 || report.Totals.EstimatedTokens != 0 {
		t.Fatalf("totals = %+v", report.Totals)
	}
}
