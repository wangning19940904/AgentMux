package parser

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCodexClientClassification(t *testing.T) {
	for _, tc := range []struct{ name, originator, source, want string }{
		{"desktop-vscode", "Codex Desktop", `"vscode"`, "codex-app"},
		{"desktop-exec", "Codex Desktop", `"exec"`, "codex-app"},
		{"desktop-subagent", "Codex Desktop", `{"subagent":{"thread_spawn":{"parent_thread_id":"parent"}}}`, "codex-app"},
		{"cli", "codex-tui", `"cli"`, "codex"},
		{"cli-legacy", "codex_cli_rs", `"cli"`, "codex"},
		{"exec", "codex_exec", `"exec"`, "codex"},
		{"source-only-cli", "", `"cli"`, "codex"},
		{"exec-not-proof-of-cli", "", `"exec"`, "codex-unknown"},
		{"ide", "codex_vscode", `"vscode"`, "codex-vscode"},
		{"unknown-embedding", "AgentNexus", `"vscode"`, "codex-unknown"},
		{"vscode-not-proof-of-app", "", `"vscode"`, "codex-unknown"},
		{"missing", "", `null`, "codex-unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			meta, err := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{
				"id": "session", "originator": tc.originator, "source": json.RawMessage(tc.source),
			}})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "sessions", "rollout-test.jsonl")
			writeClientFixture(t, path, string(meta)+"\n"+
				`{"type":"token_count","timestamp":"2026-01-02T00:00:00Z","token_count":{"input_tokens":100,"output_tokens":10,"cached_input_tokens":30}}`+"\n")
			recs, err := (&codexCollector{root: root}).Collect(context.Background(), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
			if err != nil {
				t.Fatal(err)
			}
			if len(recs) != 1 || recs[0].Source != "codex" || recs[0].RuntimeID != tc.want || recs[0].InputTokens != 70 || recs[0].CacheReadTokens != 30 || recs[0].OutputTokens != 10 {
				t.Fatalf("records = %+v", recs)
			}
			runtimes, err := SessionRuntimes(context.Background(), "codex", root)
			if err != nil || runtimes["session"] != tc.want || runtimes["rollout-test"] != tc.want {
				t.Fatalf("historical metadata = %v, err=%v", runtimes, err)
			}
		})
	}
}

func TestClaudeClientClassificationRetainsMetadataBeforeSince(t *testing.T) {
	for _, tc := range []struct{ entrypoint, want string }{
		{"claude-desktop", "claude-desktop"}, {"cli", "claudecode"}, {"", "claude-unknown"}, {"future-client", "claude-unknown"},
	} {
		t.Run(tc.want+tc.entrypoint, func(t *testing.T) {
			root := t.TempDir()
			meta, _ := json.Marshal(map[string]string{"type": "user", "timestamp": "2026-01-01T00:00:00Z", "sessionId": "s", "entrypoint": tc.entrypoint})
			writeClientFixture(t, filepath.Join(root, "projects", "p", "s.jsonl"), string(meta)+"\n"+
				`{"type":"assistant","timestamp":"2026-01-02T00:00:00Z","sessionId":"s","message":{"model":"opus","usage":{"input_tokens":100,"output_tokens":10}}}`+"\n")
			recs, err := (&claudeCollector{root: root}).Collect(context.Background(), time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
			if err != nil || len(recs) != 1 || recs[0].RuntimeID != tc.want || recs[0].Source != "claude" || recs[0].InputTokens != 100 {
				t.Fatalf("records = %+v, err=%v", recs, err)
			}
		})
	}
}

func TestSessionRuntimesIncludesArchivesAndDoesNotGuessMixedClaudeSessions(t *testing.T) {
	root := t.TempDir()
	writeClientFixture(t, filepath.Join(root, "archived_sessions", "rollout-old.jsonl"),
		`{"type":"session_meta","payload":{"id":"archived","originator":"Codex Desktop","source":"vscode"}}`+"\nnot a token record\n")
	writeClientFixture(t, filepath.Join(root, "projects", "p", "s.jsonl"),
		`{"sessionId":"mixed","entrypoint":"claude-desktop"}`+"\n"+`{"sessionId":"mixed","entrypoint":"cli"}`+"\n")
	runtimes, err := SessionRuntimes(context.Background(), "codex", root)
	if err != nil || runtimes["archived"] != "codex-app" {
		t.Fatalf("archives = %v, err=%v", runtimes, err)
	}
	runtimes, err = SessionRuntimes(context.Background(), "claude", root)
	if err != nil || runtimes["mixed"] != "claude-unknown" {
		t.Fatalf("mixed = %v, err=%v", runtimes, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := SessionRuntimes(ctx, "claude", root); err != context.Canceled {
		t.Fatalf("canceled scan = %v", err)
	}
}

func writeClientFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
