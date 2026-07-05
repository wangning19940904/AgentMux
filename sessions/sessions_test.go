package sessions

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestClaudeScannerParsesSessions(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	dir := filepath.Join(root, "projects", "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "s1.jsonl")
	data := `{"type":"system","subtype":"custom-title","sessionId":"s1","cwd":"/work/proj","text":"Build auth flow","timestamp":"2026-07-03T01:00:00Z"}
{"type":"user","sessionId":"s1","cwd":"/work/proj","message":{"role":"user","content":"hello codex"},"timestamp":"2026-07-03T01:01:00Z"}
{"type":"assistant","sessionId":"s1","message":{"role":"assistant","content":[{"type":"text","text":"done"}]},"timestamp":"2026-07-03T01:02:00Z"}
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := New().List(context.Background(), "claudecode", "cli")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("sessions = %d", len(items))
	}
	if items[0].Title != "Build auth flow" || items[0].ProjectDir != "/work/proj" || items[0].MessageCount != 2 {
		t.Fatalf("meta = %+v", items[0])
	}
	messages, err := New().Messages(context.Background(), ResumeRequest{ProviderID: "claudecode", Surface: "cli", SourcePath: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Content != "hello codex" || messages[1].Content != "done" {
		t.Fatalf("messages = %+v", messages)
	}
}

func TestCodexScannerParsesAndSkipsSubagents(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".codex")
	t.Setenv("CODEX_HOME", root)
	dir := filepath.Join(root, "sessions", "2026", "07", "03")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(dir, "rollout-cx1.jsonl")
	goodData := `{"type":"session_meta","payload":{"id":"cx1","cwd":"/repo","timestamp":"2026-07-03T02:00:00Z"}}
{"type":"response_item","payload":{"item":{"type":"message","role":"user","content":[{"type":"input_text","text":"ship it"}]}},"timestamp":"2026-07-03T02:01:00Z"}
{"type":"response_item","payload":{"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"shipped"}]}},"timestamp":"2026-07-03T02:02:00Z"}
`
	if err := os.WriteFile(good, []byte(goodData), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rollout-sub.jsonl"), []byte(`{"source":"subagent","type":"session_meta","payload":{"id":"sub"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := New().List(context.Background(), "codex", "cli")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("sessions = %d: %+v", len(items), items)
	}
	if items[0].SessionID != "cx1" || items[0].ResumeCommand != "codex resume 'cx1'" || items[0].MessageCount != 2 {
		t.Fatalf("meta = %+v", items[0])
	}
}

func TestDeleteRejectsOutsideSessionRoots(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".codex")
	t.Setenv("CODEX_HOME", root)
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.WriteFile(outside, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := New().Delete(context.Background(), ResumeRequest{ProviderID: "codex", Surface: "cli", SourcePath: outside})
	if err == nil {
		t.Fatal("expected delete outside roots to fail")
	}
}

func TestCodexAppServerMockList(t *testing.T) {
	dir := t.TempDir()
	cmd := filepath.Join(dir, "codex-mock")
	body1 := `{"jsonrpc":"2.0","id":1,"result":{}}`
	body2 := `{"jsonrpc":"2.0","id":2,"result":{"data":[{"id":"thread-1","name":"Desktop run","cwd":"/repo","updatedAt":1783047600,"message_count":7}]}}`
	script := "#!/bin/sh\n" +
		fmt.Sprintf("printf %%s\\\\n %q\n", body1) +
		fmt.Sprintf("printf %%s\\\\n %q\n", body2) +
		"sleep 1\n"
	if runtime.GOOS == "windows" {
		t.Skip("shell mock is unix-only")
	}
	if err := os.WriteFile(cmd, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	client := &CodexAppClient{Command: cmd}
	items, err := client.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].SessionID != "thread-1" || items[0].Surface != "app-server" {
		t.Fatalf("items = %+v", items)
	}
}

func TestCodexAppServerMockMessages(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell mock is unix-only")
	}
	dir := t.TempDir()
	cmd := filepath.Join(dir, "codex-mock")
	body1 := `{"jsonrpc":"2.0","id":1,"result":{}}`
	body2 := `{"jsonrpc":"2.0","id":2,"result":{"thread":{"id":"thread-1","turns":[{"role":"user","content":"hello"},{"role":"assistant","content":"world"}]}}}`
	script := "#!/bin/sh\n" +
		fmt.Sprintf("printf %%s\\\\n %q\n", body1) +
		fmt.Sprintf("printf %%s\\\\n %q\n", body2) +
		"sleep 1\n"
	if err := os.WriteFile(cmd, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	client := &CodexAppClient{Command: cmd}
	messages, err := client.Messages(context.Background(), "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Content != "hello" || messages[1].Role != "assistant" {
		t.Fatalf("messages = %+v", messages)
	}
}
