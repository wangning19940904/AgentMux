package server

import (
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/framework"
)

func TestFrameworkOperationStoreTracksOneRunPerFramework(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	store := &frameworkOperationStore{operations: map[string]*frameworkOperation{}, now: func() time.Time { return now }}

	op, created, conflict := store.start("codex", "update")
	if !created || conflict || op.State != frameworkOperationRunning || op.Progress.Phase != "checking" {
		t.Fatalf("start = %+v created=%v conflict=%v", op, created, conflict)
	}
	// A second update of the same framework follows the running one; another
	// action on it is refused.
	if again, created, conflict := store.start("codex", "update"); created || conflict || again.ID != op.ID {
		t.Fatalf("second update = %+v created=%v conflict=%v", again, created, conflict)
	}
	if _, created, conflict := store.start("codex", "uninstall"); created || !conflict {
		t.Fatalf("uninstall during update: created=%v conflict=%v", created, conflict)
	}

	store.progress(op.ID, "updating", "brew upgrade --cask codex", 30)
	store.output(op.ID, []byte("==> Fetching downloads for: codex\n"))
	store.output(op.ID, []byte("#####      40.0%\r#########  90.0%\r"))
	running, _ := store.get(op.ID)
	if running.Progress.Phase != "updating" || running.Progress.Detail != "#########  90.0%" || !strings.Contains(running.Log, "Fetching downloads") {
		t.Fatalf("running = %+v", running)
	}
	if listed := store.list(); len(listed) != 1 || listed[0].Log != "" {
		t.Fatalf("list should omit logs: %+v", listed)
	}

	store.finish(op.ID, framework.InstallResult{Kind: "codex", Action: "update", OK: true, Log: "full log", Warning: "exited 1"})
	done, _ := store.get(op.ID)
	if done.State != frameworkOperationSucceeded || done.Result == nil || done.Result.Warning != "exited 1" || done.Log != "full log" || done.FinishedAt == nil {
		t.Fatalf("done = %+v", done)
	}
	if _, created, _ := store.start("codex", "uninstall"); !created {
		t.Fatal("a finished operation should not block the next one")
	}

	now = now.Add(frameworkOperationRetention + time.Minute)
	if _, ok := store.get(op.ID); !ok {
		t.Fatal("get should not prune")
	}
	store.list()
	if _, ok := store.get(op.ID); ok {
		t.Fatal("finished operations older than the retention should be pruned")
	}
}

func TestLastOutputLine(t *testing.T) {
	cases := map[string]string{
		"":                                 "",
		"one\ntwo\n\n":                     "two",
		"progress 10%\rprogress 80%\r\n":   "progress 80%",
		"  ✔ Cask codex (0.169.0)  \n  \n": "✔ Cask codex (0.169.0)",
	}
	for input, want := range cases {
		if got := lastOutputLine(input); got != want {
			t.Errorf("lastOutputLine(%q) = %q, want %q", input, got, want)
		}
	}
}
