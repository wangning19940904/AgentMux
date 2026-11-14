package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func TestImportLegacyObservationsIsIdempotentAndCorrelates(t *testing.T) {
	st, err := OpenLegacySQLite(filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	started := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	ended := started.Add(1250 * time.Millisecond)

	agent := &core.AgentInstance{
		ID: "agent-legacy", Name: "Legacy Agent", RuntimeID: "codex", WorkDir: "/tmp/legacy-project",
		Enabled: true, CreatedAt: started, UpdatedAt: started,
	}
	if err := st.UpsertAgentInstance(ctx, agent); err != nil {
		t.Fatal(err)
	}
	conversation, _, err := st.GetOrCreateConversation(ctx, core.Conversation{
		ID: "conv-legacy", Scope: "project:legacy", ChatID: "chat-1", AgentID: agent.ID,
		WorkDir: agent.WorkDir, NativeSessionID: "session-legacy",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertUsage(ctx, []core.UsageRecord{{
		Source: "codex", SessionID: "session-legacy", RequestID: "usage-request", Project: agent.WorkDir,
		Model: "gpt-5", Timestamp: started, InputTokens: 120, OutputTokens: 30,
		CacheReadTokens: 20, CacheWriteTokens: 10, CostUSD: 0.012,
	}}); err != nil {
		t.Fatal(err)
	}
	first, err := st.ImportLegacyObservations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first != (LegacyObservationImportResult{UsageScanned: 1, UsageImported: 1}) {
		t.Fatalf("first import = %+v", first)
	}

	usageTraces, err := st.ListObservationTraces(ctx, ObservationTraceFilter{Source: "legacy_usage", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(usageTraces) != 1 {
		t.Fatalf("usage traces = %+v", usageTraces)
	}
	usageTrace := usageTraces[0]
	if usageTrace.Quality != core.ObservationQualityLegacy || usageTrace.Status != core.ObservationStatusOK ||
		usageTrace.AgentID != agent.ID || usageTrace.AgentName != agent.Name || usageTrace.RuntimeID != "codex" ||
		usageTrace.ConversationID != conversation.ID || usageTrace.SessionID != "session-legacy" {
		t.Fatalf("usage correlation = %+v", usageTrace)
	}
	if usageTrace.Usage.InputTokens != 120 || usageTrace.Usage.OutputTokens != 30 ||
		usageTrace.Usage.CacheReadTokens != 20 || usageTrace.Usage.CacheWriteTokens != 10 || usageTrace.Usage.CostUSD != 0.012 {
		t.Fatalf("usage summary = %+v", usageTrace.Usage)
	}
	usageSpans, err := st.ListObservationSpans(ctx, usageTrace.TraceID)
	if err != nil || len(usageSpans) != 1 || usageSpans[0].Model == nil || usageSpans[0].Model.RequestID != "usage-request" {
		t.Fatalf("usage spans = %+v, err=%v", usageSpans, err)
	}
	if usageSpans[0].Attributes["coverage"] != core.ObservationQualityPartial || usageSpans[0].Attributes["legacy_table"] != "usage_records" {
		t.Fatalf("usage attributes = %+v", usageSpans[0].Attributes)
	}

	var eventCount int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM observation_events WHERE source='legacy_usage'`).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("legacy event count = %d, want 1", eventCount)
	}
	usageTraceID := usageTrace.TraceID
	var usageUpdatedAt string
	if err := st.db.QueryRowContext(ctx, `SELECT updated_at FROM observation_traces WHERE trace_id=?`, usageTraceID).Scan(&usageUpdatedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM settings WHERE key=?`, legacyUsageImportCursorKey); err != nil {
		t.Fatal(err)
	}
	recovered, err := st.ImportLegacyObservations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != (LegacyObservationImportResult{UsageScanned: 1}) {
		t.Fatalf("cursor recovery import = %+v", recovered)
	}
	var usageUpdatedAfterRecovery string
	if err := st.db.QueryRowContext(ctx, `SELECT updated_at FROM observation_traces WHERE trace_id=?`, usageTraceID).Scan(&usageUpdatedAfterRecovery); err != nil {
		t.Fatal(err)
	}
	if usageUpdatedAfterRecovery != usageUpdatedAt {
		t.Fatalf("already imported usage was replayed: updated_at %q -> %q", usageUpdatedAt, usageUpdatedAfterRecovery)
	}

	second, err := st.ImportLegacyObservations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second != (LegacyObservationImportResult{}) {
		t.Fatalf("second import = %+v", second)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM observation_events WHERE source='legacy_usage'`).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("idempotent event count = %d, want 1", eventCount)
	}
	usageTraces, err = st.ListObservationTraces(ctx, ObservationTraceFilter{Source: "legacy_usage", Limit: 10})
	if err != nil || len(usageTraces) != 1 || usageTraces[0].TraceID != usageTraceID {
		t.Fatalf("stable usage trace = %+v, err=%v", usageTraces, err)
	}

	if err := st.UpsertUsage(ctx, []core.UsageRecord{{
		Source: "codex", SessionID: "session-legacy", RequestID: "usage-request-2", Project: agent.WorkDir,
		Model: "gpt-5", Timestamp: ended.Add(time.Second), InputTokens: 10, OutputTokens: 2,
	}}); err != nil {
		t.Fatal(err)
	}
	third, err := st.ImportLegacyObservations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if third != (LegacyObservationImportResult{UsageScanned: 1, UsageImported: 1}) {
		t.Fatalf("incremental import = %+v", third)
	}
	if fourth, err := st.ImportLegacyObservations(ctx); err != nil || fourth != (LegacyObservationImportResult{}) {
		t.Fatalf("incremental import replay = %+v, err=%v", fourth, err)
	}
}
