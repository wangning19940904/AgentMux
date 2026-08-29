package authflow

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestSessionLifecycleAndRegistry(t *testing.T) {
	registry := NewRegistry(0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session, created := registry.Create("codex", true, cancel)
	if !created {
		t.Fatal("first session was not created")
	}
	_, duplicateCreated := registry.Create("codex", true, func() {})
	if duplicateCreated {
		t.Fatal("duplicate active session was created")
	}

	writer := &bufferWriteCloser{}
	if err := session.AttachInput(writer); err != nil {
		t.Fatal(err)
	}
	if !session.Actionable("https://example.test/device", "ABCD-EFGH") {
		t.Fatal("actionable state was not recorded")
	}
	if err := session.WriteInput("result-code", 32); err != nil {
		t.Fatal(err)
	}
	if got := writer.String(); got != "result-code\n" {
		t.Fatalf("input = %q", got)
	}
	if !session.Finish(StateSucceeded, "") {
		t.Fatal("session did not finish")
	}
	if session.Cancel("late cancellation") {
		t.Fatal("terminal state was overwritten")
	}
	if got := session.Snapshot().State; got != StateSucceeded {
		t.Fatalf("state = %q", got)
	}
	if err := session.WriteInput("late", 32); err == nil {
		t.Fatal("terminal session accepted input")
	}
	registry.Release(session)
	if _, ok := registry.Get(session.Snapshot().SessionID); ok {
		t.Fatal("zero-TTL session was retained")
	}
	if ctx.Err() == nil {
		t.Fatal("terminal transition did not cancel context")
	}
}

type bufferWriteCloser struct{ bytes.Buffer }

func (*bufferWriteCloser) Close() error { return nil }

func TestParsingAndEnvironmentHelpers(t *testing.T) {
	line := "\x1b[32mVisit https://example.test/device). code ABCD EFGH\x1b[0m"
	if got := ActionableURL(line); got != "https://example.test/device" {
		t.Fatalf("url = %q", got)
	}
	if got := ActionableCode(line); got != "ABCD-EFGH" {
		t.Fatalf("code = %q", got)
	}
	env := MergeEnvironment([]string{"A=1", "B=2"}, map[string]string{"B": "3", "C": "4"})
	joined := strings.Join(env, "\n")
	for _, expected := range []string{"A=1", "B=3", "C=4"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("environment %q missing %q", joined, expected)
		}
	}
	if strings.Contains(joined, "B=2") {
		t.Fatalf("old override survived: %q", joined)
	}
}
