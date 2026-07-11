package cliagents

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/agentnexus/agentnexus/core"
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
			got:  cursorArgs("hello", "", "sonnet-4"),
			want: []string{"agent", "--print", "--output-format", "stream-json", "--model", "sonnet-4", "hello"},
		},
	}
	for _, tt := range tests {
		if !reflect.DeepEqual(tt.got, tt.want) {
			t.Fatalf("%s args = %#v, want %#v", tt.name, tt.got, tt.want)
		}
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
