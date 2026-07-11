package cliagents

import (
	"reflect"
	"testing"

	"github.com/agentnexus/agentnexus/core"
)

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
