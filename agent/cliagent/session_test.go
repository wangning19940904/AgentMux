package cliagent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func TestSessionReportsStderrOnProcessFailure(t *testing.T) {
	events := runHelperSession(t, "stderr")

	var got error
	for _, ev := range events {
		if ev.Type == core.EventError {
			got = ev.Err
		}
	}
	if got == nil {
		t.Fatal("expected an error event")
	}
	if msg := got.Error(); !strings.Contains(msg, "specific helper failure") || !strings.Contains(msg, "exit status") {
		t.Fatalf("error = %q, want stderr detail and exit status", msg)
	}
}

func TestSessionDrainsAndBoundsLargeStderr(t *testing.T) {
	events := runHelperSession(t, "large-stderr")
	var got error
	for _, ev := range events {
		if ev.Type == core.EventError {
			got = ev.Err
		}
	}
	if got == nil {
		t.Fatal("expected an error event")
	}
	if msg := got.Error(); !strings.Contains(msg, "large stderr marker") || len(msg) > stderrTailLimit+256 {
		t.Fatalf("bounded error len=%d contains-marker=%t", len(msg), strings.Contains(msg, "large stderr marker"))
	}
}

func TestSessionReportsScannerErrorWithoutDeadlock(t *testing.T) {
	events := runHelperSession(t, "long-stdout")
	var got error
	for _, ev := range events {
		if ev.Type == core.EventError {
			got = ev.Err
		}
	}
	if got == nil || !strings.Contains(got.Error(), "token too long") {
		t.Fatalf("error = %v, want Scanner token-too-long error", got)
	}
}

func runHelperSession(t *testing.T, scenario string) []*core.Event {
	t.Helper()
	agent := New(Spec{
		Name:          "helper",
		Binary:        os.Args[0],
		SupportsModel: true,
		Args: func(_, _, _ string) []string {
			return []string{"-test.run=TestCLIHelperProcess", "--", scenario}
		},
		Mapper: PlainTextMapper,
	}, map[string]any{
		"env": map[string]string{"GO_WANT_CLIAGENT_HELPER": "1"},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sess, err := agent.StartSession(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	eventCh, err := sess.Send(ctx, "ignored")
	if err != nil {
		t.Fatal(err)
	}

	var events []*core.Event
	for {
		select {
		case ev, ok := <-eventCh:
			if !ok {
				return events
			}
			events = append(events, ev)
		case <-ctx.Done():
			t.Fatalf("helper session timed out: %v", ctx.Err())
		}
	}
}

func TestTailBufferKeepsLatestBytes(t *testing.T) {
	b := &tailBuffer{limit: 8}
	_, _ = b.Write([]byte("first-"))
	_, _ = b.Write([]byte("failure"))
	if got, want := b.String(), "-failure"; got != want {
		t.Fatalf("tail = %q, want %q", got, want)
	}
}

func TestCLIHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_CLIAGENT_HELPER") != "1" {
		return
	}
	args := os.Args
	scenario := "stderr"
	for i, arg := range args {
		if arg == "--" && i+1 < len(args) {
			scenario = args[i+1]
			break
		}
	}
	switch scenario {
	case "large-stderr":
		fmt.Fprint(os.Stderr, strings.Repeat("x", stderrTailLimit*8))
		fmt.Fprintln(os.Stderr, "large stderr marker")
	case "long-stdout":
		fmt.Fprint(os.Stdout, strings.Repeat("x", stdoutScanLimit+1024))
	default:
		fmt.Fprintln(os.Stderr, "specific helper failure")
	}
	os.Exit(23)
}
