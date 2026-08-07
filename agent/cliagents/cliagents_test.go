package cliagents

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

func TestCodexAppServerUsesPrivateJSONOTLPOverrides(t *testing.T) {
	ctx := core.WithObservationChildTelemetry(context.Background(), core.ObservationChildTelemetry{
		Endpoint: "http://127.0.0.1:8765/api/v1/observability/otlp", Token: "local-token", CaptureContent: true,
	})
	args := codexAppServerArgs(ctx)
	joined := strings.Join(args, " ")
	for _, expected := range []string{
		`otel.trace_exporter={"otlp-http"={endpoint="http://127.0.0.1:8765/api/v1/observability/otlp/v1/traces",protocol="json"`,
		`Authorization="Bearer local-token"`, `otel.log_user_prompt=true`, `otel.exporter="none"`,
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("args missing %q: %v", expected, args)
		}
	}
	if len(args) < 3 || args[len(args)-3] != "app-server" || args[len(args)-1] != "stdio://" {
		t.Fatalf("app-server args = %v", args)
	}
}

func TestUnavailableCodexThreadError(t *testing.T) {
	missing := fmt.Errorf(`codex app-server RPC error: {"code":-32600,"message":"no rollout found for thread id 019f4c48"}`)
	if !errors.Is(unavailableCodexThreadError(missing), core.ErrNativeSessionUnavailable) {
		t.Fatal("missing Codex rollout must be marked as an unavailable native session")
	}

	other := errors.New("codex app-server RPC error: unauthorized")
	if got := unavailableCodexThreadError(other); got != other {
		t.Fatalf("other Codex errors must be returned unchanged: %v", got)
	}
}

func TestModelArgsForVerifiedCLIs(t *testing.T) {
	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{
			name: "cursor",
			got:  cursorArgs("hello", "", "sonnet-4", core.ApprovalModeManual),
			want: []string{"agent", "--print", "--output-format", "stream-json", "--trust", "--model", "sonnet-4", "hello"},
		},
	}
	for _, tt := range tests {
		if !reflect.DeepEqual(tt.got, tt.want) {
			t.Fatalf("%s args = %#v, want %#v", tt.name, tt.got, tt.want)
		}
	}
}

func TestCursorStreamEventsMapNestedAssistantText(t *testing.T) {
	events := cursorStreamEvents([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello "},{"type":"text","text":"world"}]}}`))
	if len(events) != 1 || events[0].Type != core.EventOutput || events[0].Text != "hello world" {
		t.Fatalf("assistant events = %#v", events)
	}
	if events[0].Metadata["transport"] != "stream-json" || events[0].Metadata["coverage"] != "structured" {
		t.Fatalf("assistant metadata = %#v", events[0].Metadata)
	}
}

func TestCursorStreamEventsMapToolStartAndCompletion(t *testing.T) {
	started := cursorStreamEvents([]byte(`{
		"type":"tool_call","subtype":"started","call_id":"call-1",
		"tool_call":{"shellToolCall":{"args":{"command":"printf cursor-probe","timeout":30000},"description":"print probe"},"toolCallId":"call-1","startedAtMs":"1000"}
	}`))
	if len(started) != 1 || started[0].Type != core.EventToolUse || started[0].ToolCallID != "call-1" ||
		started[0].ToolName != "执行命令" || started[0].ToolInput != "printf cursor-probe" || started[0].ToolInputRaw == "" {
		t.Fatalf("tool start = %#v", started)
	}

	completed := cursorStreamEvents([]byte(`{
		"type":"tool_call","subtype":"completed","call_id":"call-1",
		"tool_call":{"shellToolCall":{"result":{"success":{"exitCode":0,"stdout":"cursor-probe","stderr":"","interleavedOutput":"cursor-probe"}}},"toolCallId":"call-1","startedAtMs":"1000","completedAtMs":"1325"}
	}`))
	if len(completed) != 1 || completed[0].Type != core.EventToolUse || completed[0].ToolCallID != "call-1" ||
		completed[0].ToolName != "" || completed[0].ToolResult != "exit 0 · cursor-probe" || completed[0].DurationMs != 325 || completed[0].Err != nil {
		t.Fatalf("tool completion = %#v", completed)
	}
}

func TestCursorStreamEventsExposeAuthorizationOutput(t *testing.T) {
	events := cursorStreamEvents([]byte(`{
		"type":"tool_call","subtype":"completed","call_id":"call-auth",
		"tool_call":{"shellToolCall":{"result":{"success":{"exitCode":0,"stderr":"To login, open https://login.example/device?code=ABCD-EFGH\nVerification code: ABCD-EFGH\nWaiting for authorization..."}}},"toolCallId":"call-auth"}
	}`))
	if len(events) != 2 || events[0].Type != core.EventToolUse || events[1].Type != core.EventOutput {
		t.Fatalf("authorization events = %#v", events)
	}
	if !strings.Contains(events[1].Text, "https://login.example/device?code=ABCD-EFGH") || !strings.Contains(events[1].Text, "ABCD-EFGH") {
		t.Fatalf("authorization output = %q", events[1].Text)
	}
	if strings.Contains(events[1].Text, "Waiting for authorization") {
		t.Fatalf("authorization output should contain only actionable details: %q", events[1].Text)
	}
}

func TestCursorStreamEventsMapFinalUsageAndSafeProgress(t *testing.T) {
	result := cursorStreamEvents([]byte(`{
		"type":"result","subtype":"success","is_error":false,"duration_ms":900,"result":"done","request_id":"request-1",
		"usage":{"inputTokens":10,"outputTokens":5,"cacheReadTokens":3,"cacheWriteTokens":2}
	}`))
	if len(result) != 1 || result[0].Type != core.EventFinal || result[0].Text != "done" || result[0].DurationMs != 900 ||
		result[0].Usage == nil || result[0].Usage.TotalTokens != 20 || result[0].Usage.RequestID != "request-1" ||
		result[0].Metadata["clear_persistent"] != "true" {
		t.Fatalf("result events = %#v", result)
	}

	reconnecting := cursorStreamEvents([]byte(`{"type":"connection","subtype":"reconnecting","attempt":2}`))
	if len(reconnecting) != 1 || reconnecting[0].Type != core.EventThinking || !strings.Contains(reconnecting[0].Text, "第 2 次重连") {
		t.Fatalf("connection events = %#v", reconnecting)
	}
	// Raw Cursor thinking deltas are private model reasoning, not a supported
	// user-facing summary, and must remain suppressed.
	if thinking := cursorStreamEvents([]byte(`{"type":"thinking","subtype":"delta","text":"private chain of thought"}`)); len(thinking) != 0 {
		t.Fatalf("raw thinking leaked: %#v", thinking)
	}
}

func TestCursorStderrMapperPreservesMultilineLoginDetails(t *testing.T) {
	mapper := newCursorStderrMapper()
	source := mapper([]byte("Source: https://skills.byted.org/sre/spacex"))
	noise := mapper([]byte("Downloading file 431 of 500 from skills.byted.org"))
	first := mapper([]byte("To login, open https://login.example/device"))
	second := mapper([]byte("Verification code: ABCD-EFGH"))
	third := mapper([]byte("Waiting for authorization..."))
	if source != nil || noise != nil {
		t.Fatalf("ordinary stderr must not become assistant output: %#v / %#v", source, noise)
	}
	if first == nil || second == nil || third == nil || third.Type != core.EventOutput {
		t.Fatalf("stderr events = %#v / %#v / %#v", first, second, third)
	}
	if third.Metadata["persistent"] != "true" {
		t.Fatalf("authorization stderr was not marked persistent: %#v", third.Metadata)
	}
	if third.Metadata["priority"] != "action_required" {
		t.Fatalf("authorization stderr was not prioritized: %#v", third.Metadata)
	}
	for _, want := range []string{"https://login.example/device", "ABCD-EFGH", "授权完成后任务会自动继续"} {
		if !strings.Contains(third.Text, want) {
			t.Fatalf("stderr output %q missing %q", third.Text, want)
		}
	}
	for _, hidden := range []string{"Downloading file", "https://skills.byted.org/sre/spacex", "Waiting for authorization"} {
		if strings.Contains(third.Text, hidden) {
			t.Fatalf("stderr output %q leaked noisy detail %q", third.Text, hidden)
		}
	}
}

func TestParseCursorModelCatalog(t *testing.T) {
	output := "\x1b[2mAvailable models\x1b[22m\r\n\r\n" +
		"\x1b[36mauto\x1b[39m \x1b[2m- Auto\x1b[22m\r\n" +
		"\x1b[36mgpt-5.6-sol\x1b[39m \x1b[2m- GPT-5.6 Sol (default)\x1b[22m\r\n" +
		"claude-opus-4-8 - Claude Opus 4.8 (current, default)\r\n\r\n" +
		"Tip: use --model <id> to switch.\r\n"
	catalog, err := parseCursorModelCatalog([]byte(output))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"auto", "gpt-5.6-sol", "claude-opus-4-8"}
	if !reflect.DeepEqual(catalog.Models, want) || catalog.DefaultModel != "claude-opus-4-8" {
		t.Fatalf("catalog = %#v, want models %#v with default claude-opus-4-8", catalog, want)
	}
}

func TestParseCursorModelCatalogRejectsUnexpectedOutput(t *testing.T) {
	if _, err := parseCursorModelCatalog([]byte("authentication required")); err == nil {
		t.Fatal("unexpected model-list output must not replace a cached/static catalog")
	}
}

func TestCursorModelForSettingsEncodesEffortAndFast(t *testing.T) {
	tests := []struct {
		name     string
		settings core.RuntimeSettings
		want     string
	}{
		{
			name:     "adds overrides",
			settings: core.RuntimeSettings{Model: "claude-opus-4-8[context=1m]", ReasoningEffort: "xhigh", ServiceTier: "priority"},
			want:     "claude-opus-4-8[context=1m,effort=xhigh,fast=true]",
		},
		{
			name:     "replaces aliases without duplicates",
			settings: core.RuntimeSettings{Model: "gpt-5.6-sol[reasoning=low,fast=false,context=200k]", ReasoningEffort: "high", ServiceTier: "fast"},
			want:     "gpt-5.6-sol[effort=high,fast=true,context=200k]",
		},
		{
			name:     "explicit normal speed",
			settings: core.RuntimeSettings{Model: "gpt-5.6-sol", ServiceTier: "default"},
			want:     "gpt-5.6-sol[fast=false]",
		},
		{
			name:     "runtime default has no model flag",
			settings: core.RuntimeSettings{ReasoningEffort: "high", ServiceTier: "priority"},
			want:     "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cursorModelForSettings(tt.settings); got != tt.want {
				t.Fatalf("model = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestApprovalModesMapToNativeCLIFlags(t *testing.T) {
	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{"cursor-yolo", cursorArgs("hello", "", "", core.ApprovalModeYolo), []string{"agent", "--print", "--output-format", "stream-json", "--trust", "--yolo", "hello"}},
		{"gemini-auto-edit", geminiArgs("hello", "", "", core.ApprovalModeAutoEdit), []string{"-p", "hello", "--output-format", "stream-json", "--approval-mode", "auto_edit"}},
		{"qoder-yolo", qoderArgs("hello", "", "", core.ApprovalModeYolo), []string{"-p", "hello", "-f", "stream-json", "--permission-mode", "bypass_permissions"}},
		{"iflow-manual", iflowArgs("hello", "", "", core.ApprovalModeManual), []string{"-i", "-r", "-o", "hello"}},
	}
	for _, tt := range tests {
		if !reflect.DeepEqual(tt.got, tt.want) {
			t.Fatalf("%s args = %#v, want %#v", tt.name, tt.got, tt.want)
		}
	}
	if got := opencodeApprovalEnv(core.ApprovalModeYolo)["OPENCODE_CONFIG_CONTENT"]; got != `{"permission":"allow"}` {
		t.Fatalf("OpenCode YOLO config = %q", got)
	}
	if got := iflowApprovalEnv(core.ApprovalModeAutoEdit)["IFLOW_approvalMode"]; got != "autoEdit" {
		t.Fatalf("iFlow auto-edit config = %q", got)
	}
}

func TestCodexAppServerMapperStreamsMessageAndToolProgress(t *testing.T) {
	m := &codexEventMapper{}

	events, done, err := m.mapNotification("item/agentMessage/delta", map[string]any{"delta": "hello"})
	if err != nil || done || len(events) != 1 || events[0].Type != core.EventOutput || events[0].Text != "hello" {
		t.Fatalf("first delta = %#v, done=%t, err=%v", events, done, err)
	}
	events, _, _ = m.mapNotification("item/agentMessage/delta", map[string]any{"delta": " world"})
	if len(events) != 1 || events[0].Text != "hello world" {
		t.Fatalf("second delta = %#v", events)
	}

	events, _, _ = m.mapNotification("item/reasoning/summaryTextDelta", map[string]any{"delta": "checking files"})
	if len(events) != 1 || events[0].Type != core.EventThinking || events[0].Text != "checking files" {
		t.Fatalf("thinking = %#v", events)
	}

	events, _, _ = m.mapNotification("item/started", map[string]any{"item": map[string]any{"type": "commandExecution", "command": "pwd"}})
	if len(events) != 1 || events[0].Type != core.EventToolUse || events[0].ToolName != "执行命令" || events[0].ToolInput != "pwd" {
		t.Fatalf("tool start = %#v", events)
	}
	events, _, _ = m.mapNotification("item/completed", map[string]any{"item": map[string]any{"type": "commandExecution", "exitCode": float64(0), "aggregatedOutput": "/tmp"}})
	if len(events) != 1 || events[0].ToolResult != "exit 0 · /tmp" {
		t.Fatalf("tool result = %#v", events)
	}

	events, done, err = m.mapNotification("turn/completed", map[string]any{"turn": map[string]any{"status": "completed"}})
	if err != nil || !done || len(events) != 1 || events[0].Type != core.EventFinal || events[0].Text != "hello world" {
		t.Fatalf("completed = %#v, done=%t, err=%v", events, done, err)
	}
}

func TestCodexAppServerMapperDoesNotExposeRawReasoning(t *testing.T) {
	m := &codexEventMapper{}
	events, done, err := m.mapNotification("item/reasoning/textDelta", map[string]any{"delta": "private reasoning"})
	if err != nil || done || len(events) != 0 {
		t.Fatalf("raw reasoning must not render: events=%#v done=%t err=%v", events, done, err)
	}
}

func TestCodexAppServerMapperDoesNotEmitThinkingPlaceholder(t *testing.T) {
	m := &codexEventMapper{}
	for _, notification := range []struct {
		method string
		params map[string]any
	}{
		{"item/started", map[string]any{"item": map[string]any{"type": "reasoning"}}},
		{"item/reasoning/summaryPartAdded", map[string]any{}},
	} {
		events, done, err := m.mapNotification(notification.method, notification.params)
		if err != nil || done || len(events) != 0 {
			t.Fatalf("%s should not render a thinking placeholder: events=%#v done=%t err=%v", notification.method, events, done, err)
		}
	}
}

func TestCodexModelsFromResultUsesNativeCatalog(t *testing.T) {
	models := codexModelsFromResult(map[string]any{"data": []any{
		map[string]any{"id": "gpt-5.6-sol", "model": "gpt-5.6-sol"},
		map[string]any{"id": "gpt-5.6-terra", "model": "gpt-5.6-terra"},
		map[string]any{"id": "duplicate", "model": "gpt-5.6-sol"},
	}})
	want := []string{"gpt-5.6-sol", "gpt-5.6-terra"}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("models = %#v, want %#v", models, want)
	}
}

func TestCodexTurnStartCarriesModelEffortAndServiceTier(t *testing.T) {
	s := &codexSession{
		defaultModel: "gpt-5", defaultReasoningEffort: "high", defaultServiceTier: "default",
		supportedModel: []string{"gpt-5"}, supportedReasoningEfforts: []string{"low", "high", "xhigh"}, supportedServiceTiers: []string{"default", "priority"},
		modelReasoningEfforts: map[string][]string{"gpt-5": {"low", "high", "xhigh"}},
	}
	if err := s.SetRuntimeSetting(core.RuntimeSettingReasoningEffort, "xhigh"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRuntimeSetting(core.RuntimeSettingServiceTier, "priority"); err != nil {
		t.Fatal(err)
	}
	params := s.turnStartParams("thread-1", "hello")
	if params["model"] != "gpt-5" || params["effort"] != "xhigh" || params["serviceTier"] != "priority" {
		t.Fatalf("turn params = %#v", params)
	}
}

func TestCodexApprovalModesMapToTurnPolicy(t *testing.T) {
	s := &codexSession{
		defaultApprovalMode:    core.ApprovalModeManual,
		supportedApprovalModes: core.ApprovalModeValuesForRuntime("codex"),
	}
	params := s.turnStartParams("thread-1", "hello")
	if params["approvalPolicy"] != "on-request" || params["sandbox"] != "readOnly" {
		t.Fatalf("manual policy = %#v", params)
	}
	if err := s.SetRuntimeSetting(core.RuntimeSettingApprovalMode, core.ApprovalModeYolo); err != nil {
		t.Fatal(err)
	}
	params = s.turnStartParams("thread-1", "hello")
	if params["approvalPolicy"] != "never" || params["sandbox"] != "dangerFullAccess" {
		t.Fatalf("YOLO policy = %#v", params)
	}
	if err := s.SetRuntimeSetting(core.RuntimeSettingApprovalMode, core.ApprovalModeAuto); err != nil {
		t.Fatal(err)
	}
	params = s.turnStartParams("thread-1", "hello")
	if params["approvalPolicy"] != "on-request" || params["sandbox"] != "workspaceWrite" || params["approvalsReviewer"] != "auto_review" {
		t.Fatalf("auto-review policy = %#v", params)
	}
}

func TestCodexCatalogExtractsNativeReasoningAndServiceCapabilities(t *testing.T) {
	result := map[string]any{"data": []any{map[string]any{
		"model":                     "gpt-5",
		"supportedReasoningEfforts": []any{map[string]any{"reasoningEffort": "low"}, map[string]any{"reasoningEffort": "high"}},
		"serviceTiers":              []any{map[string]any{"id": "default"}, map[string]any{"id": "priority"}},
	}}}
	if got := codexReasoningEffortsFromResult(result); !reflect.DeepEqual(got, []string{"low", "high"}) {
		t.Fatalf("reasoning capabilities = %#v", got)
	}
	if got := codexServiceTiersFromResult(result); !reflect.DeepEqual(got, []string{"default", "priority"}) {
		t.Fatalf("service capabilities = %#v", got)
	}
}

func TestCodexAppServerMapperCorrelatesParallelToolsOutOfOrder(t *testing.T) {
	m := &codexEventMapper{}
	start := func(id, command string, startedAt float64) *core.Event {
		events, _, err := m.mapNotification("item/started", map[string]any{
			"threadId": "thread-1", "turnId": "turn-1", "startedAtMs": startedAt,
			"item": map[string]any{"id": id, "type": "commandExecution", "command": command, "status": "inProgress"},
		})
		if err != nil || len(events) != 1 {
			t.Fatalf("start %s: events=%#v err=%v", id, events, err)
		}
		return events[0]
	}
	a := start("call-a", "cat a", 1000)
	b := start("call-b", "cat b", 1100)
	if a.ToolCallID != "call-a" || b.ToolCallID != "call-b" || a.ToolInputRaw != "cat a" || b.ToolInputRaw != "cat b" {
		t.Fatalf("starts lost ids/raw input: a=%#v b=%#v", a, b)
	}
	updates, _, _ := m.mapNotification("item/commandExecution/outputDelta", map[string]any{
		"threadId": "thread-1", "turnId": "turn-1", "itemId": "call-b", "delta": "streamed b output",
	})
	if len(updates) != 1 || updates[0].ToolCallID != "call-b" || updates[0].ToolResultRaw != "streamed b output" || updates[0].ToolResult != "" {
		t.Fatalf("tool output update = %#v", updates)
	}

	complete := func(id, output string, completedAt float64) *core.Event {
		events, _, err := m.mapNotification("item/completed", map[string]any{
			"threadId": "thread-1", "turnId": "turn-1", "completedAtMs": completedAt,
			"item": map[string]any{"id": id, "type": "commandExecution", "status": "completed", "exitCode": float64(0), "aggregatedOutput": output},
		})
		if err != nil || len(events) != 1 {
			t.Fatalf("complete %s: events=%#v err=%v", id, events, err)
		}
		return events[0]
	}
	resultB := complete("call-b", "b result", 1300)
	resultA := complete("call-a", "a result", 1600)
	if resultB.ToolCallID != "call-b" || resultA.ToolCallID != "call-a" {
		t.Fatalf("out-of-order correlation failed: b=%#v a=%#v", resultB, resultA)
	}
	if resultB.DurationMs != 200 || resultA.DurationMs != 600 {
		t.Fatalf("durations: b=%d a=%d", resultB.DurationMs, resultA.DurationMs)
	}
	if resultB.ToolName != "" || resultA.ToolName != "" {
		t.Fatal("completion events must remain result-only for legacy renderers")
	}
	if resultB.ToolResultRaw == "" || resultA.ToolResultRaw == "" {
		t.Fatal("full adapter-visible results must be retained")
	}
}

func TestCodexAppServerMapperEmitsCumulativeUsageDeltas(t *testing.T) {
	m := &codexEventMapper{requestedModel: "gpt-5.5", resolvedModel: "gpt-5.5"}
	notification := func(input, cached, output, reasoning, total float64) []*core.Event {
		events, done, err := m.mapNotification("thread/tokenUsage/updated", map[string]any{
			"threadId": "thread-1", "turnId": "turn-1",
			"tokenUsage": map[string]any{
				"total": map[string]any{"inputTokens": input, "cachedInputTokens": cached, "outputTokens": output, "reasoningOutputTokens": reasoning, "totalTokens": total},
				"last":  map[string]any{},
			},
		})
		if done || err != nil {
			t.Fatalf("usage notification: done=%t err=%v", done, err)
		}
		return events
	}

	first := notification(100, 40, 20, 5, 120)
	if len(first) != 1 || first[0].Usage == nil || first[0].Usage.InputTokens != 60 || first[0].Usage.CacheReadTokens != 40 || first[0].Usage.TotalTokens != 120 {
		t.Fatalf("first delta = %#v", first)
	}
	if got := first[0].Usage.InputTokens + first[0].Usage.CacheReadTokens + first[0].Usage.OutputTokens; got != first[0].Usage.TotalTokens {
		t.Fatalf("first normalized token sum = %d, total = %d", got, first[0].Usage.TotalTokens)
	}
	if duplicate := notification(100, 40, 20, 5, 120); len(duplicate) != 0 {
		t.Fatalf("duplicate cumulative usage must be suppressed: %#v", duplicate)
	}
	second := notification(160, 70, 35, 8, 195)
	if len(second) != 1 || second[0].Usage.InputTokens != 30 || second[0].Usage.CacheReadTokens != 30 ||
		second[0].Usage.OutputTokens != 15 || second[0].Usage.ReasoningTokens != 3 || second[0].Usage.TotalTokens != 75 {
		t.Fatalf("second delta = %#v", second)
	}
	if got := second[0].Usage.InputTokens + second[0].Usage.CacheReadTokens + second[0].Usage.OutputTokens; got != second[0].Usage.TotalTokens {
		t.Fatalf("second normalized token sum = %d, total = %d", got, second[0].Usage.TotalTokens)
	}
}

func TestCodexAppServerMapperRerouteFailureAndCompaction(t *testing.T) {
	m := &codexEventMapper{requestedModel: "gpt-5.5", resolvedModel: "gpt-5.5"}
	rerouted, _, _ := m.mapNotification("model/rerouted", map[string]any{
		"threadId": "thread-1", "turnId": "turn-1", "fromModel": "gpt-5.5", "toModel": "gpt-5.5-safe", "reason": "highRiskCyberActivity",
	})
	if len(rerouted) != 1 || rerouted[0].Usage == nil || rerouted[0].Usage.ResolvedModel != "gpt-5.5-safe" || rerouted[0].Status != "rerouted" {
		t.Fatalf("reroute = %#v", rerouted)
	}

	started, _, _ := m.mapNotification("item/started", map[string]any{
		"turnId": "turn-1", "startedAtMs": float64(1000), "item": map[string]any{"id": "compact-1", "type": "contextCompaction"},
	})
	completed, _, _ := m.mapNotification("item/completed", map[string]any{
		"turnId": "turn-1", "completedAtMs": float64(1250), "item": map[string]any{"id": "compact-1", "type": "contextCompaction"},
	})
	if len(started) != 1 || len(completed) != 1 || started[0].Type != core.EventCompaction || completed[0].DurationMs != 250 {
		t.Fatalf("compaction lifecycle: started=%#v completed=%#v", started, completed)
	}

	failed, done, err := m.mapNotification("turn/completed", map[string]any{
		"turn": map[string]any{"id": "turn-1", "status": "failed", "durationMs": float64(900), "error": map[string]any{"message": "model failed"}},
	})
	if !done || err == nil || len(failed) != 1 || failed[0].Status != "failed" || failed[0].DurationMs != 900 {
		t.Fatalf("failure = %#v done=%t err=%v", failed, done, err)
	}
}

func TestCodexAppServerMapperTracksRetryAttempts(t *testing.T) {
	m := &codexEventMapper{threadID: "thread-1", turnID: "turn-1", requestedModel: "gpt-5.5", resolvedModel: "gpt-5.5", retryAttempt: 1}
	events, done, err := m.mapNotification("error", map[string]any{
		"threadId": "thread-1", "turnId": "turn-1", "willRetry": true,
		"error": map[string]any{"message": "upstream overloaded"},
	})
	if done || err != nil || len(events) != 2 {
		t.Fatalf("retry events=%#v done=%t err=%v", events, done, err)
	}
	if events[0].Usage.Attempt != 1 || events[1].Usage.Attempt != 2 || events[0].Usage.RequestID != events[1].Usage.RequestID {
		t.Fatalf("retry attempt correlation = %#v", events)
	}

	usage, _, _ := m.mapNotification("thread/tokenUsage/updated", map[string]any{
		"threadId": "thread-1", "turnId": "turn-1",
		"tokenUsage": map[string]any{
			"total": map[string]any{"inputTokens": float64(20), "cachedInputTokens": float64(5), "outputTokens": float64(4), "totalTokens": float64(24)},
			"last":  map[string]any{"inputTokens": float64(20), "cachedInputTokens": float64(5), "outputTokens": float64(4), "totalTokens": float64(24)},
		},
	})
	if len(usage) != 1 || usage[0].Usage.Attempt != 2 || usage[0].Usage.RequestID != events[0].Usage.RequestID {
		t.Fatalf("retried usage = %#v", usage)
	}
}

func TestCodexAppServerMapperUsesLastUsageAsResumeBaseline(t *testing.T) {
	m := &codexEventMapper{turnID: "turn-1", requestedModel: "gpt-5.5", resolvedModel: "gpt-5.5"}
	events, _, _ := m.mapNotification("thread/tokenUsage/updated", map[string]any{
		"turnId": "turn-1",
		"tokenUsage": map[string]any{
			"total": map[string]any{"inputTokens": float64(1000), "cachedInputTokens": float64(500), "outputTokens": float64(100), "totalTokens": float64(1100)},
			"last":  map[string]any{"inputTokens": float64(20), "cachedInputTokens": float64(5), "outputTokens": float64(4), "totalTokens": float64(24)},
		},
	})
	if len(events) != 1 || events[0].Usage == nil || events[0].Usage.InputTokens != 15 ||
		events[0].Usage.CacheReadTokens != 5 || events[0].Usage.OutputTokens != 4 || events[0].Usage.TotalTokens != 24 {
		t.Fatalf("resumed usage must exclude historic cumulative totals: %#v", events)
	}
}

func TestCodexInteractionMappingAndHighRiskPolicy(t *testing.T) {
	session := &codexSession{pendingInteractions: map[string]codexPendingInteraction{}}
	interaction, ok := session.captureServerInteraction(9, "item/commandExecution/requestApproval", map[string]any{
		"params": map[string]any{
			"threadId": "thread-1", "turnId": "turn-1", "itemId": "item-1",
			"command": "git push origin main", "cwd": "/work/project",
		},
	})
	if !ok || interaction.Kind != core.AgentInteractionCommandApproval || !interaction.HighRisk {
		t.Fatalf("interaction = %+v, ok=%t", interaction, ok)
	}
	pending := session.pendingInteractions[interaction.ID]
	if _, err := codexInteractionResult(pending.method, pending.params, core.AgentInteractionResponse{Decision: "acceptForSession"}); err == nil {
		t.Fatal("high-risk command accepted for session")
	}
	result, err := codexInteractionResult(pending.method, pending.params, core.AgentInteractionResponse{Decision: "accept"})
	if err != nil || result.(map[string]any)["decision"] != "accept" {
		t.Fatalf("allow once result=%+v err=%v", result, err)
	}
	if !codexHighRiskInteraction(&core.AgentInteraction{
		Kind: core.AgentInteractionFileChangeApproval,
		RawParams: map[string]any{
			"changes": []any{map[string]any{"type": "delete", "path": "important.txt"}},
		},
	}) {
		t.Fatal("file deletion was not classified as high risk")
	}
}

func TestCodexWorkDirMatchingIsCanonicalAndRejectsMissingMetadata(t *testing.T) {
	root := t.TempDir()
	if !sameCodexWorkDir(root, root+"/.") {
		t.Fatal("canonical equivalent work directories did not match")
	}
	if sameCodexWorkDir("", root) || sameCodexWorkDir(root, t.TempDir()) {
		t.Fatal("missing or different work directory matched")
	}
}

func TestCodexRequestUserInputResponsePreservesQuestionCorrelation(t *testing.T) {
	params := map[string]any{"questions": []any{
		map[string]any{"id": "choice", "header": "Mode", "question": "Which?", "options": []any{
			map[string]any{"label": "Safe", "description": "Use safe mode"},
		}},
		map[string]any{"id": "secret", "header": "Token", "question": "Enter token", "isSecret": true},
	}}
	result, err := codexInteractionResult("item/tool/requestUserInput", params, core.AgentInteractionResponse{
		Answers: map[string][]string{"choice": {"Safe"}, "secret": {"local-value"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	answers := result.(map[string]any)["answers"].(map[string]any)
	if len(answers) != 2 {
		t.Fatalf("answers = %+v", answers)
	}
}

func TestSharedCodexClientRoutesByThread(t *testing.T) {
	client := &codexAppClient{
		sessions: map[string]*codexSession{}, pending: map[int]chan codexRPCResponse{},
		done: make(chan struct{}),
	}
	session := &codexSession{inbox: make(chan map[string]any, 1)}
	client.sessions["thread-1"] = session
	client.routeServerMessage(map[string]any{
		"jsonrpc": "2.0", "method": "turn/started",
		"params": map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": "turn-1"}},
	})
	select {
	case message := <-session.inbox:
		if message["method"] != "turn/started" {
			t.Fatalf("message = %+v", message)
		}
	default:
		t.Fatal("thread notification was not routed")
	}
}

func TestGenericCLIEventsDeclarePartialCoverage(t *testing.T) {
	for name, event := range map[string]*core.Event{
		"json":  jsonTextMapper([]byte(`{"text":"hello"}`)),
		"plain": partialPlainTextMapper([]byte("hello")),
	} {
		if event == nil || event.Metadata["coverage"] != "partial" {
			t.Fatalf("%s event = %#v", name, event)
		}
	}
}
