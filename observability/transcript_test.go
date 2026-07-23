package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentnexus/agentnexus/core"
	"github.com/agentnexus/agentnexus/store"
)

type capturedObservations struct {
	mu     sync.Mutex
	events []core.ObservationEnvelope
}

func (c *capturedObservations) observe(_ context.Context, envelope core.ObservationEnvelope) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, envelope)
	return nil
}

func (c *capturedObservations) snapshot() []core.ObservationEnvelope {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]core.ObservationEnvelope(nil), c.events...)
}

func (c *capturedObservations) reset() {
	c.mu.Lock()
	c.events = nil
	c.mu.Unlock()
}

func TestTranscriptTailerDiscoveryRetentionIncrementalAndTruncate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, ".agentnexus", "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bus := core.NewObservationBus()
	captured := &capturedObservations{}
	bus.Subscribe("capture", captured.observe)

	mainPath := filepath.Join(home, ".claude", "projects", "-tmp-project", "main.jsonl")
	metadataPath := filepath.Join(home, ".claude", "projects", "-tmp-project", "metadata.jsonl")
	expiredPath := filepath.Join(home, ".claude", "projects", "-tmp-project", "expired.jsonl")
	subagentPath := filepath.Join(home, ".claude", "projects", "-tmp-project", "main", "subagents", "agent-a1.jsonl")
	workflowPath := filepath.Join(home, ".claude", "projects", "-tmp-project", "main", "subagents", "workflows", "wf-1", "journal.jsonl")
	activePath := filepath.Join(home, ".codex", "sessions", "2026", "07", "11", "rollout-active.jsonl")
	archivedPath := filepath.Join(home, ".codex", "archived_sessions", "rollout-archived.jsonl")

	writeJSONL(t, mainPath,
		claudeUser("claude-current", "claude-session", now.Add(-time.Hour), "current prompt"),
	)
	writeJSONL(t, metadataPath,
		claudeUser("claude-metadata", "claude-old-session", now.Add(-40*24*time.Hour), "metadata prompt"),
	)
	writeJSONL(t, expiredPath,
		claudeUser("claude-expired", "claude-expired-session", now.Add(-200*24*time.Hour), "expired prompt"),
	)
	writeJSONL(t, subagentPath,
		map[string]any{"type": "user", "uuid": "subagent-user", "promptId": "subagent-turn", "agentId": "a1", "sessionId": "claude-session", "timestamp": now.Add(-50 * time.Minute).Format(time.RFC3339Nano), "message": map[string]any{"role": "user", "content": "subagent prompt"}},
	)
	writeJSONL(t, workflowPath,
		map[string]any{"type": "started", "key": "workflow-start", "agentId": "a2"},
		map[string]any{"type": "result", "key": "workflow-result", "agentId": "a2", "result": map[string]any{"summary": "done"}},
	)
	if err := os.Chtimes(workflowPath, now, now); err != nil {
		t.Fatal(err)
	}
	writeJSONL(t, activePath,
		map[string]any{"timestamp": now.Add(-45 * time.Minute).Format(time.RFC3339Nano), "type": "session_meta", "payload": map[string]any{"id": "codex-active", "cwd": "/tmp/active"}},
		map[string]any{"timestamp": now.Add(-44 * time.Minute).Format(time.RFC3339Nano), "type": "event_msg", "payload": map[string]any{"type": "task_started", "turn_id": "codex-turn-1"}},
		map[string]any{"timestamp": now.Add(-43 * time.Minute).Format(time.RFC3339Nano), "type": "response_item", "payload": map[string]any{"type": "function_call", "id": "item-call", "call_id": "call-1", "name": "shell", "arguments": "{\"cmd\":\"pwd\"}"}},
		map[string]any{"timestamp": now.Add(-42 * time.Minute).Format(time.RFC3339Nano), "type": "response_item", "payload": map[string]any{"type": "function_call_output", "id": "item-output", "call_id": "call-1", "output": "/tmp/active"}},
		map[string]any{"timestamp": now.Add(-41 * time.Minute).Format(time.RFC3339Nano), "type": "event_msg", "payload": map[string]any{"type": "token_count", "info": map[string]any{"total_token_usage": codexTokens(100, 20, 30, 5, 120)}}},
	)
	writeJSONL(t, archivedPath,
		map[string]any{"timestamp": now.Add(-2 * time.Hour).Format(time.RFC3339Nano), "type": "session_meta", "payload": map[string]any{"id": "codex-archived"}},
		map[string]any{"timestamp": now.Add(-119 * time.Minute).Format(time.RFC3339Nano), "type": "event_msg", "payload": map[string]any{"type": "task_started", "turn_id": "archived-turn"}},
	)

	tailer := NewTranscriptTailer(slog.Default(), st, bus, TranscriptTailerOptions{Home: home, Now: func() time.Time { return now }})
	result, err := tailer.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.FilesDiscovered != 7 || result.FilesRead != 7 {
		t.Fatalf("unexpected discovery result: %+v", result)
	}
	if result.OldLinesSkipped != 1 {
		t.Fatalf("old lines skipped = %d, want 1", result.OldLinesSkipped)
	}
	events := captured.snapshot()
	if len(events) == 0 {
		t.Fatal("expected transcript events")
	}
	classes := map[string]bool{}
	var current, metadata *core.ObservationEnvelope
	for i := range events {
		event := &events[i]
		if event.Source != "transcript" || event.Quality != core.ObservationQualityPartial || !contains(event.Provenance, "backfill") {
			t.Fatalf("bad transcript provenance: %+v", event)
		}
		classes[fmt.Sprint(event.Attributes["transcript_class"])] = true
		if event.Attributes["native_event_id"] == "claude-current" {
			current = event
		}
		if event.Attributes["native_event_id"] == "claude-metadata" {
			metadata = event
		}
		if event.Attributes["native_event_id"] == "claude-expired" {
			t.Fatal("expired event was published")
		}
	}
	for _, class := range []string{"main", "subagent", "workflow", "active", "archived"} {
		if !classes[class] {
			t.Fatalf("missing transcript class %q in %v", class, classes)
		}
	}
	if current == nil || current.Content == nil {
		t.Fatal("current content was not retained")
	}
	if metadata == nil || metadata.Content != nil || metadata.Attributes["content_retention"] != "metadata_only" {
		t.Fatalf("40-day event should be metadata-only: %+v", metadata)
	}

	captured.reset()
	second, err := tailer.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second.EventsPublished != 0 || len(captured.snapshot()) != 0 {
		t.Fatalf("unchanged scan replayed events: result=%+v events=%d", second, len(captured.snapshot()))
	}

	appendJSONL(t, mainPath, map[string]any{
		"type": "assistant", "uuid": "claude-answer", "requestId": "claude-request", "sessionId": "claude-session",
		"timestamp": now.Add(-30 * time.Minute).Format(time.RFC3339Nano),
		"message":   map[string]any{"role": "assistant", "id": "claude-message", "model": "claude-sonnet", "stop_reason": "end_turn", "content": []any{map[string]any{"type": "text", "text": "answer"}}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 2}},
	})
	captured.reset()
	third, err := tailer.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if third.EventsPublished != 1 || len(captured.snapshot()) != 1 || captured.snapshot()[0].Kind != "model.request" {
		t.Fatalf("append was not incremental: result=%+v events=%+v", third, captured.snapshot())
	}
	info, err := os.Stat(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := st.GetObservationIngestCursor(ctx, "transcript:claude", mainPath)
	if err != nil || cursor == nil || cursor.ByteOffset != info.Size() || cursor.MessageID != "claude-answer" {
		t.Fatalf("cursor = %+v, err=%v, size=%d", cursor, err, info.Size())
	}

	// Replacing/truncating a file resets the byte offset. Because the previous
	// message ID is absent, the new file is consumed from byte zero.
	writeJSONL(t, mainPath, claudeUser("after-truncate", "claude-session-2", now.Add(-10*time.Minute), "new prompt"))
	captured.reset()
	truncated, err := tailer.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if truncated.EventsPublished != 1 || len(captured.snapshot()) != 1 || captured.snapshot()[0].Attributes["native_event_id"] != "after-truncate" {
		t.Fatalf("truncate recovery failed: result=%+v events=%+v", truncated, captured.snapshot())
	}

	// A replacement file may retain the last processed message. The tailer
	// locates that ID in the rotated file and resumes after it, avoiding replay.
	writeJSONL(t, mainPath,
		map[string]any{"type": "queue-operation", "operation": "enqueue", "timestamp": now.Add(-11 * time.Minute).Format(time.RFC3339Nano)},
		claudeUser("after-truncate", "claude-session-2", now.Add(-10*time.Minute), "new prompt"),
		claudeUser("after-rotation", "claude-session-2", now.Add(-5*time.Minute), "rotated prompt"),
	)
	captured.reset()
	rotated, err := tailer.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.EventsPublished != 1 || len(captured.snapshot()) != 1 || captured.snapshot()[0].Attributes["native_event_id"] != "after-rotation" {
		t.Fatalf("rotation message-id resume failed: result=%+v events=%+v", rotated, captured.snapshot())
	}
}

func TestTranscriptTailerPartialLineAndCodexUsageDelta(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "agentnexus.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bus := core.NewObservationBus()
	captured := &capturedObservations{}
	bus.Subscribe("capture", captured.observe)
	bus.Subscribe("store", st.RecordObservation)
	path := filepath.Join(home, ".codex", "sessions", "2026", "07", "11", "rollout-delta.jsonl")
	writeJSONL(t, path,
		map[string]any{"timestamp": now.Add(-time.Hour).Format(time.RFC3339Nano), "type": "session_meta", "payload": map[string]any{"id": "delta-session"}},
		map[string]any{"timestamp": now.Add(-59 * time.Minute).Format(time.RFC3339Nano), "type": "event_msg", "payload": map[string]any{"type": "task_started", "turn_id": "delta-turn"}},
		map[string]any{"timestamp": now.Add(-58 * time.Minute).Format(time.RFC3339Nano), "type": "event_msg", "payload": map[string]any{"type": "token_count", "info": map[string]any{"total_token_usage": codexTokens(100, 10, 20, 2, 110)}}},
	)
	tailer := NewTranscriptTailer(slog.Default(), st, bus, TranscriptTailerOptions{Home: home, Now: func() time.Time { return now }})
	if _, err := tailer.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	usageEvents := filterUsage(captured.snapshot())
	if len(usageEvents) != 1 || usageEvents[0].Usage.InputTokens != 80 || usageEvents[0].Usage.CacheReadTokens != 20 || usageEvents[0].Usage.TotalTokens != 110 {
		t.Fatalf("first usage = %+v", usageEvents)
	}
	if usageEvents[0].Lifecycle != core.ObservationLifecycleEnd || usageEvents[0].Status != core.ObservationStatusOK {
		t.Fatalf("request delta must close its synthetic span: %+v", usageEvents[0])
	}
	materialized, err := st.QueryObservationUsage(ctx, time.Time{})
	if err != nil || len(materialized) != 1 || materialized[0].InputTokens != 80 {
		t.Fatalf("materialized transcript usage = %+v, err=%v", materialized, err)
	}

	partial := map[string]any{"timestamp": now.Add(-57 * time.Minute).Format(time.RFC3339Nano), "type": "event_msg", "payload": map[string]any{"type": "token_count", "info": map[string]any{"total_token_usage": codexTokens(160, 20, 40, 4, 180)}}}
	encoded, err := json.Marshal(partial)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(encoded); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	captured.reset()
	before, err := st.GetObservationIngestCursor(ctx, "transcript:codex", path)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := tailer.Scan(ctx); err != nil || result.EventsPublished != 0 {
		t.Fatalf("partial scan result=%+v err=%v", result, err)
	}
	afterPartial, err := st.GetObservationIngestCursor(ctx, "transcript:codex", path)
	if err != nil || afterPartial.ByteOffset != before.ByteOffset {
		t.Fatalf("partial line advanced cursor: before=%+v after=%+v err=%v", before, afterPartial, err)
	}
	file, err = os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if result, err := tailer.Scan(ctx); err != nil || result.EventsPublished != 1 {
		t.Fatalf("completed partial scan result=%+v err=%v", result, err)
	}
	usageEvents = filterUsage(captured.snapshot())
	if len(usageEvents) != 1 || usageEvents[0].Usage.InputTokens != 40 || usageEvents[0].Usage.OutputTokens != 10 || usageEvents[0].Usage.CacheReadTokens != 20 || usageEvents[0].Usage.TotalTokens != 70 {
		t.Fatalf("delta usage = %+v", usageEvents)
	}
	if usageEvents[0].Model == nil || usageEvents[0].Model.RequestID == "delta-turn" {
		t.Fatalf("token delta did not get a request-level identity: %+v", usageEvents[0].Model)
	}
	materialized, err = st.QueryObservationUsage(ctx, time.Time{})
	if err != nil || len(materialized) != 2 {
		t.Fatalf("materialized transcript deltas = %+v, err=%v", materialized, err)
	}
	captured.reset()
	if result, err := tailer.Scan(ctx); err != nil || result.EventsPublished != 0 || len(captured.snapshot()) != 0 {
		t.Fatalf("completed line replayed: result=%+v err=%v events=%+v", result, err, captured.snapshot())
	}
}

func TestTranscriptTailerStoresOnlyPublicReasoningSummary(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "agentnexus.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bus := core.NewObservationBus()
	captured := &capturedObservations{}
	bus.Subscribe("capture", captured.observe)
	writeJSONL(t, filepath.Join(home, ".codex", "sessions", "2026", "07", "11", "rollout-reasoning.jsonl"),
		map[string]any{"timestamp": now.Add(-time.Hour).Format(time.RFC3339Nano), "type": "session_meta", "payload": map[string]any{"id": "reasoning-session"}},
		map[string]any{"timestamp": now.Add(-59 * time.Minute).Format(time.RFC3339Nano), "type": "event_msg", "payload": map[string]any{"type": "task_started", "turn_id": "reasoning-turn"}},
		map[string]any{"timestamp": now.Add(-58 * time.Minute).Format(time.RFC3339Nano), "type": "response_item", "payload": map[string]any{"type": "reasoning", "id": "reasoning-item", "summary": []any{map[string]any{"type": "summary_text", "text": "public summary"}}, "encrypted_content": "hidden-cot-secret"}},
	)
	writeJSONL(t, filepath.Join(home, ".claude", "projects", "-tmp", "reasoning.jsonl"),
		claudeUser("claude-reasoning-user", "claude-reasoning", now.Add(-time.Hour), "prompt"),
		map[string]any{"type": "assistant", "uuid": "claude-reasoning-answer", "requestId": "reasoning-request", "sessionId": "claude-reasoning", "timestamp": now.Add(-50 * time.Minute).Format(time.RFC3339Nano), "message": map[string]any{"role": "assistant", "id": "reasoning-message", "model": "claude-sonnet", "stop_reason": "end_turn", "content": []any{map[string]any{"type": "thinking", "thinking": "claude-hidden-cot"}, map[string]any{"type": "text", "text": "public answer"}}}},
	)
	tailer := NewTranscriptTailer(slog.Default(), st, bus, TranscriptTailerOptions{Home: home, Now: func() time.Time { return now }})
	if _, err := tailer.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	var allContent strings.Builder
	for _, event := range captured.snapshot() {
		if event.Content != nil {
			allContent.Write(event.Content.Data)
		}
	}
	content := allContent.String()
	if strings.Contains(content, "hidden-cot-secret") || strings.Contains(content, "claude-hidden-cot") {
		t.Fatalf("hidden reasoning leaked into observation content: %s", content)
	}
	if !strings.Contains(content, "public summary") || !strings.Contains(content, "public answer") {
		t.Fatalf("public summaries were not retained: %s", content)
	}
}

func TestTranscriptTailerSkipsUnchangedFiles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "agentnexus.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bus := core.NewObservationBus()
	bus.Subscribe("noop", func(context.Context, core.ObservationEnvelope) error { return nil })

	mainPath := filepath.Join(home, ".claude", "projects", "-tmp", "main.jsonl")
	writeJSONL(t, mainPath, claudeUser("u1", "session", now.Add(-time.Hour), "prompt"))

	tailer := NewTranscriptTailer(slog.Default(), st, bus, TranscriptTailerOptions{Home: home, Now: func() time.Time { return now }})
	first, err := tailer.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.FilesDiscovered != 1 || first.FilesRead != 1 {
		t.Fatalf("first scan should read the file: %+v", first)
	}

	// Nothing changed on disk: the file must be discovered but not re-opened.
	second, err := tailer.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second.FilesDiscovered != 1 || second.FilesRead != 0 {
		t.Fatalf("unchanged file should be skipped, not re-read: %+v", second)
	}

	// A new append bumps size/mtime, so the file is scanned again.
	appendJSONL(t, mainPath, map[string]any{
		"type": "assistant", "uuid": "a1", "requestId": "r1", "sessionId": "session",
		"timestamp": now.Add(-30 * time.Minute).Format(time.RFC3339Nano),
		"message":   map[string]any{"role": "assistant", "id": "m1", "model": "claude-sonnet", "stop_reason": "end_turn", "content": []any{map[string]any{"type": "text", "text": "answer"}}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 2}},
	})
	third, err := tailer.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if third.FilesRead != 1 {
		t.Fatalf("appended file should be re-read: %+v", third)
	}
}

func TestTranscriptTailerDurableCursorSkipsReplayAfterRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "agentnexus.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bus := core.NewObservationBus()
	captured := &capturedObservations{}
	bus.Subscribe("capture", captured.observe)

	mainPath := filepath.Join(home, ".claude", "projects", "-tmp", "main.jsonl")
	writeJSONL(t, mainPath, map[string]any{
		"type": "assistant", "uuid": "a1", "requestId": "r1", "sessionId": "s",
		"timestamp": now.Add(-time.Hour).Format(time.RFC3339Nano),
		"message":   map[string]any{"role": "assistant", "id": "m1", "model": "claude-sonnet", "stop_reason": "end_turn", "content": []any{map[string]any{"type": "text", "text": "answer"}}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 2}},
	})

	first := NewTranscriptTailer(slog.Default(), st, bus, TranscriptTailerOptions{Home: home, Now: func() time.Time { return now }})
	if r, err := first.Scan(ctx); err != nil || r.EventsPublished == 0 {
		t.Fatalf("initial scan should publish: result=%+v err=%v", r, err)
	}

	// Simulate a process restart: a brand-new tailer has an empty in-memory
	// fingerprint, so only the durable cursor can prevent a full replay.
	captured.reset()
	restarted := NewTranscriptTailer(slog.Default(), st, bus, TranscriptTailerOptions{Home: home, Now: func() time.Time { return now }})
	second, err := restarted.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second.EventsPublished != 0 || len(captured.snapshot()) != 0 {
		t.Fatalf("fully-consumed file must not replay after restart: result=%+v events=%d", second, len(captured.snapshot()))
	}
	if second.LinesRead != 0 {
		t.Fatalf("fully-consumed file must not be re-read line by line: %+v", second)
	}
}

func TestTranscriptTailerCheckpointsLargeFileAfterPartialFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "agentnexus.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	path := filepath.Join(home, ".claude", "projects", "-tmp", "large.jsonl")
	lines := make([]map[string]any, 300)
	for index := range lines {
		lines[index] = claudeUser(fmt.Sprintf("checkpoint-%03d", index), "checkpoint-session", now.Add(-time.Minute), "prompt")
	}
	writeJSONL(t, path, lines...)

	failedBus := core.NewObservationBus()
	published := 0
	failedBus.Subscribe("fail-after-checkpoint", func(context.Context, core.ObservationEnvelope) error {
		published++
		if published == 280 {
			return fmt.Errorf("injected observation failure")
		}
		return nil
	})
	first := NewTranscriptTailer(slog.Default(), st, failedBus, TranscriptTailerOptions{Home: home, Now: func() time.Time { return now }})
	if _, err := first.Scan(ctx); err == nil {
		t.Fatal("expected injected scan failure")
	}
	cursor, err := st.GetObservationIngestCursor(ctx, "transcript:claude", path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if cursor == nil || cursor.ByteOffset <= 0 || cursor.ByteOffset >= info.Size() {
		t.Fatalf("partial scan did not persist a bounded checkpoint: cursor=%+v size=%d", cursor, info.Size())
	}

	recoveredBus := core.NewObservationBus()
	recoveredBus.Subscribe("noop", func(context.Context, core.ObservationEnvelope) error { return nil })
	restarted := NewTranscriptTailer(slog.Default(), st, recoveredBus, TranscriptTailerOptions{Home: home, Now: func() time.Time { return now }})
	recovered, err := restarted.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.LinesRead <= 0 || recovered.LinesRead > transcriptCursorCheckpointLines {
		t.Fatalf("restart replayed more than one checkpoint window: %+v", recovered)
	}
	if recovered.EventsPublished != recovered.LinesRead {
		t.Fatalf("recovered events/lines mismatch: %+v", recovered)
	}
}

func claudeUser(id, session string, timestamp time.Time, content string) map[string]any {
	return map[string]any{
		"type": "user", "uuid": id, "promptId": id + "-turn", "sessionId": session,
		"timestamp": timestamp.Format(time.RFC3339Nano), "cwd": "/tmp/project",
		"message": map[string]any{"role": "user", "content": content},
	}
}

func codexTokens(input, output, cached, reasoning, total int64) map[string]any {
	return map[string]any{"input_tokens": input, "output_tokens": output, "cached_input_tokens": cached, "reasoning_output_tokens": reasoning, "total_tokens": total}
}

func writeJSONL(t *testing.T, path string, lines ...map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	var builder strings.Builder
	for _, line := range lines {
		encoded, err := json.Marshal(line)
		if err != nil {
			t.Fatal(err)
		}
		builder.Write(encoded)
		builder.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(builder.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendJSONL(t *testing.T, path string, line map[string]any) {
	t.Helper()
	encoded, err := json.Marshal(line)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		t.Fatal(err)
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func filterUsage(events []core.ObservationEnvelope) []core.ObservationEnvelope {
	var result []core.ObservationEnvelope
	for _, event := range events {
		if event.Usage != nil {
			result = append(result, event)
		}
	}
	return result
}
