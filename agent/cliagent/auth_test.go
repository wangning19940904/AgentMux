package cliagent

import (
	"context"
	"errors"
	"testing"
)

func TestAuthPreflightBeforeCatalogAndResumedTurns(t *testing.T) {
	loginErr := errors.New("login needs renewal")
	nextError := loginErr
	calls := 0
	agent := New(Spec{
		Name: "auth-test", Binary: "/nonexistent-agentmux-test-cli",
		EnsureAuth: func(ctx context.Context, env map[string]string) error {
			calls++
			if env["TRAE_HOME"] != "/agent-specific-home" {
				t.Fatal("wrong auth environment")
			}
			return nextError
		},
		Args: func(string, string, string, string) []string { t.Fatal("turn started with expired auth"); return nil },
		ResumeArgs: func(string, string, string, string, string) []string {
			t.Fatal("resume started with expired auth")
			return nil
		},
	}, map[string]any{"env": map[string]string{"TRAE_HOME": "/agent-specific-home"}})
	if _, err := agent.StartSession(context.Background(), t.TempDir()); !errors.Is(err, loginErr) {
		t.Fatalf("session auth error=%v", err)
	}
	nextError = nil
	sess, err := agent.StartSession(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	nextError = loginErr
	for _, nativeID := range []string{"", "existing-native-thread"} {
		sess.(*session).nativeSessionID = nativeID
		if _, err := sess.Send(context.Background(), "test"); !errors.Is(err, loginErr) {
			t.Fatalf("send auth error=%v", err)
		}
	}
	if calls != 4 {
		t.Fatalf("auth checks=%d, want 4", calls)
	}
}
