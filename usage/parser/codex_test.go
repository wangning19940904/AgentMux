package parser

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCodexCollectorParsesNestedCumulativeUsageAndDeduplicates(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions", "2026", "07", "11")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []byte(
		`{"timestamp":"2026-07-11T06:00:00Z","type":"session_meta","payload":{"id":"session-1","cwd":"/tmp/project-a"}}` + "\n" +
			`{"timestamp":"2026-07-11T06:00:01Z","type":"turn_context","payload":{"model":"gpt-5.5","cwd":"/tmp/project-a"}}` + "\n" +
			`{"timestamp":"2026-07-11T06:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20,"reasoning_output_tokens":5,"total_tokens":120},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20,"reasoning_output_tokens":5,"total_tokens":120}}}}` + "\n" +
			// Exact cumulative duplicate: no second UsageRecord.
			`{"timestamp":"2026-07-11T06:00:03Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20,"reasoning_output_tokens":5,"total_tokens":120},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20,"reasoning_output_tokens":5,"total_tokens":120}}}}` + "\n" +
			`{"timestamp":"2026-07-11T06:00:04Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":160,"cached_input_tokens":70,"output_tokens":35,"reasoning_output_tokens":8,"total_tokens":195},"last_token_usage":{"input_tokens":60,"cached_input_tokens":30,"output_tokens":15,"reasoning_output_tokens":3,"total_tokens":75}}}}` + "\n")
	path := filepath.Join(sessions, "rollout-test.jsonl")
	if err := os.WriteFile(path, lines, 0o644); err != nil {
		t.Fatal(err)
	}

	collector, err := NewCollector("codex", root)
	if err != nil {
		t.Fatal(err)
	}
	records, err := collector.Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %#v, want 2", records)
	}
	first, second := records[0], records[1]
	if first.SessionID != "session-1" || first.Project != "/tmp/project-a" || first.Model != "gpt-5.5" {
		t.Fatalf("correlation = %#v", first)
	}
	if first.InputTokens != 60 || first.CacheReadTokens != 40 || first.OutputTokens != 20 {
		t.Fatalf("first usage = %#v", first)
	}
	if got := first.InputTokens + first.CacheReadTokens + first.OutputTokens; got != 120 {
		t.Fatalf("first normalized total = %d, want 120", got)
	}
	if second.InputTokens != 30 || second.CacheReadTokens != 30 || second.OutputTokens != 15 {
		t.Fatalf("delta usage = %#v", second)
	}
	if got := second.InputTokens + second.CacheReadTokens + second.OutputTokens; got != 75 {
		t.Fatalf("second normalized total = %d, want 75", got)
	}

	since, _ := time.Parse(time.RFC3339, "2026-07-11T06:00:04Z")
	recent, err := collector.Collect(context.Background(), since)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 || recent[0].InputTokens != 30 || recent[0].CacheReadTokens != 30 || recent[0].OutputTokens != 15 {
		t.Fatalf("since must retain cumulative baseline before filtering: %#v", recent)
	}
}

func TestCodexCollectorUsesLastUsageWhenCumulativeCounterResets(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []byte(
		`{"timestamp":"2026-07-11T06:00:00Z","type":"turn_context","payload":{"model":"gpt-5.5"}}` + "\n" +
			`{"timestamp":"2026-07-11T06:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":500,"output_tokens":50,"total_tokens":550},"last_token_usage":{"input_tokens":500,"output_tokens":50,"total_tokens":550}}}}` + "\n" +
			`{"timestamp":"2026-07-11T06:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":40,"output_tokens":10,"total_tokens":50},"last_token_usage":{"input_tokens":40,"output_tokens":10,"total_tokens":50}}}}` + "\n")
	if err := os.WriteFile(filepath.Join(sessions, "rollout-reset.jsonl"), lines, 0o644); err != nil {
		t.Fatal(err)
	}

	collector, _ := NewCollector("codex", root)
	records, err := collector.Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[1].InputTokens != 40 || records[1].OutputTokens != 10 {
		t.Fatalf("reset records = %#v", records)
	}
}

func TestCodexCollectorKeepsLegacyTopLevelTokenCount(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"token_count","timestamp":"2026-01-01T10:00:00Z","model":"gpt-5","token_count":{"input_tokens":12,"output_tokens":3,"cached_input_tokens":4}}`
	if err := os.WriteFile(filepath.Join(sessions, "rollout-legacy.jsonl"), []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	collector, _ := NewCollector("codex", root)
	records, err := collector.Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].InputTokens != 8 || records[0].OutputTokens != 3 || records[0].CacheReadTokens != 4 {
		t.Fatalf("legacy records = %#v", records)
	}
}

func TestCodexCollectorSkipsFilesOlderThanSince(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions", "2026", "06", "01")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"token_count","timestamp":"2026-06-01T10:00:00Z","model":"gpt-5","token_count":{"input_tokens":12,"output_tokens":3,"cached_input_tokens":4}}`
	path := filepath.Join(sessions, "rollout-old.jsonl")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Stamp the file well before the incremental window.
	old := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	collector, _ := NewCollector("codex", root)

	// A since after the file's mtime must skip it entirely (no records).
	since := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	skipped, err := collector.Collect(context.Background(), since)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 0 {
		t.Fatalf("file older than since must be skipped: %#v", skipped)
	}

	// A zero since (full scan) still reads it.
	full, err := collector.Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != 1 {
		t.Fatalf("full scan must read the file: %#v", full)
	}
}

func writeCodexRollout(t *testing.T, events ...string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"timestamp":"2026-11-18T07:00:00Z","type":"session_meta","payload":{"id":"thread-1","cwd":"/tmp/p"}}` + "\n"
	for _, event := range events {
		content += event + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "rollout-test.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func codexTokenCount(at, total, last string) string {
	return `{"timestamp":"` + at + `","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":` + total + `,"last_token_usage":` + last + `}}}`
}

// A file that continues an earlier thread opens with the thread's running
// total. Only its last request is new.
func TestCodexCollectorDoesNotRecountAContinuedThread(t *testing.T) {
	root := writeCodexRollout(t,
		codexTokenCount("2026-11-18T07:10:08Z",
			`{"input_tokens":41000000,"cached_input_tokens":40000000,"output_tokens":70000,"total_tokens":41070000}`,
			`{"input_tokens":300000,"cached_input_tokens":290000,"output_tokens":500,"total_tokens":300500}`),
		codexTokenCount("2026-11-18T07:11:00Z",
			`{"input_tokens":41200000,"cached_input_tokens":40150000,"output_tokens":70300,"total_tokens":41270300}`,
			`{"input_tokens":200000,"cached_input_tokens":150000,"output_tokens":300,"total_tokens":200300}`))
	collector, _ := NewCollector("codex", root)
	records, err := collector.Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, record := range records {
		total += record.InputTokens + record.CacheReadTokens + record.OutputTokens
	}
	if len(records) != 2 || total != 300500+200300 {
		t.Fatalf("records = %d, tokens = %d; the earlier thread's %d tokens must not be counted again", len(records), total, 41070000-300500)
	}
}

// A marker event carries a total but no token counts. It is not a request
// and must not become the baseline the next request is measured against.
func TestCodexCollectorIgnoresTotalOnlyMarkerEvents(t *testing.T) {
	zero := `{"input_tokens":0,"cached_input_tokens":0,"output_tokens":0,"total_tokens":0}`
	root := writeCodexRollout(t,
		codexTokenCount("2026-11-13T07:20:00Z",
			`{"input_tokens":1000,"cached_input_tokens":400,"output_tokens":50,"total_tokens":1050}`,
			`{"input_tokens":1000,"cached_input_tokens":400,"output_tokens":50,"total_tokens":1050}`),
		codexTokenCount("2026-11-13T07:20:43Z",
			`{"input_tokens":0,"cached_input_tokens":0,"output_tokens":0,"total_tokens":950000}`, zero),
		// No last_token_usage here: only the delta can tell what is new.
		codexTokenCount("2026-11-13T07:21:00Z",
			`{"input_tokens":1600,"cached_input_tokens":900,"output_tokens":80,"total_tokens":1680}`, zero))
	collector, _ := NewCollector("codex", root)
	records, err := collector.Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %+v, want the two requests and no row for the marker", records)
	}
	second := records[1]
	if got := second.InputTokens + second.CacheReadTokens + second.OutputTokens; got != 630 {
		t.Fatalf("request after the marker = %d tokens, want the 630 it added, not the whole total", got)
	}
}
