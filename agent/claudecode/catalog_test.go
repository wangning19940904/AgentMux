package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

// fakeClaude installs a `claude` on PATH that answers the initialize control
// request with body (the "response" object of a control_response) and records
// its arguments.
func fakeClaude(t *testing.T, body string) (argsFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell mock is unix-only")
	}
	bin := t.TempDir()
	argsFile = filepath.Join(bin, "args")
	script := `#!/bin/sh
printf '%s\n' "$@" > "` + argsFile + `"
read -r line
id=$(printf '%s' "$line" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
echo '{"type":"system","subtype":"init"}'
echo 'not json'
printf '{"type":"control_response","response":{"request_id":"%s",` + body + `}}\n' "$id"
sleep 5
`
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsFile
}

func TestRuntimeSettingsCatalogReadsInitializeModels(t *testing.T) {
	argsFile := fakeClaude(t, `"subtype":"success","response":{"commands":[],"models":[`+
		`{"value":"default","displayName":"Default (recommended)","supportsEffort":true,"supportedEffortLevels":["low","high"]},`+
		`{"value":"group/opus-anywhere","displayName":"Opus anywhere","supportsEffort":true,"supportedEffortLevels":["low","high","max"]},`+
		`{"value":"haiku","displayName":"Haiku"},{"value":"  "}]}`)
	agent, err := core.CreateAgent("claudecode", map[string]any{"model": "default"})
	if err != nil {
		t.Fatal(err)
	}
	cataloger := agent.(core.RuntimeSettingsCataloger)
	defaults, caps, err := cataloger.RuntimeSettingsCatalog(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if defaults.Model != "default" {
		t.Fatalf("defaults = %+v", defaults)
	}
	var models []string
	for _, option := range caps.Models {
		models = append(models, option.Value)
	}
	if !slices.Equal(models, []string{"default", "group/opus-anywhere", "haiku"}) {
		t.Fatalf("models = %v", models)
	}
	if caps.Models[1].Label != "Opus anywhere" {
		t.Fatalf("labels = %+v", caps.Models)
	}
	if got := caps.ForModel("group/opus-anywhere").ReasoningEfforts; len(got) != 3 {
		t.Fatalf("group efforts = %+v", got)
	}
	if got := caps.ForModel("haiku").ReasoningEfforts; len(got) != 0 {
		t.Fatalf("a model without effort support must not inherit efforts: %+v", got)
	}
	if err := core.ValidateRuntimeSettings(caps, core.RuntimeSettings{Model: "group/opus-anywhere", ReasoningEffort: "max"}); err != nil {
		t.Fatal(err)
	}
	if len(caps.ApprovalModes) == 0 {
		t.Fatal("approval modes missing")
	}
	args, _ := os.ReadFile(argsFile)
	for _, want := range []string{"--input-format", "stream-json", "--no-session-persistence"} {
		if !strings.Contains(string(args), want+"\n") {
			t.Fatalf("args %q missing %q", args, want)
		}
	}
}

func TestRuntimeSettingsCatalogReportsInitializeError(t *testing.T) {
	fakeClaude(t, `"subtype":"error","error":"not logged in"`)
	agent := &Agent{}
	_, _, err := agent.RuntimeSettingsCatalog(context.Background(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "not logged in") || !strings.Contains(err.Error(), "discover claude models") {
		t.Fatalf("err = %v", err)
	}
}

func TestScanInitializeResponseNeedsAnswer(t *testing.T) {
	if _, err := scanInitializeResponse(strings.NewReader(`{"type":"system"}` + "\n")); err == nil {
		t.Fatal("expected an error when the CLI exits without answering")
	}
	empty := `{"type":"control_response","response":{"request_id":"` + catalogRequestID + `","subtype":"success","response":{"models":[]}}}`
	if _, err := scanInitializeResponse(strings.NewReader(empty)); err == nil {
		t.Fatal("expected an error for an empty model list")
	}
	other := `{"type":"control_response","response":{"request_id":"other","subtype":"error","error":"x"}}`
	if _, err := scanInitializeResponse(strings.NewReader(other)); err == nil || err.Error() == "x" {
		t.Fatalf("a response to another request must be ignored, err = %v", err)
	}
}

func TestHealthCheckArgsDisableToolsAndTranscript(t *testing.T) {
	agent, err := core.CreateAgent("claudecode", map[string]any{"model": "haiku", core.AgentConfigHealthCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	s, err := newSession(agent.(*Agent), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got := s.args("Reply with exactly: ok")
	for _, want := range []string{"--tools=", "--no-session-persistence"} {
		if !slices.Contains(got, want) {
			t.Fatalf("args %v missing %s", got, want)
		}
	}
	if i := slices.Index(got, "--max-turns"); i < 0 || got[i+1] != "1" {
		t.Fatalf("args %v missing --max-turns 1", got)
	}
	plain, _ := newSession(&Agent{}, t.TempDir())
	if slices.Contains(plain.args("hi"), "--tools=") {
		t.Fatal("normal sessions must keep their tools")
	}
}

func TestSessionsOfferTheFrameworkModelList(t *testing.T) {
	fakeClaude(t, `"subtype":"success","response":{"models":[{"value":"default"},{"value":"group/fast"}]}`)
	agent, err := core.CreateAgent("claudecode", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	started, err := agent.StartSession(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := started.(*session).SupportedModels(); !slices.Equal(got, []string{"default", "group/fast"}) {
		t.Fatalf("in-chat models = %v", got)
	}
	// A configured list wins and needs no discovery.
	configured, _ := core.CreateAgent("claudecode", map[string]any{"supported_models": []string{"sonnet"}})
	started, _ = configured.StartSession(context.Background(), t.TempDir())
	if got := started.(*session).SupportedModels(); !slices.Equal(got, []string{"sonnet"}) {
		t.Fatalf("configured models = %v", got)
	}
}
