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

	collector, err := NewCollector("codex", root, nil)
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

	collector, _ := NewCollector("codex", root, nil)
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

	collector, _ := NewCollector("codex", root, nil)
	records, err := collector.Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].InputTokens != 8 || records[0].OutputTokens != 3 || records[0].CacheReadTokens != 4 {
		t.Fatalf("legacy records = %#v", records)
	}
}
