package sessions

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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

func TestClaudeScannerPairsToolInputAndOutput(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	dir := filepath.Join(root, "projects", "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "tools.jsonl")
	data := `{"type":"assistant","sessionId":"tools","message":{"role":"assistant","content":[{"type":"text","text":"I will inspect it."},{"type":"tool_use","id":"call-1","name":"read_file","input":{"path":"README.md"}}]},"timestamp":"2026-07-03T01:01:00Z"}
{"type":"user","sessionId":"tools","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"file contents"}]},"timestamp":"2026-07-03T01:02:00Z"}
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	messages, err := New().Messages(context.Background(), ResumeRequest{ProviderID: "claudecode", Surface: "cli", SourcePath: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %+v", messages)
	}
	tool := messages[1]
	if tool.Role != "tool" || tool.ToolName != "read_file" || tool.ToolCallID != "call-1" || tool.ToolOutput != "file contents" {
		t.Fatalf("tool message = %+v", tool)
	}
	if tool.ToolInput != "{\n  \"path\": \"README.md\"\n}" {
		t.Fatalf("tool input = %q", tool.ToolInput)
	}
}

func TestClaudeScannerUsesBoundedSummaryForLongTranscripts(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	dir := filepath.Join(root, "projects", "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "long.jsonl")
	var data strings.Builder
	data.WriteString("{\"type\":\"user\",\"sessionId\":\"long\",\"message\":{\"role\":\"user\",\"content\":\"first prompt\"}}\n")
	for i := 0; i < 300; i++ {
		data.WriteString("{\"type\":\"assistant\",\"sessionId\":\"long\",\"message\":{\"role\":\"assistant\",\"content\":\"answer\"}}\n")
	}
	if err := os.WriteFile(path, []byte(data.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := New().List(context.Background(), "claudecode", "cli")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !items[0].MessagesPartial || items[0].MessageCount != 220 {
		t.Fatalf("summary meta = %+v", items)
	}
	messages, err := New().Messages(context.Background(), ResumeRequest{ProviderID: "claudecode", Surface: "cli", SourcePath: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 301 {
		t.Fatalf("full messages = %d, want 301", len(messages))
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

func TestCodexScannerPairsFunctionCallInputAndOutput(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".codex")
	t.Setenv("CODEX_HOME", root)
	dir := filepath.Join(root, "sessions", "2026", "07", "03")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-tools.jsonl")
	data := `{"type":"session_meta","payload":{"id":"tools","cwd":"/repo","timestamp":"2026-07-03T02:00:00Z"}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"check status"}]},"timestamp":"2026-07-03T02:01:00Z"}
{"type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"git status\"}","call_id":"call-1"},"timestamp":"2026-07-03T02:02:00Z"}
{"type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":"clean"},"timestamp":"2026-07-03T02:03:00Z"}
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	messages, err := New().Messages(context.Background(), ResumeRequest{ProviderID: "codex", Surface: "cli", SourcePath: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %+v", messages)
	}
	tool := messages[1]
	if tool.Role != "tool" || tool.ToolName != "exec_command" || tool.ToolCallID != "call-1" || tool.ToolOutput != "clean" {
		t.Fatalf("tool message = %+v", tool)
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

func TestServiceListCoalescesConcurrentDiscovery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell mock is unix-only")
	}
	dir := t.TempDir()
	cmd := filepath.Join(dir, "codex-mock")
	starts := filepath.Join(dir, "starts")
	body1 := `{"jsonrpc":"2.0","id":1,"result":{}}`
	body2 := `{"jsonrpc":"2.0","id":2,"result":{"data":[{"id":"thread-1"}]}}`
	script := "#!/bin/sh\n" +
		fmt.Sprintf("printf x >> %q\n", starts) +
		"sleep 0.2\n" +
		fmt.Sprintf("printf %%s\\\\n %q\n", body1) +
		fmt.Sprintf("printf %%s\\\\n %q\n", body2) +
		"sleep 1\n"
	if err := os.WriteFile(cmd, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	service := &Service{app: &CodexAppClient{Command: cmd}}
	var wait sync.WaitGroup
	wait.Add(2)
	errors := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wait.Done()
			_, err := service.List(context.Background(), "codex", "app-server")
			errors <- err
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	started, err := os.ReadFile(starts)
	if err != nil {
		t.Fatal(err)
	}
	if string(started) != "x" {
		t.Fatalf("app-server starts = %q, want one", started)
	}
}

func TestCodexDesktopListFiltersOriginatorAndSourceKind(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell mock is unix-only")
	}
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	sessionsDir := filepath.Join(codexHome, "sessions", "2026", "08", "20")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMeta := func(id, originator string) string {
		t.Helper()
		body := fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"cwd":"/repo","source":"vscode","originator":%q}}`, id, originator)
		path := filepath.Join(sessionsDir, "rollout-"+id+".jsonl")
		if err := os.WriteFile(path, []byte(body+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	desktopPath := writeMeta("desktop-thread", "Codex Desktop")
	writeMeta("vscode-thread", "Codex VS Code Extension")

	dir := t.TempDir()
	cmd := filepath.Join(dir, "codex-mock")
	capture := filepath.Join(dir, "request.json")
	body1 := `{"jsonrpc":"2.0","id":1,"result":{}}`
	body2 := fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"result":{"data":[{"id":"desktop-thread","name":"Desktop run","cwd":"/repo","path":%q},{"id":"vscode-thread","name":"VS Code run","cwd":"/repo"}]}}`, desktopPath)
	script := "#!/bin/sh\n" +
		"IFS= read -r initialize\n" +
		fmt.Sprintf("printf %%s\\\\n %q\n", body1) +
		"IFS= read -r initialized\n" +
		"IFS= read -r request\n" +
		fmt.Sprintf("printf '%%s\\n' \"$request\" > %q\n", capture) +
		fmt.Sprintf("printf %%s\\\\n %q\n", body2) +
		"sleep 1\n"
	if err := os.WriteFile(cmd, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	service := &Service{app: &CodexAppClient{Command: cmd}}
	items, err := service.List(context.Background(), "codex", "desktop")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].SessionID != "desktop-thread" || items[0].Surface != "desktop" || items[0].Originator != "Codex Desktop" {
		t.Fatalf("items = %+v", items)
	}
	request, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(request), `"sourceKinds":["vscode"]`) {
		t.Fatalf("thread/list request = %s", request)
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

func TestCodexAppServerMessagesParsesThreadItems(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell mock is unix-only")
	}
	dir := t.TempDir()
	cmd := filepath.Join(dir, "codex-mock")
	body1 := `{"jsonrpc":"2.0","id":1,"result":{}}`
	body2 := `{"jsonrpc":"2.0","id":2,"result":{"thread":{"id":"thread-1","turns":[{"createdAt":1783047600,"items":[{"id":"u1","type":"userMessage","content":[{"type":"text","text":"hello"}]},{"id":"tool-1","type":"commandExecution","command":"pwd","cwd":"/repo","aggregatedOutput":"/repo","exitCode":0,"status":"completed","commandActions":[]},{"id":"a1","type":"agentMessage","text":"done"}]}]}}}`
	script := "#!/bin/sh\n" +
		fmt.Sprintf("printf %%s\\\\n %q\n", body1) +
		fmt.Sprintf("printf %%s\\\\n %q\n", body2) +
		"sleep 1\n"
	if err := os.WriteFile(cmd, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	messages, err := (&CodexAppClient{Command: cmd}).Messages(context.Background(), "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 || messages[0].Role != "user" || messages[2].Role != "assistant" {
		t.Fatalf("messages = %+v", messages)
	}
	tool := messages[1]
	if tool.Role != "tool" || tool.ToolName != "exec_command" || tool.ToolOutput != "/repo\n\nexit code: 0" || tool.ToolStatus != "completed" {
		t.Fatalf("tool message = %+v", tool)
	}
}

func TestAppServerThreadStatusIsSafeForUnownedThreads(t *testing.T) {
	tests := []struct {
		status any
		want   string
	}{
		{status: map[string]any{"type": "notLoaded"}, want: "idle"},
		{status: map[string]any{"type": "active"}, want: "running"},
		{status: map[string]any{"type": "waiting_input"}, want: "waiting_input"},
		{status: map[string]any{"type": "failed"}, want: "failed"},
	}
	for _, test := range tests {
		if got := appServerThreadStatus(map[string]any{"status": test.status}); got != test.want {
			t.Fatalf("status %#v = %q, want %q", test.status, got, test.want)
		}
	}
}
