package store

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func openPostgresIntegrationStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("AGENTMUX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("AGENTMUX_TEST_DATABASE_URL is not set")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("amux_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + quoteIdentifier(schema)); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	st, err := OpenPostgres(context.Background(), DatabaseConfig{URL: parsed.String()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = st.Close()
		_, _ = admin.Exec(`DROP SCHEMA ` + quoteIdentifier(schema) + ` CASCADE`)
		_ = admin.Close()
	})
	return st
}

func TestPostgresObservationBatchDeduplicatesConcurrently(t *testing.T) {
	st := openPostgresIntegrationStore(t)
	recorder, err := NewObservationRecorder(st, ObservationRecorderOptions{CaptureContent: false})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	envelope := core.ObservationEnvelope{
		EventID: "obs_postgres_duplicate", DedupeKey: "postgres-duplicate",
		TraceID: "trace_postgres", SpanID: "span_postgres", Kind: "agent.turn",
		Lifecycle: core.ObservationLifecycleEnd, Status: core.ObservationStatusOK, Time: now,
	}
	const writers = 100
	var group sync.WaitGroup
	group.Add(writers)
	for range writers {
		go func() {
			defer group.Done()
			if _, err := recorder.Record(context.Background(), envelope); err != nil {
				t.Errorf("Record: %v", err)
			}
		}()
	}
	group.Wait()
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := recorder.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	events, err := st.ListObservationEvents(context.Background(), envelope.TraceID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	trace, err := st.GetObservationTrace(context.Background(), envelope.TraceID)
	if err != nil {
		t.Fatal(err)
	}
	if trace == nil || trace.EventCount != 1 || trace.SpanCount != 1 {
		t.Fatalf("trace summary = %+v", trace)
	}
	stats := recorder.Stats()
	if stats.Inserted != 1 || stats.Deduplicated != writers-1 {
		t.Fatalf("stats = %+v", stats)
	}
	if stats.Batches >= writers {
		t.Fatalf("batches = %d, want fewer than %d", stats.Batches, writers)
	}
}

func TestPostgresDuplicateDoesNotWritePayload(t *testing.T) {
	st := openPostgresIntegrationStore(t)
	recorder, err := NewObservationRecorder(st, ObservationRecorderOptions{
		CaptureContent: true,
		MasterKey:      bytes.Repeat([]byte{0x42}, observationMasterKeySize),
	})
	if err != nil {
		t.Fatal(err)
	}
	envelope := core.ObservationEnvelope{
		EventID: "obs_postgres_payload_duplicate", DedupeKey: "postgres-payload-duplicate",
		TraceID: "trace_postgres_payload", SpanID: "span_postgres_payload", Kind: "model.response",
		Lifecycle: core.ObservationLifecycleEnd, Status: core.ObservationStatusOK, Time: time.Now().UTC(),
		Content: &core.ObservationContent{ContentType: "text/plain", Data: []byte("persist this once")},
	}
	if _, err := recorder.Record(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for recorder.Stats().Inserted != 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if recorder.Stats().Inserted != 1 {
		t.Fatalf("first event did not flush: %+v", recorder.Stats())
	}
	for range 100 {
		if _, err := recorder.Record(context.Background(), envelope); err != nil {
			t.Fatal(err)
		}
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := recorder.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	var payloads, chunks int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM observation_payloads`).Scan(&payloads); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM observation_payload_chunks`).Scan(&chunks); err != nil {
		t.Fatal(err)
	}
	if payloads != 1 || chunks != 1 {
		t.Fatalf("payloads=%d chunks=%d, want one encrypted payload and one chunk", payloads, chunks)
	}
}

func TestPostgresDirtyTraceMaterializesUsageAsOneSet(t *testing.T) {
	st := openPostgresIntegrationStore(t)
	recorder, err := NewObservationRecorder(st, ObservationRecorderOptions{CaptureContent: false})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for index, item := range []struct {
		source string
		usage  int64
	}{
		{source: "proxy", usage: 999},
		{source: "agentmux.internal", usage: 100},
	} {
		if _, err := recorder.Record(context.Background(), core.ObservationEnvelope{
			EventID: fmt.Sprintf("obs_usage_%d", index), TraceID: "trace_usage", SpanID: fmt.Sprintf("span_usage_%d", index),
			Kind: "model.request", Lifecycle: core.ObservationLifecycleEnd, Status: core.ObservationStatusOK,
			RuntimeID: "codex", Source: item.source, Time: now.Add(time.Duration(index) * time.Millisecond),
			Model: &core.ObservationModel{RequestID: "request-1", Attempt: 1},
			Usage: &core.ObservationUsage{InputTokens: item.usage, TotalTokens: item.usage},
		}); err != nil {
			t.Fatal(err)
		}
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := recorder.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	trace, err := st.GetObservationTrace(context.Background(), "trace_usage")
	if err != nil {
		t.Fatal(err)
	}
	if trace == nil || trace.Usage.InputTokens != 100 || trace.Usage.TotalTokens != 100 ||
		trace.EventCount != 2 || trace.SpanCount != 2 {
		t.Fatalf("materialized trace = %+v", trace)
	}
}

func TestPostgresMigratesLegacySQLite(t *testing.T) {
	target := openPostgresIntegrationStore(t)
	sourcePath := t.TempDir() + "/legacy.db"
	source, err := Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := source.UpsertUsage(context.Background(), []core.UsageRecord{{
		Source: "test", SessionID: "session-migrate", Timestamp: now,
		InputTokens: 42, OutputTokens: 7, CostUSD: 1.25, Host: "localhost",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := source.RecordObservation(context.Background(), core.ObservationEnvelope{
		EventID: "obs_migrate", TraceID: "trace_migrate", SpanID: "span_migrate",
		Kind: "agent.turn", Lifecycle: core.ObservationLifecycleEnd,
		Status: core.ObservationStatusOK, Time: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := target.MigrateSQLite(context.Background(), SQLiteMigrationOptions{
		Source: sourcePath, BackupPath: sourcePath + ".backup",
		ObservationsSince: now.Add(-time.Hour), BatchSize: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Backup == "" {
		t.Fatal("migration did not create a backup")
	}
	trace, err := target.GetObservationTrace(context.Background(), "trace_migrate")
	if err != nil {
		t.Fatal(err)
	}
	if trace == nil || trace.EventCount != 1 {
		t.Fatalf("migrated trace = %+v", trace)
	}
	usage, err := target.QueryUsage(context.Background(), now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(usage) != 1 || usage[0].InputTokens != 42 || usage[0].CostUSD != 1.25 {
		t.Fatalf("migrated usage = %+v", usage)
	}
}

func TestPostgresObservationEnqueueP99(t *testing.T) {
	st := openPostgresIntegrationStore(t)
	recorder, err := NewObservationRecorder(st, ObservationRecorderOptions{CaptureContent: false})
	if err != nil {
		t.Fatal(err)
	}
	const events = 10000
	latencies := make([]time.Duration, 0, events)
	for index := range events {
		started := time.Now()
		_, err := recorder.Record(context.Background(), core.ObservationEnvelope{
			EventID: fmt.Sprintf("obs_latency_%d", index), TraceID: "trace_latency", SpanID: "span_latency",
			Kind: "custom.event", Status: core.ObservationStatusRunning, Time: time.Now().UTC(),
		})
		if err != nil {
			t.Fatal(err)
		}
		latencies = append(latencies, time.Since(started))
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p99 := latencies[(len(latencies)*99/100)-1]
	if p99 > time.Millisecond {
		t.Fatalf("Record p99 = %v, want <= 1ms", p99)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := recorder.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	stats := recorder.Stats()
	if stats.Dropped != 0 || stats.Inserted != events {
		t.Fatalf("writer stats = %+v", stats)
	}
	if stats.Batches > events/100 {
		t.Fatalf("transactions were not sufficiently batched: %d batches for %d events", stats.Batches, events)
	}
	t.Logf("Record p99=%v, batches=%d", p99, stats.Batches)
}
