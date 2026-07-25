package claudecode

import (
	"reflect"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

func TestObservationTelemetryIsPrivatePerChildAndContentGated(t *testing.T) {
	env := withObservationTelemetry([]string{
		"PATH=/bin", "OTEL_EXPORTER_OTLP_ENDPOINT=https://third-party.example", "OTEL_LOG_USER_PROMPTS=1",
	}, core.ObservationChildTelemetry{
		Endpoint: "http://127.0.0.1:8765/api/v1/observability/otlp", Token: "local-token",
		TraceID: "trace-1", ParentSpanID: "span-1", TurnID: "turn-1", SessionID: "session-1", AgentID: "agent-1",
	})
	values := map[string]string{}
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	if values["OTEL_EXPORTER_OTLP_ENDPOINT"] != "http://127.0.0.1:8765/api/v1/observability/otlp" ||
		values["OTEL_EXPORTER_OTLP_PROTOCOL"] != "http/json" || values["OTEL_TRACES_EXPORTER"] != "otlp" {
		t.Fatalf("private OTLP environment = %+v", values)
	}
	if values["OTEL_LOG_USER_PROMPTS"] != "0" || values["OTEL_LOG_TOOL_CONTENT"] != "0" || values["OTEL_LOG_RAW_API_BODIES"] != "0" {
		t.Fatalf("content gates = %+v", values)
	}
	if !strings.Contains(values["OTEL_RESOURCE_ATTRIBUTES"], "agentmux.parent_trace_id=trace-1") ||
		values["OTEL_EXPORTER_OTLP_HEADERS"] != "Authorization=Bearer local-token" {
		t.Fatalf("correlation/auth environment = %+v", values)
	}
}

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
		`{"type":"tool_use","id":"tool-1","name":"Bash","input":{"command":"lark-cli im send --card joke"}}]}}`
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
	if evs[0].ToolCallID != "tool-1" || evs[0].ToolInputRaw != `{"command":"lark-cli im send --card joke"}` {
		t.Fatalf("tool correlation/raw input = %#v", evs[0])
	}
}

func TestStreamMapperToolResultAndText(t *testing.T) {
	m := &streamMapper{}
	// tool_result on a user message becomes a result-only EventToolUse.
	res := m.map_([]byte(`{"type":"user","message":{"role":"user","content":[` +
		`{"type":"tool_result","tool_use_id":"tool-1","content":"card sent ok","is_error":false}]}}`))
	if len(res) != 1 || res[0].Type != core.EventToolUse || res[0].ToolName != "" {
		t.Fatalf("tool_result mapping = %#v", res)
	}
	if res[0].ToolResult != "card sent ok" {
		t.Fatalf("tool result = %q", res[0].ToolResult)
	}
	if res[0].ToolCallID != "tool-1" || res[0].ToolResultRaw != `"card sent ok"` {
		t.Fatalf("tool correlation/raw result = %#v", res[0])
	}
	// A plain assistant text message still yields an output event.
	out := m.map_([]byte(`{"type":"assistant","message":{"role":"assistant","content":[` +
		`{"type":"text","text":"done"}]}}`))
	if len(out) != 1 || out[0].Type != core.EventOutput || out[0].Text != "done" {
		t.Fatalf("assistant text mapping = %#v", out)
	}
}

func TestStreamMapperCorrelatesParallelToolResultsOutOfOrder(t *testing.T) {
	m := &streamMapper{}
	starts := m.map_([]byte(`{"type":"assistant","message":{"role":"assistant","content":[` +
		`{"type":"tool_use","id":"call-a","name":"Read","input":{"file_path":"a.go"}},` +
		`{"type":"tool_use","id":"call-b","name":"Read","input":{"file_path":"b.go"}}]}}`))
	if len(starts) != 2 || starts[0].ToolCallID != "call-a" || starts[1].ToolCallID != "call-b" {
		t.Fatalf("parallel starts = %#v", starts)
	}

	results := m.map_([]byte(`{"type":"user","message":{"role":"user","content":[` +
		`{"type":"tool_result","tool_use_id":"call-b","content":"b result"},` +
		`{"type":"tool_result","tool_use_id":"call-a","content":"a result"}]}}`))
	if len(results) != 2 || results[0].ToolCallID != "call-b" || results[1].ToolCallID != "call-a" {
		t.Fatalf("out-of-order results lost correlation = %#v", results)
	}
}

func TestStreamMapperEmitsModelResponseUsageForToolOnlyMessage(t *testing.T) {
	m := &streamMapper{requestedModel: "sonnet"}
	events := m.map_([]byte(`{"type":"assistant","uuid":"msg-1","message":{"role":"assistant","model":"claude-sonnet-4-6","usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":40,"cache_creation_input_tokens":5},"content":[` +
		`{"type":"tool_use","id":"tool-1","name":"Bash","input":{"command":"pwd"}}]}}`))
	if len(events) != 2 || events[1].Type != core.EventModelResponse || events[1].Usage == nil {
		t.Fatalf("events = %#v", events)
	}
	if events[1].Usage.InputTokens != 100 || events[1].Usage.OutputTokens != 20 || events[1].Usage.TotalTokens != 165 ||
		events[1].Usage.RequestID != "msg-1" || events[1].Usage.RequestedModel != "sonnet" || events[1].Usage.ResolvedModel != "claude-sonnet-4-6" {
		t.Fatalf("usage = %#v", events[1].Usage)
	}
}
