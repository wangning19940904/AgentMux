package usage

import (
	"testing"
	"time"

	"github.com/agentnexus/agentnexus/core"
)

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
