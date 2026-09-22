package core

import (
	"strings"
	"testing"
)

func TestChannelRejectsUnsupportedDefaultInsteadOfRunningWithFallback(t *testing.T) {
	session := &approvalSession{settings: NewRuntimeSettingsSelection(
		RuntimeSettings{Model: "grok-4.7", ReasoningEffort: "low"},
		RuntimeSettingsCapabilities{
			Models:           RuntimeOptions([]string{"grok-4.7"}),
			ReasoningEfforts: RuntimeOptions([]string{"low", "medium", "high", "xhigh"}),
		},
	)}
	rt := &channelRuntime{}
	err := rt.applyRuntimeDefaultsFrom(session, RuntimeSettings{Model: "grok-4.7", ReasoningEffort: "max"})
	if err == nil || !strings.Contains(err.Error(), "reasoning_effort=\"max\"") {
		t.Fatalf("unsupported default should prevent the turn: %v", err)
	}
	if err := rt.applyRuntimeDefaultsFrom(session, RuntimeSettings{Model: "grok-4.7", ReasoningEffort: "xhigh"}); err != nil {
		t.Fatal(err)
	}
	if got := session.CurrentRuntimeSettings().ReasoningEffort; got != "xhigh" {
		t.Fatalf("effective effort = %q", got)
	}
}
