package cliagents

import (
	"slices"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

func TestDSHArgsMapApprovalModesToPresets(t *testing.T) {
	for mode, preset := range map[string]string{
		core.ApprovalModePlan:     "read-only",
		core.ApprovalModeAutoEdit: "workspace-write",
		core.ApprovalModeYolo:     "danger-full-access",
		"":                        "workspace-write",
	} {
		args := dshArgs("-leading dash", "", "", mode)
		index := slices.Index(args, "--permission-mode")
		if index < 0 || args[index+1] != preset {
			t.Fatalf("mode %q: args=%v", mode, args)
		}
		if !slices.Contains(args, "never") {
			t.Fatalf("mode %q must never wait for an approval: %v", mode, args)
		}
		if args[len(args)-2] != "--" || args[len(args)-1] != "-leading dash" {
			t.Fatalf("prompt must not parse as a flag: %v", args)
		}
	}
}

func TestDSHPrependsSystemPromptOnlyOnFirstTurn(t *testing.T) {
	first := dshArgs("task", "be brief", "", core.ApprovalModePlan)
	if got := first[len(first)-1]; got != "be brief\n\ntask" {
		t.Fatalf("first turn prompt = %q", got)
	}
	resumed := dshResumeArgs("session-1", "next", "be brief", "", core.ApprovalModePlan)
	if got := resumed[len(resumed)-1]; got != "next" {
		t.Fatalf("resumed prompt = %q", got)
	}
	if index := slices.Index(resumed, "--session-id"); index < 0 || resumed[index+1] != "session-1" {
		t.Fatalf("resume args = %v", resumed)
	}
}

func TestDSHSessionID(t *testing.T) {
	if got := dshSessionID([]byte(`{"type":"session","sessionId":"session-1","cwd":"/repo"}`)); got != "session-1" {
		t.Fatalf("session id = %q", got)
	}
	if got := dshSessionID([]byte(`{"type":"final","sessionId":"session-1"}`)); got != "" {
		t.Fatalf("only the opening frame names the session, got %q", got)
	}
}

func TestDSHStreamEvents(t *testing.T) {
	cases := []struct {
		name, line string
		eventType  core.EventType
		check      func(*core.Event) bool
	}{
		{"text", `{"type":"text","text":"hi"}`, core.EventOutput, func(e *core.Event) bool { return e.Text == "hi" }},
		{"tool call", `{"type":"tool_call","callId":"c1","tool":"bash","input":{"cmd":"ls"}}`, core.EventToolUse, func(e *core.Event) bool {
			return e.ToolName == "bash" && e.ToolCallID == "c1" && e.ToolInput == `{"cmd":"ls"}`
		}},
		{"tool error", `{"type":"tool_result","callId":"c1","status":"error","result":"denied"}`, core.EventToolUse, func(e *core.Event) bool {
			return e.Err != nil && e.ToolResult == "denied"
		}},
		{"usage", `{"type":"status","phase":"step_end","usage":{"inputTokens":3,"outputTokens":4}}`, core.EventModelResponse, func(e *core.Event) bool {
			return e.Usage != nil && e.Usage.TotalTokens == 7
		}},
		{"completed", `{"type":"final","sessionId":"s","reason":{"kind":"completed"},"text":"done"}`, core.EventFinal, func(e *core.Event) bool {
			return e.Final && e.Text == "done"
		}},
		{"turn error", `{"type":"final","reason":{"kind":"error","error":{"code":"x","message":"boom"}},"text":""}`, core.EventError, func(e *core.Event) bool {
			return e.Err != nil && e.Err.Error() == "boom"
		}},
		{"no terminal turn", `{"type":"final","reason":null,"text":"partial"}`, core.EventError, func(e *core.Event) bool { return e.Err != nil }},
		{"cancelled", `{"type":"final","reason":{"kind":"cancelled"},"text":"partial"}`, core.EventError, func(e *core.Event) bool { return e.Err != nil }},
		{"startup error", `{"type":"error","message":"no key"}`, core.EventError, func(e *core.Event) bool { return e.Err.Error() == "no key" }},
	}
	for _, tc := range cases {
		events := dshStreamEvents([]byte(tc.line))
		if len(events) != 1 || events[0].Type != tc.eventType || !tc.check(events[0]) {
			t.Fatalf("%s: events=%+v", tc.name, events)
		}
	}
	for _, line := range []string{`{"type":"thinking","text":"private"}`, `{"type":"status","phase":"turn_start"}`, `not json`} {
		if events := dshStreamEvents([]byte(line)); len(events) != 0 {
			t.Fatalf("%s must be skipped: %+v", line, events)
		}
	}
}
