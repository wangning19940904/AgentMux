package store

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func TestObservationRecorderEncryptsRedactsAndDeduplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observations.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	masterKey := bytes.Repeat([]byte{0x42}, 32)
	recorder, err := NewObservationRecorder(store, ObservationRecorderOptions{
		CaptureContent: true,
		MasterKey:      masterKey,
		KnownSecrets:   []string{"project-secret-value"},
		Now:            func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	traceID := core.NewObservationTraceID()
	spanID := core.NewObservationSpanID()
	start := core.ObservationEnvelope{
		EventID: "event-start", TraceID: traceID, SpanID: spanID, Sequence: 1, Time: now,
		Kind: "model.request", Name: "Claude request", Lifecycle: core.ObservationLifecycleStart,
		AgentID: "agent-1", RuntimeID: "claude", SessionID: "session-1", Source: "agentmux",
		Status: core.ObservationStatusRunning,
		Model:  &core.ObservationModel{Requested: "claude-sonnet", RequestID: "request-1"},
		Content: &core.ObservationContent{ContentType: "application/json", Data: []byte(`{
			"prompt":"hello project-secret-value", "authorization":"Bearer unsafe-token-value",
			"nested":{"api_key":"sk-ant-12345678901234567890"}
		}`)},
	}
	if err := recorder.Observe(ctx, start); err != nil {
		t.Fatal(err)
	}
	end := start
	end.EventID = "event-end"
	end.DedupeKey = "claude:request-1:completed"
	end.Sequence = 2
	end.Lifecycle = core.ObservationLifecycleEnd
	end.Status = core.ObservationStatusOK
	end.Content = nil
	end.Usage = &core.ObservationUsage{InputTokens: 100, OutputTokens: 25, CacheReadTokens: 10, CostUSD: 0.01, Cumulative: true}
	if err := recorder.Observe(ctx, end); err != nil {
		t.Fatal(err)
	}
	// Replaying the same event and a new event with the same stable dedupe key
	// must not inflate counts or usage.
	if err := recorder.Observe(ctx, end); err != nil {
		t.Fatal(err)
	}
	duplicate := end
	duplicate.EventID = "event-end-from-hook"
	duplicate.Source = "native_hook"
	if err := recorder.Observe(ctx, duplicate); err != nil {
		t.Fatal(err)
	}
	// Content-bearing replay must be rejected before allocating another
	// encrypted payload.
	if err := recorder.Observe(ctx, start); err != nil {
		t.Fatal(err)
	}

	trace, err := store.GetObservationTrace(ctx, traceID)
	if err != nil {
		t.Fatal(err)
	}
	if trace == nil || trace.EventCount != 2 || trace.SpanCount != 1 || trace.Status != core.ObservationStatusOK {
		t.Fatalf("trace summary = %+v", trace)
	}
	if trace.Usage.InputTokens != 100 || trace.Usage.OutputTokens != 25 || trace.Usage.TotalTokens != 135 {
		t.Fatalf("trace usage = %+v", trace.Usage)
	}
	spans, err := store.ListObservationSpans(ctx, traceID)
	if err != nil || len(spans) != 1 || spans[0].Model == nil || spans[0].Model.RequestID != "request-1" {
		t.Fatalf("spans = %+v, err=%v", spans, err)
	}
	events, err := store.ListObservationEvents(ctx, traceID, 0, 10)
	if err != nil || len(events) != 2 || events[0].PayloadRef == nil {
		t.Fatalf("events = %+v, err=%v", events, err)
	}
	plaintext, contentType, err := recorder.ReadPayload(ctx, events[0].PayloadRef.ID)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "application/json" || bytes.Contains(plaintext, []byte("project-secret-value")) || bytes.Contains(plaintext, []byte("unsafe-token-value")) || bytes.Contains(plaintext, []byte("sk-ant-")) {
		t.Fatalf("payload not safely redacted: %s", plaintext)
	}
	if bytes.Count(plaintext, []byte("[REDACTED]")) < 3 {
		t.Fatalf("payload redactions missing: %s", plaintext)
	}
	var payloads, orphanPayloads int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM observation_payloads`).Scan(&payloads); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM observation_payloads p WHERE NOT EXISTS
		(SELECT 1 FROM observation_events e WHERE e.payload_id=p.payload_id)`).Scan(&orphanPayloads); err != nil {
		t.Fatal(err)
	}
	if payloads != 1 || orphanPayloads != 0 {
		t.Fatalf("payloads = %d, orphans = %d; replay leaked encrypted content", payloads, orphanPayloads)
	}

	var wrapped []byte
	if err := store.db.QueryRow(`SELECT wrapped_key FROM observation_data_keys WHERE key_id='2026-07-11'`).Scan(&wrapped); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(wrapped, masterKey) || bytes.Equal(wrapped, masterKey) {
		t.Fatal("master/data key material was stored without wrapping")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{path, path + "-wal"} {
		raw, readErr := os.ReadFile(candidate)
		if readErr != nil && !os.IsNotExist(readErr) {
			t.Fatal(readErr)
		}
		for _, secret := range [][]byte{[]byte("project-secret-value"), []byte("unsafe-token-value"), []byte("sk-ant-12345678901234567890")} {
			if bytes.Contains(raw, secret) {
				t.Fatalf("database file %s contains plaintext secret %q", candidate, secret)
			}
		}
	}
}

func TestConcurrentObservationReplayDoesNotLeakPayloads(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "concurrent-replay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	recorder, err := NewObservationRecorder(st, ObservationRecorderOptions{
		CaptureContent: true, MasterKey: bytes.Repeat([]byte{0x25}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	envelope := core.ObservationEnvelope{
		EventID: "same-event", DedupeKey: "same-dedupe", TraceID: "same-trace", SpanID: "same-span", Kind: "agent.turn",
		Content: &core.ObservationContent{Data: bytes.Repeat([]byte("content"), 1024)},
	}
	const workers = 16
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- recorder.Observe(context.Background(), envelope)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var events, payloads, chunks int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM observation_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM observation_payloads`).Scan(&payloads); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM observation_payload_chunks`).Scan(&chunks); err != nil {
		t.Fatal(err)
	}
	if events != 1 || payloads != 1 || chunks != 1 {
		t.Fatalf("events=%d payloads=%d chunks=%d, want one durable copy", events, payloads, chunks)
	}
}

func TestObservationCleanupRemovesOrphansInBatches(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "orphan-cleanup.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	created := observationTime(now.Add(-2 * time.Hour))
	expires := observationTime(now.Add(24 * time.Hour))
	want := observationCleanupBatchSize + 17
	tx, err := st.writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for index := range want {
		id := "orphan-" + strconv.Itoa(index)
		if _, err := tx.ExecContext(ctx, `INSERT INTO observation_payloads
			(payload_id,key_id,content_type,compression,encryption,nonce,ciphertext,sha256,original_bytes,stored_bytes,redacted,created_at,expires_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, "2026-07-21", "text/plain", "gzip-chunks", "AES-256-GCM",
			[]byte{}, []byte{}, "digest", 1, 1, false, created, expires); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO observation_payload_chunks
			(payload_id,chunk_index,nonce,ciphertext,original_bytes,stored_bytes) VALUES(?,?,?,?,?,?)`,
			id, 0, []byte{1}, []byte{2}, 1, 1); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	result, err := st.CleanupObservationRetention(ctx, now, 180*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if result.Payloads != int64(want) {
		t.Fatalf("cleaned payloads = %d, want %d", result.Payloads, want)
	}
	var payloads, chunks int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM observation_payloads`).Scan(&payloads); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM observation_payload_chunks`).Scan(&chunks); err != nil {
		t.Fatal(err)
	}
	if payloads != 0 || chunks != 0 {
		t.Fatalf("payloads=%d chunks=%d after cleanup", payloads, chunks)
	}
}

func TestObservationOrphanLookupUsesPayloadIndex(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "orphan-plan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rows, err := st.db.Query(`EXPLAIN QUERY PLAN SELECT payload_id FROM observation_payloads WHERE `+
		observationOrphanPayloadPredicate+` ORDER BY created_at LIMIT ?`, observationTime(time.Now()), observationCleanupBatchSize)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail)
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.String(), "idx_observation_events_payload") {
		t.Fatalf("orphan lookup does not use payload index:\n%s", plan.String())
	}
}

func TestObservationRecorderSanitizesExternalAttributesBeforeSQLite(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "attributes.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	recorder, err := NewObservationRecorder(st, ObservationRecorderOptions{
		CaptureContent: false, KnownSecrets: []string{"known-secret-value"},
	})
	if err != nil {
		t.Fatal(err)
	}
	secured, err := recorder.Record(context.Background(), core.ObservationEnvelope{
		EventID: "external-attributes", TraceID: "external-trace", SpanID: "external-span", Kind: "tool.call",
		Source: "external.plugin", Attributes: map[string]any{
			"prompt": "plain-private-prompt", "authorization": "Bearer external-token-value",
			"safe":   "prefix known-secret-value suffix",
			"nested": map[string]any{"output": "plain-tool-output", "count": 3},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(secured.Attributes)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"plain-private-prompt", "external-token-value", "known-secret-value", "plain-tool-output"} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("sanitized attributes leaked %q: %s", forbidden, raw)
		}
	}
	var stored string
	if err := st.db.QueryRow(`SELECT envelope_json FROM observation_events WHERE event_id='external-attributes'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "plain-private-prompt") || strings.Contains(stored, "known-secret-value") || !strings.Contains(stored, "[REDACTED_CONTENT]") {
		t.Fatalf("stored envelope attributes are unsafe: %s", stored)
	}
}

func TestObservationRecorderNeverPersistsHiddenReasoningContent(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "hidden-reasoning.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	recorder, err := NewObservationRecorder(st, ObservationRecorderOptions{
		CaptureContent: true, MasterKey: bytes.Repeat([]byte{7}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	secured, err := recorder.Record(context.Background(), core.ObservationEnvelope{
		EventID: "hidden-reasoning", TraceID: "hidden-trace", SpanID: "hidden-span", Kind: "model.reasoning",
		Content: &core.ObservationContent{ContentType: "text/plain", Data: []byte("never-store-private-cot")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if secured.PayloadRef != nil || secured.Attributes["content_capture"] != "suppressed_hidden_reasoning" {
		t.Fatalf("hidden reasoning was not suppressed: %+v", secured)
	}
	var payloads int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM observation_payloads`).Scan(&payloads); err != nil || payloads != 0 {
		t.Fatalf("hidden reasoning payload count = %d, err=%v", payloads, err)
	}
	publicSummary, err := recorder.Record(context.Background(), core.ObservationEnvelope{
		EventID: "public-summary", TraceID: "summary-trace", SpanID: "summary-span", Kind: "model.reasoning_summary",
		Content: &core.ObservationContent{ContentType: "text/plain", Data: []byte("public summary")},
	})
	if err != nil || publicSummary.PayloadRef == nil {
		t.Fatalf("public reasoning summary should remain available: %+v, err=%v", publicSummary, err)
	}
}

func TestObservationTraceListFilters(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "observations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	base := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	for index, agentID := range []string{"agent-a", "agent-b", "agent-a"} {
		envelope := core.ObservationEnvelope{
			EventID: "filter-event-" + string(rune('a'+index)), TraceID: core.NewObservationTraceID(), SpanID: core.NewObservationSpanID(),
			Time: base.Add(time.Duration(index) * time.Hour), Kind: "agent.turn", AgentID: agentID, Status: core.ObservationStatusOK,
		}
		if err := store.RecordObservation(ctx, envelope); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := store.ListObservationTraces(ctx, ObservationTraceFilter{AgentID: "agent-a", Since: base.Add(30 * time.Minute), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].AgentID != "agent-a" || !rows[0].StartedAt.Equal(base.Add(2*time.Hour)) {
		t.Fatalf("filtered rows = %+v", rows)
	}
}

func TestObservationRetentionKeepsMetadataAfterContentExpiry(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "observations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recorder, err := NewObservationRecorder(store, ObservationRecorderOptions{
		CaptureContent: true, MasterKey: bytes.Repeat([]byte{7}, 32),
		ContentRetention: 30 * 24 * time.Hour, DetailRetention: 180 * 24 * time.Hour,
		Now: func() time.Time { return created },
	})
	if err != nil {
		t.Fatal(err)
	}
	envelope := core.ObservationEnvelope{EventID: "retained", TraceID: "retained-trace", SpanID: "retained-span", Time: created,
		Kind: "tool.call", Status: core.ObservationStatusOK, Content: &core.ObservationContent{Data: []byte("safe content")}}
	if err := recorder.Observe(ctx, envelope); err != nil {
		t.Fatal(err)
	}
	cleanup, err := recorder.Cleanup(ctx, created.Add(31*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if cleanup.Payloads != 1 || cleanup.DataKeys != 1 || cleanup.Events != 0 || cleanup.Spans != 0 || cleanup.Traces != 0 {
		t.Fatalf("31-day cleanup = %+v", cleanup)
	}
	trace, err := store.GetObservationTrace(ctx, "retained-trace")
	if err != nil || trace == nil {
		t.Fatalf("trace should remain for detail retention: %+v, %v", trace, err)
	}
	cleanup, err = recorder.Cleanup(ctx, created.Add(181*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if cleanup.Events != 1 || cleanup.Spans != 1 || cleanup.Traces != 1 || cleanup.DataKeys != 0 {
		t.Fatalf("181-day cleanup = %+v", cleanup)
	}
}

func TestBackfilledContentExpiresFromEventTimeAndExpiredContentIsDropped(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "backfill-retention.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ingestedAt := time.Date(2026, 1, 30, 0, 0, 0, 0, time.UTC)
	recorder, err := NewObservationRecorder(st, ObservationRecorderOptions{
		CaptureContent: true, MasterKey: bytes.Repeat([]byte{6}, 32), ContentRetention: 30 * 24 * time.Hour,
		Now: func() time.Time { return ingestedAt },
	})
	if err != nil {
		t.Fatal(err)
	}
	backfilled, err := recorder.Record(context.Background(), core.ObservationEnvelope{
		EventID: "backfilled-content", TraceID: "backfilled-trace", SpanID: "backfilled-span", Kind: "agent.turn",
		Time: ingestedAt.Add(-15 * 24 * time.Hour), Content: &core.ObservationContent{Data: []byte("recent backfill")},
	})
	if err != nil || backfilled.PayloadRef == nil {
		t.Fatalf("backfilled payload = %+v, err=%v", backfilled.PayloadRef, err)
	}
	wantExpiry := ingestedAt.Add(15 * 24 * time.Hour)
	if !backfilled.PayloadRef.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("expires_at = %v, want %v", backfilled.PayloadRef.ExpiresAt, wantExpiry)
	}
	expired, err := recorder.Record(context.Background(), core.ObservationEnvelope{
		EventID: "expired-content", TraceID: "expired-trace", SpanID: "expired-span", Kind: "agent.turn",
		Time: ingestedAt.Add(-31 * 24 * time.Hour), Content: &core.ObservationContent{Data: []byte("expired backfill")},
	})
	if err != nil || expired.PayloadRef != nil || expired.Attributes["content_capture"] != "expired_before_ingest" {
		t.Fatalf("expired payload should be metadata-only: %+v, err=%v", expired, err)
	}
}

func TestObservationPayloadIsCompressedAndEncryptedInChunks(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "chunks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	recorder, err := NewObservationRecorder(st, ObservationRecorderOptions{
		CaptureContent: true, MasterKey: bytes.Repeat([]byte{3}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("0123456789abcdef"), (2*observationPayloadChunkBytes+12345)/16+1)
	payload = payload[:2*observationPayloadChunkBytes+12345]
	envelope := core.ObservationEnvelope{
		TraceID: core.NewObservationTraceID(), SpanID: core.NewObservationSpanID(), Kind: "tool.call",
		Content: &core.ObservationContent{ContentType: "application/octet-stream", Data: payload},
	}
	secured, err := recorder.Record(context.Background(), envelope)
	if err != nil {
		t.Fatal(err)
	}
	var chunks int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM observation_payload_chunks WHERE payload_id=?`, secured.PayloadRef.ID).Scan(&chunks); err != nil {
		t.Fatal(err)
	}
	if chunks != 3 {
		t.Fatalf("chunks = %d, want 3", chunks)
	}
	decoded, contentType, err := recorder.ReadPayload(context.Background(), secured.PayloadRef.ID)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "application/octet-stream" || !bytes.Equal(decoded, payload) {
		t.Fatal("chunked payload round-trip mismatch")
	}
}

func TestObservationTranscriptSourceReferenceAvoidsPayloadCopyAndStillRedacts(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "source-ref.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC()
	recorder, err := NewObservationRecorder(st, ObservationRecorderOptions{
		CaptureContent: true, MasterKey: bytes.Repeat([]byte{7}, 32), KnownSecrets: []string{"local-secret"},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	content := core.ObservationContent{ContentType: "application/json", Data: []byte(`{"output":"local-secret"}`)}
	recorder.SetPayloadSourceResolver(func(context.Context, core.ObservationPayloadRef) ([]core.ObservationContent, error) {
		return []core.ObservationContent{content}, nil
	})
	envelope := core.ObservationEnvelope{
		EventID: "source-event", TraceID: "source-trace", SpanID: "source-span", Time: now,
		Kind: "tool.call", Source: "transcript",
		Content: &core.ObservationContent{
			ContentType: content.ContentType, Data: content.Data,
			Source: &core.ObservationContentSource{
				Storage: core.ObservationPayloadStorageTranscriptFile, Path: filepath.Join(t.TempDir(), "rollout.jsonl"),
				Offset: 42, Length: 128, SHA256: strings.Repeat("a", 64), Runtime: "codex", Class: "active",
			},
		},
	}
	secured, err := recorder.Record(context.Background(), envelope)
	if err != nil {
		t.Fatal(err)
	}
	if secured.PayloadRef == nil || secured.PayloadRef.Storage != core.ObservationPayloadStorageTranscriptFile || secured.PayloadRef.StoredBytes != 0 {
		t.Fatalf("source payload ref = %+v", secured.PayloadRef)
	}
	if secured.PayloadRef.SourceContentSHA256 == "" || secured.PayloadRef.ContentSHA256 != "" {
		t.Fatalf("new source reference should defer redaction to expansion: %+v", secured.PayloadRef)
	}
	var payloads, chunks int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM observation_payloads`).Scan(&payloads); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM observation_payload_chunks`).Scan(&chunks); err != nil {
		t.Fatal(err)
	}
	if payloads != 0 || chunks != 0 {
		t.Fatalf("source reference copied payload into SQLite: payloads=%d chunks=%d", payloads, chunks)
	}
	decoded, contentType, err := recorder.ReadEnvelopePayload(context.Background(), secured)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "application/json" || bytes.Contains(decoded, []byte("local-secret")) || !bytes.Contains(decoded, []byte("[REDACTED]")) {
		t.Fatalf("source payload was not safely materialized: %s", decoded)
	}
}

func TestObservationMetadataOnlyNeverPersistsContent(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "observations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	recorder, err := NewObservationRecorder(store, ObservationRecorderOptions{CaptureContent: false})
	if err != nil {
		t.Fatal(err)
	}
	if !recorder.MetadataOnly() {
		t.Fatal("recorder should be metadata-only")
	}
	envelope := core.ObservationEnvelope{EventID: "metadata", TraceID: "metadata-trace", SpanID: "metadata-span", Kind: "agent.turn",
		Content: &core.ObservationContent{Data: []byte("must-not-persist")}}
	if err := recorder.Observe(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM observation_payloads`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("payload count = %d, err=%v", count, err)
	}
	events, err := store.ListObservationEvents(context.Background(), "metadata-trace", 0, 10)
	if err != nil || len(events) != 1 || events[0].PayloadRef != nil || events[0].Attributes["content_capture"] != "metadata_only" {
		t.Fatalf("metadata event = %+v, err=%v", events, err)
	}
}

func TestRedactObservationContentJSONAndText(t *testing.T) {
	jsonInput := []byte(`{"prompt":"keep this", "cookie":"session=secret", "nested":{"password":{"unexpected":"shape"}}, "reasoning_summary":"public"}`)
	redacted, changed := RedactObservationContent(jsonInput, nil)
	if !changed || bytes.Contains(redacted, []byte("session=secret")) || bytes.Contains(redacted, []byte(`"unexpected"`)) {
		t.Fatalf("JSON redaction failed: %s", redacted)
	}
	var decoded map[string]any
	if err := json.Unmarshal(redacted, &decoded); err != nil || decoded["prompt"] != "keep this" || decoded["reasoning_summary"] != "public" {
		t.Fatalf("JSON structure/public summary not preserved: %s, %v", redacted, err)
	}
	textInput := []byte("Authorization: Bearer top-secret\napi_key=abcdef123456&query=ok\nknown-value")
	redacted, changed = RedactObservationContent(textInput, []string{"known-value"})
	if !changed || strings.Contains(string(redacted), "top-secret") || strings.Contains(string(redacted), "abcdef123456") || strings.Contains(string(redacted), "known-value") {
		t.Fatalf("text redaction failed: %s", redacted)
	}
}

func TestObservationOutboxInsightOwnershipAndLease(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "observations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	envelope := core.ObservationEnvelope{EventID: "export-event", TraceID: "export-trace", SpanID: "export-span", Kind: "model.request",
		Content: &core.ObservationContent{Data: []byte("plaintext must not enter outbox")}}
	if err := store.EnqueueObservationExport(ctx, "collector", envelope, false); err != nil {
		t.Fatal(err)
	}
	if err := store.EnqueueObservationExport(ctx, "collector", envelope, false); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListPendingObservationExports(ctx, "collector", time.Now().Add(time.Minute), 10)
	if err != nil || len(items) != 1 || items[0].Envelope.Content != nil || items[0].IncludeContent {
		t.Fatalf("outbox items = %+v, err=%v", items, err)
	}
	if err := store.RetryObservationExport(ctx, items[0].ID, context.DeadlineExceeded, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	items, err = store.ListPendingObservationExports(ctx, "collector", time.Now().Add(2*time.Hour), 10)
	if err != nil || len(items) != 1 || items[0].Attempts != 1 || items[0].LastError == "" {
		t.Fatalf("retried outbox items = %+v, err=%v", items, err)
	}
	if err := store.CompleteObservationExport(ctx, items[0].ID); err != nil {
		t.Fatal(err)
	}

	insight := ObservationInsight{ID: "insight-1", RuleID: "tool_failure_rate", AgentID: "agent-1", Title: "Review failing tool",
		SampleSize: 25, Confidence: 0.9, RelatedTraceIDs: []string{"trace-1"}, Suggestion: "Try a narrower schema"}
	if err := store.UpsertObservationInsight(ctx, insight); err != nil {
		t.Fatal(err)
	}
	insights, err := store.ListObservationInsights(ctx, ObservationInsightFilter{AgentID: "agent-1"})
	if err != nil || len(insights) != 1 || !insights[0].OnlySuggestion || insights[0].SampleSize != 25 {
		t.Fatalf("insights = %+v, err=%v", insights, err)
	}

	ownership := ObservationIntegrationOwnership{InstallID: "install-a", Host: "local", Scope: "codex", ResourceKey: "agentmux-observer",
		HandlerFingerprint: "fingerprint-a", TargetPath: "/tmp/plugin"}
	claimed, err := store.ClaimObservationIntegrationOwnership(ctx, ownership)
	if err != nil || !claimed {
		t.Fatalf("claim ownership = %v, %v", claimed, err)
	}
	other := ownership
	other.InstallID = "install-b"
	claimed, err = store.ClaimObservationIntegrationOwnership(ctx, other)
	if claimed || err != ErrObservationResourceOwned {
		t.Fatalf("conflicting claim = %v, %v", claimed, err)
	}
	deleted, err := store.DeleteObservationIntegrationOwnership(ctx, ownership.InstallID, ownership.ResourceKey, "drifted")
	if err != nil || deleted {
		t.Fatalf("drifted delete = %v, %v", deleted, err)
	}
	deleted, err = store.DeleteObservationIntegrationOwnership(ctx, ownership.InstallID, ownership.ResourceKey, ownership.HandlerFingerprint)
	if err != nil || !deleted {
		t.Fatalf("owned delete = %v, %v", deleted, err)
	}

	lease, acquired, err := store.AcquireObservationResourceLease(ctx, "config:codex", "owner-a", "install-a", time.Minute, nil)
	if err != nil || !acquired || lease == nil {
		t.Fatalf("acquire lease = %+v, %v, %v", lease, acquired, err)
	}
	_, acquired, err = store.AcquireObservationResourceLease(ctx, "config:codex", "owner-b", "install-b", time.Minute, nil)
	if err != nil || acquired {
		t.Fatalf("steal live lease = %v, %v", acquired, err)
	}
	released, err := store.ReleaseObservationResourceLease(ctx, "config:codex", "wrong-token")
	if err != nil || released {
		t.Fatalf("wrong-token release = %v, %v", released, err)
	}
	released, err = store.ReleaseObservationResourceLease(ctx, "config:codex", lease.LeaseToken)
	if err != nil || !released {
		t.Fatalf("release lease = %v, %v", released, err)
	}
}
