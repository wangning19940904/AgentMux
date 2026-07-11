package claudecode

import (
	"reflect"
	"testing"

	"github.com/agentnexus/agentnexus/core"
)

func TestSessionArgsIncludeModel(t *testing.T) {
	s, err := newSessionResume(&Agent{
		systemPrompt:    "be terse",
		defaultModel:    "sonnet",
		supportedModels: []string{"sonnet", "opus"},
	}, "/tmp/work", "native-1")
	if err != nil {
		t.Fatal(err)
	}
	got := s.args("hello")
	want := []string{
		"--print", "--output-format", "stream-json", "--verbose", "--include-partial-messages",
		"--append-system-prompt", "be terse",
		"--model", "sonnet",
		"--resume", "native-1",
		"hello",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
	if err := s.SetModel("opus"); err != nil {
		t.Fatal(err)
	}
	got = s.args("again")
	if got[len(got)-5] != "--model" || got[len(got)-4] != "opus" {
		t.Fatalf("switched args = %#v", got)
	}
}

func TestSessionArgsIncludeSelectedEffort(t *testing.T) {
	s, err := newSessionResume(&Agent{
		defaultModel: "sonnet", supportedModels: []string{"sonnet"},
		defaultReasoningEffort: "high", supportedReasoningEfforts: []string{"low", "high", "max"},
	}, "/tmp/work", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetRuntimeSetting(core.RuntimeSettingReasoningEffort, "max"); err != nil {
		t.Fatal(err)
	}
	got := s.args("hello")
	for i := range got {
		if got[i] == "--effort" && i+1 < len(got) && got[i+1] == "max" {
			return
		}
	}
	t.Fatalf("args missing selected effort: %#v", got)
}

func TestStreamMapperEmitsToolUse(t *testing.T) {
	m := &streamMapper{}
	// An assistant message that both invokes a tool and carries no text should
	// yield exactly one EventToolUse (no blank output event).
	line := `{"type":"assistant","message":{"role":"assistant","content":[` +
		`{"type":"tool_use","name":"Bash","input":{"command":"lark-cli im send --card joke"}}]}}`
	evs := m.map_([]byte(line))
	if len(evs) != 1 {
		t.Fatalf("want 1 event, got %d: %#v", len(evs), evs)
	}
	if evs[0].Type != core.EventToolUse || evs[0].ToolName != "Bash" {
		t.Fatalf("unexpected event: %#v", evs[0])
	}
	if evs[0].ToolInput != "lark-cli im send --card joke" {
		t.Fatalf("tool input = %q", evs[0].ToolInput)
	}
}

func TestStreamMapperToolResultAndText(t *testing.T) {
	m := &streamMapper{}
	// tool_result on a user message becomes a result-only EventToolUse.
	res := m.map_([]byte(`{"type":"user","message":{"role":"user","content":[` +
		`{"type":"tool_result","content":"card sent ok","is_error":false}]}}`))
	if len(res) != 1 || res[0].Type != core.EventToolUse || res[0].ToolName != "" {
		t.Fatalf("tool_result mapping = %#v", res)
	}
	if res[0].ToolResult != "card sent ok" {
		t.Fatalf("tool result = %q", res[0].ToolResult)
	}
	// A plain assistant text message still yields an output event.
	out := m.map_([]byte(`{"type":"assistant","message":{"role":"assistant","content":[` +
		`{"type":"text","text":"done"}]}}`))
	if len(out) != 1 || out[0].Type != core.EventOutput || out[0].Text != "done" {
		t.Fatalf("assistant text mapping = %#v", out)
	}
}
