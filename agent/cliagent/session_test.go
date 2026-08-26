package cliagent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func TestSessionReportsStderrOnProcessFailure(t *testing.T) {
	events := runHelperSession(t, "stderr")

	var got error
	var metadata map[string]string
	for _, ev := range events {
		if ev.Type == core.EventError {
			got = ev.Err
			metadata = ev.Metadata
		}
	}
	if got == nil {
		t.Fatal("expected an error event")
	}
	if msg := got.Error(); !strings.Contains(msg, "specific helper failure") || !strings.Contains(msg, "exit status") {
		t.Fatalf("error = %q, want stderr detail and exit status", msg)
	}
	if metadata["runtime"] != "helper" || metadata["transport"] != "process" || metadata["lifecycle"] != "failed" {
		t.Fatalf("process error metadata = %#v", metadata)
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

func TestSessionStreamsMappedStderrBeforeProcessExit(t *testing.T) {
	agent := New(Spec{
		Name:   "stderr-stream-helper",
		Binary: os.Args[0],
		Args: func(_, _, _, _ string) []string {
			return []string{"-test.run=TestCLIHelperProcess", "--", "stream-stderr"}
		},
		Mapper: PlainTextMapper, FinalFromLast: true,
		NewStderrMapper: func() LineMapper {
			var output string
			return func(line []byte) *core.Event {
				if output != "" {
					output += "\n"
				}
				output += string(line)
				return &core.Event{Type: core.EventOutput, Text: output}
			}
		},
	}, map[string]any{"env": map[string]string{"GO_WANT_CLIAGENT_HELPER": "1"}})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, err := agent.StartSession(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	events, err := sess.Send(ctx, "ignored")
	if err != nil {
		t.Fatal(err)
	}

	select {
	case event := <-events:
		if event == nil || event.Type != core.EventOutput || !strings.Contains(event.Text, "https://login.example/device") {
			t.Fatalf("first live stderr event = %#v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stderr was buffered until process exit")
	}

	var final *core.Event
	for event := range events {
		if event.Type == core.EventFinal {
			final = event
		}
	}
	if final == nil || final.Text != "completed" {
		t.Fatalf("final event = %#v", final)
	}
}

func TestSessionEventMapperCanReturnMultipleEvents(t *testing.T) {
	sess := &session{agent: &Agent{spec: Spec{
		Mapper: func([]byte) *core.Event {
			t.Fatal("single-event mapper must not run when EventMapper is configured")
			return nil
		},
		EventMapper: func([]byte) []*core.Event {
			return []*core.Event{{Type: core.EventToolUse, ToolName: "tool"}, {Type: core.EventOutput, Text: "visible output"}}
		},
	}}}
	events := sess.mapOutputLine([]byte(`{"type":"combined"}`))
	if len(events) != 2 || events[0].Type != core.EventToolUse || events[1].Text != "visible output" {
		t.Fatalf("mapped events = %#v", events)
	}
}

func TestSessionRemembersNativeSessionForResume(t *testing.T) {
	sess := &session{}
	sess.rememberNativeSessionID(" native-thread ")
	if got := sess.currentNativeSessionID(); got != "native-thread" {
		t.Fatalf("native session id = %q", got)
	}
	sess.rememberNativeSessionID("")
	if got := sess.currentNativeSessionID(); got != "native-thread" {
		t.Fatalf("empty update cleared native session id: %q", got)
	}
}

func TestStartSessionDiscoversAndCachesRuntimeModels(t *testing.T) {
	var parses atomic.Int32
	agent := New(Spec{
		Name:          "catalog-helper",
		Binary:        os.Args[0],
		SupportsModel: true,
		Args: func(_, _, _, _ string) []string {
			return nil
		},
		ModelCatalogArgs: []string{"-test.run=TestCLIHelperProcess", "--", "models"},
		ParseModelCatalog: func(output []byte) (ModelCatalog, error) {
			parses.Add(1)
			if !strings.Contains(string(output), "model catalog") {
				return ModelCatalog{}, fmt.Errorf("unexpected output: %s", output)
			}
			return ModelCatalog{Models: []string{"cursor-default", "cursor-fast"}, DefaultModel: "cursor-default"}, nil
		},
		ReasoningEfforts: []string{"low", "high"},
		ServiceTiers:     []string{"default", "priority"},
	}, map[string]any{"env": map[string]string{"GO_WANT_CLIAGENT_HELPER": "1"}})

	for i := 0; i < 2; i++ {
		sess, err := agent.StartSession(context.Background(), t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		settings, ok := core.RuntimeSettingsForSession(sess)
		if !ok {
			t.Fatal("discovered session does not expose runtime settings")
		}
		current := settings.CurrentRuntimeSettings()
		caps := settings.RuntimeSettingsCapabilities()
		if current.Model != "cursor-default" || len(caps.Models) != 2 || len(caps.ReasoningEfforts) != 2 || len(caps.ServiceTiers) != 2 {
			t.Fatalf("settings = %#v, capabilities = %#v", current, caps)
		}
	}
	if got := parses.Load(); got != 1 {
		t.Fatalf("catalog parser calls = %d, want one cached discovery", got)
	}
}

func TestSessionHidesSettingsFixedBySelectedModel(t *testing.T) {
	agent := New(Spec{
		Name: "parameterized-helper", SupportsModel: true,
		Args:             func(_, _, _, _ string) []string { return nil },
		ReasoningEfforts: []string{"low", "medium", "high"},
		ServiceTiers:     []string{"default", "priority"},
		EmbeddedModelSettings: func(model string) core.RuntimeSettings {
			if model == "fixed-medium-fast" {
				return core.RuntimeSettings{ReasoningEffort: "medium", ServiceTier: "priority"}
			}
			return core.RuntimeSettings{}
		},
	}, map[string]any{
		"model":            "fixed-medium-fast",
		"supported_models": []string{"fixed-medium-fast", "flexible"},
	})
	sess, err := agent.StartSession(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	settings, ok := core.RuntimeSettingsForSession(sess)
	if !ok {
		t.Fatal("session does not expose runtime settings")
	}
	current := settings.CurrentRuntimeSettings()
	caps := settings.RuntimeSettingsCapabilities()
	if current.ReasoningEffort != "medium" || current.ServiceTier != "priority" {
		t.Fatalf("effective settings = %#v", current)
	}
	if len(caps.ReasoningEfforts) != 0 || len(caps.ServiceTiers) != 0 {
		t.Fatalf("fixed controls still exposed: %#v", caps)
	}
	if err := settings.SetRuntimeSetting(core.RuntimeSettingReasoningEffort, "high"); err == nil {
		t.Fatal("fixed reasoning effort should reject an independent override")
	}
	if err := settings.SetRuntimeSetting(core.RuntimeSettingModel, "flexible"); err != nil {
		t.Fatal(err)
	}
	if len(settings.RuntimeSettingsCapabilities().ReasoningEfforts) == 0 || len(settings.RuntimeSettingsCapabilities().ServiceTiers) == 0 {
		t.Fatal("flexible model should restore independent controls")
	}
}

func runHelperSession(t *testing.T, scenario string) []*core.Event {
	t.Helper()
	agent := New(Spec{
		Name:          "helper",
		Binary:        os.Args[0],
		SupportsModel: true,
		Args: func(_, _, _, _ string) []string {
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
	case "models":
		fmt.Fprintln(os.Stdout, "model catalog")
		return
	case "stream-stderr":
		fmt.Fprintln(os.Stderr, "https://login.example/device")
		fmt.Fprintln(os.Stderr, "verification code: ABCD-EFGH")
		time.Sleep(350 * time.Millisecond)
		fmt.Fprintln(os.Stdout, "completed")
		os.Exit(0)
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
