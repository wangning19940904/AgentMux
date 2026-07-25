package core

import (
	"context"
	"testing"
)

func TestShellHookInheritsEnvironmentAndReceivesFullJSONOnStdin(t *testing.T) {
	t.Setenv("AGENTMUX_PARENT_ENV", "visible")
	err := RunHookAction(context.Background(), ActionShell,
		`test "$AGENTMUX_PARENT_ENV" = visible && test "$HOOK_EVENT" = message.received && grep -q '"trace_id":"trace-1"'`, "",
		map[string]string{"event": "message.received", "trace_id": "trace-1"})
	if err != nil {
		t.Fatal(err)
	}
}
