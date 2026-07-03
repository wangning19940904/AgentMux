package parser

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestClaudeCollectorWithRoot verifies the Claude parser over a synthetic
// transcript tree. The same code path is used for SSH-synced data (the SSH
// collector simply points root at a local staging dir), so this also exercises
// the SSH ingestion path's parsing stage.
func TestClaudeCollectorWithRoot(t *testing.T) {
	root := t.TempDir()
	projDir := filepath.Join(root, "projects", "my-proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"assistant","timestamp":"2026-01-01T10:00:00Z","sessionId":"abc","message":{"model":"claude-opus-4-8","usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":10,"cache_creation_input_tokens":5}}}`
	if err := os.WriteFile(filepath.Join(projDir, "s.jsonl"), []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	col, err := NewCollector("claude", root, nil)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := col.Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("records = %d, want 1", len(recs))
	}
	r := recs[0]
	if r.Model != "claude-opus-4-8" || r.InputTokens != 100 || r.OutputTokens != 50 ||
		r.CacheReadTokens != 10 || r.CacheWriteTokens != 5 {
		t.Fatalf("record mismatch: %+v", r)
	}
	if r.Project != "my-proj" || r.SessionID != "abc" {
		t.Fatalf("project/session mismatch: %+v", r)
	}
}

func TestCollectorSinceFilter(t *testing.T) {
	root := t.TempDir()
	projDir := filepath.Join(root, "projects", "p")
	_ = os.MkdirAll(projDir, 0o755)
	old := `{"type":"assistant","timestamp":"2025-01-01T10:00:00Z","sessionId":"a","message":{"model":"opus","usage":{"input_tokens":1}}}`
	_ = os.WriteFile(filepath.Join(projDir, "s.jsonl"), []byte(old+"\n"), 0o644)

	col, _ := NewCollector("claude", root, nil)
	recs, _ := col.Collect(context.Background(), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if len(recs) != 0 {
		t.Fatalf("since filter: got %d, want 0", len(recs))
	}
}
