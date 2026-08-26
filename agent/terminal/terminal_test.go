package terminal

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func TestNewValidatesTerminalRuntime(t *testing.T) {
	if _, err := New(map[string]any{"terminal_runtime": "not-a-runtime"}); err == nil {
		t.Fatal("SDK runtime was accepted by terminal backend")
	}
	if _, err := New(map[string]any{"terminal_runtime": "codex", "env": map[string]string{"-u": "HOME"}}); err == nil {
		t.Fatal("option-like environment key was accepted")
	}
	agent, err := New(map[string]any{
		"terminal_runtime": "claude-code",
		"model":            "sonnet",
		"reasoning_effort": "high",
		"approval_mode":    core.ApprovalModePlan,
	})
	if err != nil {
		t.Fatal(err)
	}
	if agent.Name() != "claudecode" {
		t.Fatalf("agent name = %q", agent.Name())
	}
	args := agent.spec.Args(agent.defaults)
	joined := strings.Join(args, " ")
	for _, want := range []string{"--model sonnet", "--effort high", "--permission-mode plan"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("claude args %q missing %q", joined, want)
		}
	}
}

func TestTmuxSessionStreamsSnapshotAndResumes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake tmux fixture uses a POSIX shell")
	}
	binDir := t.TempDir()
	stateDir := t.TempDir()
	writeFakeTmux(t, filepath.Join(binDir, "tmux"))
	cli := filepath.Join(binDir, "fake-agent")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_TMUX_DIR", stateDir)

	agent, err := New(map[string]any{
		"terminal_runtime":            "codex",
		"terminal_binary":             cli,
		"system_prompt":               "Keep changes scoped.",
		"terminal_idle_timeout_ms":    40,
		"terminal_poll_interval_ms":   5,
		"terminal_minimum_latency_ms": 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := agent.StartSession(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess := raw.(*session)
	events, err := sess.Send(context.Background(), "run tests")
	if err != nil {
		t.Fatal(err)
	}
	final := collectFinal(t, events)
	if !strings.Contains(final, "System instructions") || !strings.Contains(final, "run tests") || !strings.Contains(final, "fake response complete") {
		t.Fatalf("final snapshot = %q", final)
	}
	info := sess.TerminalInfo()
	if !info.Available || info.Backend != "tmux" || !strings.Contains(info.AttachCommand, sess.id) {
		t.Fatalf("terminal info = %+v", info)
	}
	if err := sess.Detach(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !sess.TerminalInfo().Available {
		t.Fatal("detaching killed the tmux session")
	}
	if _, err := sess.Send(context.Background(), "must be rejected by detached handle"); err == nil {
		t.Fatal("detached in-process handle still accepted input")
	}

	resumedRaw, err := agent.StartSessionResume(context.Background(), sess.workDir, sess.NativeSessionID())
	if err != nil {
		t.Fatal(err)
	}
	resumed := resumedRaw.(*session)
	events, err = resumed.Send(context.Background(), "follow up")
	if err != nil {
		t.Fatal(err)
	}
	if got := collectFinal(t, events); !strings.Contains(got, "follow up") {
		t.Fatalf("resumed snapshot = %q", got)
	}
	if err := resumed.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if resumed.TerminalInfo().Available {
		t.Fatal("closed tmux session is still available")
	}
	if _, err := agent.StartSessionResume(context.Background(), sess.workDir, sess.id); err != core.ErrNativeSessionUnavailable {
		t.Fatalf("resume missing session error = %v", err)
	}
}

func collectFinal(t *testing.T, events <-chan *core.Event) string {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("terminal turn timed out")
		case event, ok := <-events:
			if !ok {
				t.Fatal("terminal turn closed without final")
			}
			if event.Type == core.EventError {
				t.Fatalf("terminal event error: %v", event.Err)
			}
			if event.Type == core.EventFinal {
				return event.Text
			}
		}
	}
}

func writeFakeTmux(t *testing.T, path string) {
	t.Helper()
	script := `#!/bin/sh
set -eu
root="${FAKE_TMUX_DIR:?}"
cmd="$1"
shift
session=""
buffer="default"
previous=""
for arg in "$@"; do
  case "$previous" in
    -s|-t) session="${arg#=}" ;;
    -b) buffer="$arg" ;;
  esac
  previous="$arg"
done
case "$cmd" in
  new-session)
    mkdir -p "$root/$session"
    printf 'fake cli ready\n' > "$root/$session/pane"
    ;;
  has-session)
    test -f "$root/$session/pane"
    ;;
  load-buffer)
    cat > "$root/buffer-$buffer"
    ;;
  paste-buffer)
    cat "$root/buffer-$buffer" >> "$root/$session/pane"
    rm -f "$root/buffer-$buffer"
    ;;
  send-keys)
    last=""
    for arg in "$@"; do last="$arg"; done
    if [ "$last" = "Enter" ]; then
      printf '\nfake response complete\n' >> "$root/$session/pane"
    elif [ "$last" = "C-c" ]; then
      printf '\ninterrupted\n' >> "$root/$session/pane"
    fi
    ;;
  capture-pane)
    cat "$root/$session/pane"
    ;;
  resize-window)
    :
    ;;
  kill-session)
    rm -rf "$root/$session"
    ;;
  list-sessions)
    for dir in "$root"/amux-*; do
      [ -d "$dir" ] && basename "$dir"
    done
    ;;
  *)
    echo "unsupported fake tmux command: $cmd" >&2
    exit 2
    ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}
