package core

import (
	"context"
	"strings"
	"testing"
)

func TestParseApprovalModeCommands(t *testing.T) {
	tests := []struct {
		input string
		want  runtimeSettingsCommand
	}{
		{input: "/approval", want: runtimeSettingsCommand{Setting: RuntimeSettingApprovalMode, List: true}},
		{input: "/approval status", want: runtimeSettingsCommand{Setting: RuntimeSettingApprovalMode, List: true}},
		{input: "/approval yolo", want: runtimeSettingsCommand{Setting: RuntimeSettingApprovalMode, Value: ApprovalModeYolo}},
		{input: "/permissions auto", want: runtimeSettingsCommand{Setting: RuntimeSettingApprovalMode, Value: ApprovalModeAuto}},
		{input: "/approval reset", want: runtimeSettingsCommand{Setting: RuntimeSettingApprovalMode, Reset: true}},
		{input: "/yolo on", want: runtimeSettingsCommand{Setting: RuntimeSettingApprovalMode, Value: ApprovalModeYolo}},
		{input: "/yolo off", want: runtimeSettingsCommand{Setting: RuntimeSettingApprovalMode, Value: ApprovalModeManual}},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, ok := parseRuntimeSettingsCommand(tt.input)
			if !ok || got != tt.want {
				t.Fatalf("parseRuntimeSettingsCommand(%q) = %+v, %t; want %+v, true", tt.input, got, ok, tt.want)
			}
		})
	}
}

func TestApprovalSlashCommandSwitchesCurrentConversation(t *testing.T) {
	sess := &runtimeCommandSession{RuntimeSettingsSelection: NewRuntimeSettingsSelection(
		RuntimeSettings{ApprovalMode: ApprovalModeManual},
		RuntimeSettingsCapabilities{ApprovalModes: RuntimeOptions(ApprovalModeValuesForRuntime("cursor"))},
	)}
	eng := NewEngine(nil, NewHookRunner(nil, nil))
	var reply string
	if !eng.handleRuntimeSettingsCommand(sess, "/yolo on", func(text string) { reply = text }, nil, nil) {
		t.Fatal("/yolo on was not handled")
	}
	if got := sess.CurrentRuntimeSettings().ApprovalMode; got != ApprovalModeYolo {
		t.Fatalf("approval mode = %q, want yolo", got)
	}
	for _, want := range []string{"审批模式已切换为", "作用范围：当前会话", "/yolo off"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply missing %q:\n%s", want, reply)
		}
	}

	if !eng.handleRuntimeSettingsCommand(sess, "/yolo off", func(text string) { reply = text }, nil, nil) {
		t.Fatal("/yolo off was not handled")
	}
	if got := sess.CurrentRuntimeSettings().ApprovalMode; got != ApprovalModeManual {
		t.Fatalf("approval mode = %q, want manual", got)
	}
}

func TestApprovalSlashCommandExplainsUnsupportedMode(t *testing.T) {
	sess := &runtimeCommandSession{RuntimeSettingsSelection: NewRuntimeSettingsSelection(
		RuntimeSettings{ApprovalMode: ApprovalModeManual},
		RuntimeSettingsCapabilities{ApprovalModes: RuntimeOptions([]string{ApprovalModeManual, ApprovalModePlan})},
	)}
	eng := NewEngine(nil, NewHookRunner(nil, nil))
	var reply string
	eng.handleRuntimeSettingsCommand(sess, "/approval yolo", func(text string) { reply = text }, nil, nil)
	for _, want := range []string{"无法切换审批模式", "manual, plan", "/approval <模式>"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("error reply missing %q:\n%s", want, reply)
		}
	}
}

type runtimeCommandSession struct {
	*RuntimeSettingsSelection
}

func (s *runtimeCommandSession) ID() string { return "runtime-command" }
func (s *runtimeCommandSession) Send(context.Context, string) (<-chan *Event, error) {
	out := make(chan *Event)
	close(out)
	return out, nil
}
func (s *runtimeCommandSession) RespondPermission(context.Context, bool) error { return nil }
func (s *runtimeCommandSession) Close(context.Context) error                   { return nil }
