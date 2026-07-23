package observability

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentnexus/agentnexus/config"
	"github.com/agentnexus/agentnexus/core"
	"github.com/agentnexus/agentnexus/store"
)

func TestMigrateTranscriptPayloadReferencesValidatesAndRemovesEncryptedCopy(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	now := time.Now().UTC().Add(-time.Minute)
	st, err := store.Open(filepath.Join(home, ".agentnexus", "migration.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	path := filepath.Join(home, ".codex", "sessions", "2026", "07", "21", "rollout-migration.jsonl")
	writeJSONL(t, path,
		map[string]any{"timestamp": now.Add(-3 * time.Second).Format(time.RFC3339Nano), "type": "session_meta", "payload": map[string]any{"id": "migration-session"}},
		map[string]any{"timestamp": now.Add(-2 * time.Second).Format(time.RFC3339Nano), "type": "event_msg", "payload": map[string]any{"type": "task_started", "turn_id": "migration-turn"}},
		map[string]any{"timestamp": now.Format(time.RFC3339Nano), "type": "response_item", "payload": map[string]any{
			"type": "function_call_output", "id": "migration-output", "call_id": "migration-call", "output": "tool-output",
		}},
	)
	captureBus := core.NewObservationBus()
	captured := &capturedObservations{}
	captureBus.Subscribe("capture", captured.observe)
	tailer := NewTranscriptTailer(nil, st, captureBus, TranscriptTailerOptions{Home: home, Now: func() time.Time { return now.Add(time.Minute) }})
	if _, err := tailer.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	var legacy core.ObservationEnvelope
	for _, event := range captured.snapshot() {
		if event.Kind == "tool.call" && event.Lifecycle == core.ObservationLifecycleEnd && event.Content != nil {
			legacy = event
			break
		}
	}
	if legacy.Content == nil || legacy.Content.Source == nil {
		t.Fatal("transcript tool output did not carry a source reference")
	}
	t.Setenv("AGENTNEXUS_TEST_SOURCE_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	runtime, err := NewRuntime(nil, config.ObservabilityConfig{
		Enabled: true, CaptureContent: "full", MasterKeyEnv: "AGENTNEXUS_TEST_SOURCE_KEY",
		ContentRetentionDays: 30, DetailRetentionDays: 180, BackfillDays: 180,
	}, st, home, nil)
	if err != nil {
		t.Fatal(err)
	}
	direct := legacy
	direct.EventID += "-source"
	direct.DedupeKey += ":source"
	directSecured, err := runtime.Recorder.Record(ctx, direct)
	if err != nil {
		t.Fatal(err)
	}
	if directSecured.PayloadRef == nil || directSecured.PayloadRef.Storage != core.ObservationPayloadStorageTranscriptFile {
		t.Fatalf("new transcript payload was copied instead of referenced: %+v", directSecured.PayloadRef)
	}
	directContent, _, err := runtime.Recorder.ReadEnvelopePayload(ctx, directSecured)
	if err != nil || !bytes.Contains(directContent, []byte("tool-output")) {
		t.Fatalf("new transcript source payload = %s, err=%v", directContent, err)
	}
	legacy.EventID += "-legacy"
	legacy.DedupeKey += ":legacy"
	legacy.Content = &core.ObservationContent{ContentType: legacy.Content.ContentType, Data: append([]byte(nil), legacy.Content.Data...)}
	secured, err := runtime.Recorder.Record(ctx, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if secured.PayloadRef == nil || secured.PayloadRef.Storage != "" {
		t.Fatalf("legacy payload was not encrypted first: %+v", secured.PayloadRef)
	}
	before, err := st.ListObservationTranscriptPayloadCandidates(ctx, 0, 10)
	if err != nil || len(before) != 1 {
		t.Fatalf("migration candidates before = %+v, err=%v", before, err)
	}
	result, err := runtime.MigrateTranscriptPayloadReferences(ctx, 16, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Replaced != 1 || result.ValidationFailed != 0 || result.StoredBytes == 0 {
		t.Fatalf("migration result = %+v", result)
	}
	events, err := st.ListObservationEvents(ctx, legacy.TraceID, 0, 10)
	if err != nil || len(events) == 0 {
		t.Fatalf("events after migration = %+v, err=%v", events, err)
	}
	var migrated core.ObservationEnvelope
	for _, event := range events {
		if event.EventID == legacy.EventID {
			migrated = event
			break
		}
	}
	if migrated.PayloadRef == nil || migrated.PayloadRef.Storage != core.ObservationPayloadStorageTranscriptFile {
		t.Fatalf("migrated payload ref = %+v", migrated.PayloadRef)
	}
	decoded, _, err := runtime.Recorder.ReadEnvelopePayload(ctx, migrated)
	if err != nil || !bytes.Contains(decoded, []byte("tool-output")) {
		t.Fatalf("materialized migrated payload = %s, err=%v", decoded, err)
	}
	after, err := st.ListObservationTranscriptPayloadCandidates(ctx, 0, 10)
	if err != nil || len(after) != 0 {
		t.Fatalf("migration candidates remain = %+v, err=%v", after, err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("X"), migrated.PayloadRef.SourceOffset); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	_ = file.Close()
	if _, _, err := runtime.Recorder.ReadEnvelopePayload(ctx, migrated); err == nil {
		t.Fatal("mutated transcript source bypassed checksum validation")
	}
}
