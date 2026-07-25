package store

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func TestImportLegacyObservationsIsIdempotentAndCorrelates(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "legacy.db"))
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
	if err := st.InsertProxyTrace(ctx, core.ProxyTrace{
		ID: "proxy-row-1", RequestID: "proxy-request", TraceID: "preserved-proxy-trace", Attempt: 1,
		Timestamp: ended, StartedAt: started, Tool: "claudecode", ProviderID: "relay",
		ProviderName: "Relay", ClientProtocol: "anthropic", UpstreamProtocol: "anthropic",
		ClientModel: "claude-sonnet", UpstreamModel: "claude-sonnet-routed", StatusCode: 503,
		Success: false, Error: "Authorization: Bearer should-not-be-copied", SessionID: "session-legacy",
		ProjectDir: agent.WorkDir, TTFTMs: 200, DurationMs: 1250, StreamComplete: false,
		InputTokens: 40, OutputTokens: 5, CacheReadTokens: 2, RequestBytes: 600, ResponseBytes: 120,
	}); err != nil {
		t.Fatal(err)
	}

	first, err := st.ImportLegacyObservations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first != (LegacyObservationImportResult{UsageScanned: 1, UsageImported: 1, ProxyScanned: 1, ProxyImported: 1}) {
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

	proxyTrace, err := st.GetObservationTrace(ctx, "preserved-proxy-trace")
	if err != nil {
		t.Fatal(err)
	}
	if proxyTrace == nil || proxyTrace.Source != "legacy_proxy" || proxyTrace.Quality != core.ObservationQualityLegacy ||
		proxyTrace.Status != core.ObservationStatusError || proxyTrace.AgentID != agent.ID || proxyTrace.ConversationID != conversation.ID {
		t.Fatalf("proxy trace = %+v", proxyTrace)
	}
	proxySpans, err := st.ListObservationSpans(ctx, proxyTrace.TraceID)
	if err != nil || len(proxySpans) != 1 {
		t.Fatalf("proxy spans = %+v, err=%v", proxySpans, err)
	}
	proxySpan := proxySpans[0]
	if proxySpan.Model == nil || proxySpan.Model.RequestID != "proxy-request" || proxySpan.Model.Attempt != 1 ||
		proxySpan.Model.TTFTMillis != 200 || proxySpan.DurationMillis != 1250 || proxySpan.Error == nil ||
		proxySpan.Error.Message != "Legacy proxy request failed" || proxySpan.Usage.InputTokens != 40 {
		t.Fatalf("proxy span = %+v", proxySpan)
	}
	if !proxySpan.StartedAt.Equal(started) || proxySpan.EndedAt == nil || !proxySpan.EndedAt.Equal(ended) {
		t.Fatalf("proxy timing = start %s end %v", proxySpan.StartedAt, proxySpan.EndedAt)
	}
	proxyEvents, err := st.ListObservationEvents(ctx, proxyTrace.TraceID, 0, 10)
	if err != nil || len(proxyEvents) != 2 {
		t.Fatalf("proxy events = %+v, err=%v", proxyEvents, err)
	}
	rawEvents, _ := json.Marshal(proxyEvents)
	if strings.Contains(string(rawEvents), "should-not-be-copied") || strings.Contains(string(rawEvents), "Bearer") {
		t.Fatalf("legacy raw error copied into observation: %s", rawEvents)
	}

	var eventCount int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM observation_events WHERE source IN ('legacy_usage','legacy_proxy')`).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 3 {
		t.Fatalf("legacy event count = %d, want 3", eventCount)
	}
	usageTraceID := usageTrace.TraceID
	var usageUpdatedAt string
	if err := st.db.QueryRowContext(ctx, `SELECT updated_at FROM observation_traces WHERE trace_id=?`, usageTraceID).Scan(&usageUpdatedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM settings WHERE key IN (?,?)`, legacyUsageImportCursorKey, legacyProxyImportCursorKey); err != nil {
		t.Fatal(err)
	}
	recovered, err := st.ImportLegacyObservations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != (LegacyObservationImportResult{UsageScanned: 1, ProxyScanned: 1}) {
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
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM observation_events WHERE source IN ('legacy_usage','legacy_proxy')`).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 3 {
		t.Fatalf("idempotent event count = %d, want 3", eventCount)
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

func TestSecureLegacyProxyErrorsEncryptsThenClearsCompatibilityPlaintext(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "legacy-error.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := st.InsertProxyTrace(ctx, core.ProxyTrace{
		ID: "legacy-error-row", TraceID: "legacy-error-trace", RequestID: "legacy-error-request", Attempt: 1,
		Timestamp: now, StartedAt: now.Add(-time.Second), Tool: "codex", ProviderID: "provider",
		ClientProtocol: "openai_responses", UpstreamProtocol: "openai_responses", Success: false,
		Error: "Authorization: Bearer migration-secret-token",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ImportLegacyObservations(ctx); err != nil {
		t.Fatal(err)
	}
	recorder, err := NewObservationRecorder(st, ObservationRecorderOptions{
		CaptureContent: true, MasterKey: bytes.Repeat([]byte{5}, 32), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	secured, err := st.SecureLegacyProxyErrors(ctx, recorder.Observe)
	if err != nil || secured != 1 {
		t.Fatalf("secured=%d err=%v", secured, err)
	}
	rows, err := st.QueryProxyTraces(ctx, "codex", "", 10)
	if err != nil || len(rows) != 1 || rows[0].Error != "Legacy proxy request failed" {
		t.Fatalf("compatibility trace = %+v, err=%v", rows, err)
	}
	events, err := st.ListObservationEvents(ctx, "legacy-error-trace", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	var payloadID string
	for _, event := range events {
		if event.Kind == "proxy.error" && event.PayloadRef != nil {
			payloadID = event.PayloadRef.ID
		}
	}
	if payloadID == "" {
		t.Fatalf("encrypted proxy error event missing: %+v", events)
	}
	plaintext, _, err := recorder.ReadPayload(ctx, payloadID)
	if err != nil || bytes.Contains(plaintext, []byte("migration-secret-token")) || !bytes.Contains(plaintext, []byte("[REDACTED]")) {
		t.Fatalf("secured error = %q, err=%v", plaintext, err)
	}
	if secured, err := st.SecureLegacyProxyErrors(ctx, recorder.Observe); err != nil || secured != 0 {
		t.Fatalf("second migration secured=%d err=%v", secured, err)
	}
}

func TestImportLegacyObservationsReusesRequestSpanWithoutDoubleCounting(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "legacy-dedupe.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 2, 9, 0, 0, 0, time.UTC)
	const traceID = "shared-request-trace"
	const requestID = "shared-request"
	if err := st.UpsertUsage(ctx, []core.UsageRecord{{
		Source: "claude", SessionID: "shared-session", TraceID: traceID, RequestID: requestID,
		Model: "claude-sonnet", Timestamp: now, InputTokens: 100, OutputTokens: 20,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertProxyTrace(ctx, core.ProxyTrace{
		ID: "shared-proxy-row", RequestID: requestID, TraceID: traceID, Attempt: 1,
		Timestamp: now.Add(time.Second), StartedAt: now, Tool: "claudecode", ProviderID: "anthropic",
		ClientModel: "claude-sonnet", UpstreamModel: "claude-sonnet", Success: true, StreamComplete: true,
		InputTokens: 100, OutputTokens: 20,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ImportLegacyObservations(ctx); err != nil {
		t.Fatal(err)
	}
	trace, err := st.GetObservationTrace(ctx, traceID)
	if err != nil || trace == nil {
		t.Fatalf("trace = %+v, err=%v", trace, err)
	}
	spans, err := st.ListObservationSpans(ctx, traceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 1 {
		t.Fatalf("request sources created duplicate model spans: %+v", spans)
	}
	if trace.Usage.InputTokens != 100 || trace.Usage.OutputTokens != 20 || trace.Usage.TotalTokens != 120 {
		t.Fatalf("request usage was double counted: %+v", trace.Usage)
	}
}
