package cliagents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// fakeDSH replays frames in the shape of DSH's json-stream.ts and records its
// argv, so this exercises the real subprocess path of the adapter end to end.
const fakeDSH = `#!/bin/sh
printf '%s\n' "$*" | tr '\n' ' ' >> "$FAKE_DSH_LOG"
echo >> "$FAKE_DSH_LOG"
session="session-new"
while [ $# -gt 0 ]; do
  [ "$1" = "--session-id" ] && session="$2"
  shift
done
printf '{"type":"session","sessionId":"%s","cwd":"/repo"}\n' "$session"
printf '{"type":"status","phase":"turn_start","turn":1}\n'
printf '{"type":"status","phase":"step_start","turn":1,"step":1}\n'
printf '{"type":"thinking","text":"private reasoning"}\n'
printf '{"type":"tool_call","callId":"c1","tool":"bash","input":{"command":"ls"}}\n'
printf '{"type":"tool_result","callId":"c1","status":"completed","result":"a.txt"}\n'
printf '{"type":"text","text":"partial"}\n'
printf '{"type":"status","phase":"step_end","turn":1,"step":1,"usage":{"inputTokens":10,"outputTokens":5}}\n'
printf '{"type":"status","phase":"turn_end","turn":1,"reason":{"kind":"completed"}}\n'
if [ -n "$FAKE_DSH_FAIL" ]; then
  printf '{"type":"final","sessionId":"%s","reason":{"kind":"error","error":{"code":"quota","message":"out of quota"}},"text":""}\n' "$session"
  echo "dsh: quota: out of quota" >&2
  exit 1
fi
printf '{"type":"final","sessionId":"%s","reason":{"kind":"completed"},"text":"the whole answer"}\n' "$session"
`

func startFakeDSH(t *testing.T, cfg map[string]any) (core.AgentSession, string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "dsh"), []byte(fakeDSH), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(bin, "argv.log")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DSH_LOG", log)
	if cfg == nil {
		cfg = map[string]any{}
	}
	agent, err := core.CreateAgent("dsh", cfg)
	if err != nil {
		t.Fatal(err)
	}
	session, err := agent.StartSession(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return session, log
}

func collect(t *testing.T, session core.AgentSession, prompt string) []*core.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	events, err := session.Send(ctx, prompt)
	if err != nil {
		t.Fatal(err)
	}
	var out []*core.Event
	for event := range events {
		out = append(out, event)
	}
	return out
}

func TestDSHProcessRunsAndResumesItsSession(t *testing.T) {
	session, log := startFakeDSH(t, map[string]any{"system_prompt": "be brief"})
	events := collect(t, session, "first task")
	var final string
	var tools, usage int
	for _, event := range events {
		switch event.Type {
		case core.EventFinal:
			final = event.Text
		case core.EventToolUse:
			tools++
		case core.EventModelResponse:
			if event.Usage != nil && event.Usage.TotalTokens == 15 {
				usage++
			}
		case core.EventThinking:
			t.Fatalf("raw reasoning must not surface: %+v", event)
		case core.EventError:
			t.Fatalf("unexpected error: %v", event.Err)
		}
	}
	if final != "the whole answer" || tools != 2 || usage != 1 {
		t.Fatalf("final=%q tools=%d usage=%d", final, tools, usage)
	}
	collect(t, session, "second task")

	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		t.Fatalf("argv log = %q", data)
	}
	first, second := lines[0], lines[len(lines)-1]
	for _, want := range []string{"--profile headless", "--json", "--permission-mode workspace-write", "--approval-policy never", "first task"} {
		if !strings.Contains(first, want) {
			t.Errorf("first turn lacks %q: %s", want, first)
		}
	}
	if !strings.Contains(first, "be brief") {
		t.Errorf("first turn must carry the system prompt: %s", first)
	}
	if strings.Contains(first, "--session-id") {
		t.Errorf("first turn must start a new session: %s", first)
	}
	if !strings.Contains(second, "--session-id session-new") || strings.Contains(second, "be brief") {
		t.Errorf("second turn must resume without repeating the system prompt: %s", second)
	}
}

func TestDSHProcessReportsTurnFailureOnce(t *testing.T) {
	session, _ := startFakeDSH(t, nil)
	t.Setenv("FAKE_DSH_FAIL", "1")
	var failures []string
	for _, event := range collect(t, session, "task") {
		if event.Type == core.EventFinal {
			t.Fatalf("a failed turn must not look final: %+v", event)
		}
		if event.Type == core.EventError {
			failures = append(failures, event.Err.Error())
		}
	}
	if len(failures) != 1 || failures[0] != "out of quota" {
		t.Fatalf("failures = %v", failures)
	}
}
