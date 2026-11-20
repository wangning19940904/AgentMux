package usage

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

func TestUsageAgentKeyFoldsWorkTreesIntoTheirRepository(t *testing.T) {
	for project, want := range map[string]string{
		"":           UnknownAgent,
		"  ":         UnknownAgent,
		"agent:a1":   "agent:a1",
		"channel:c1": "channel:c1",
		"/work/repo": "/work/repo",
		"-Users-me-repo--claude-worktrees-fix-bug-12ab": "-Users-me-repo",
		"/Users/me/repo/.claude/worktrees/fix-bug":      "/Users/me/repo",
		"/Users/me/.codex/worktrees/256f/repo":          "/Users/me/.codex/worktrees/*/repo",
		"/Users/me/.codex/worktrees/c424/repo":          "/Users/me/.codex/worktrees/*/repo",
		"/Users/me/.codex/worktrees/c424":               "/Users/me/.codex/worktrees/c424",
		"/private/var/folders/x1/abc/T/rev123":          TemporaryAgent,
		"/tmp/run-9":                                    TemporaryAgent,
		"/tmpfiles/kept":                                "/tmpfiles/kept",
	} {
		if got := usageAgentKey(core.UsageRecord{Project: project}); got != want {
			t.Errorf("usageAgentKey(%q) = %q, want %q", project, got, want)
		}
	}
}

func TestReportQueryFiltersAfterReconciliationAndListsUnfilteredFacets(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	eng := NewEngine(&config.Config{Usage: config.UsageConfig{CacheDir: t.TempDir(), Offline: true}}, st, nil)
	at := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	transcript := core.UsageRecord{
		Source: "codex", SessionID: "s1", Model: "gpt-6", Project: "/work/repo", RuntimeID: "codex-app",
		Timestamp: at, InputTokens: 100, OutputTokens: 10,
	}
	// The live twin of the transcript row: same request seen by AgentMux.
	live := transcript
	live.RequestID, live.Provenance, live.Project, live.RuntimeID = "r1", "agentmux.internal", "agent:a1", "codex"
	live.Timestamp = at.Add(5 * time.Second)
	records := []core.UsageRecord{
		transcript, live,
		{Source: "claude", SessionID: "s2", Model: "claude-opus", Project: "-work-repo--claude-worktrees-x", RuntimeID: "claude-desktop",
			Timestamp: at.Add(time.Hour), InputTokens: 1000},
		{Source: "codex", SessionID: "s3", Model: "", Timestamp: at.Add(2 * time.Hour), InputTokens: 7},
	}
	if err := st.UpsertUsage(ctx, records); err != nil {
		t.Fatal(err)
	}
	report := func(filter ReportFilter) *Report {
		t.Helper()
		got, err := eng.ReportQuery(ctx, ReportQuery{Period: "daily", Location: time.UTC, Filter: filter})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	tokens := func(r *Report) int64 {
		return r.Totals.InputTokens + r.Totals.OutputTokens + r.Totals.CacheReadTokens + r.Totals.CacheWriteTokens
	}

	all := report(ReportFilter{})
	if got := tokens(all); got != 110+1000+7 {
		t.Fatalf("unfiltered tokens = %d", got)
	}
	wantFacets := &Facets{
		Models:   []FacetValue{{"claude-opus", 1000}, {"gpt-6", 110}, {"", 7}},
		Runtimes: []FacetValue{{"claude-desktop", 1000}, {"codex-agentmux", 110}, {"codex-unknown", 7}},
		Agents:   []FacetValue{{"-work-repo", 1000}, {"agent:a1", 110}, {UnknownAgent, 7}},
	}
	if !reflect.DeepEqual(all.Facets, wantFacets) {
		t.Fatalf("facets = %+v", all.Facets)
	}

	// The transcript twin matches this filter, but it is the dropped duplicate
	// of the live record: filtering first would count the request again.
	if got := tokens(report(ReportFilter{Agents: []string{"/work/repo"}})); got != 0 {
		t.Fatalf("a reconciled duplicate was counted: %d tokens", got)
	}
	if got := tokens(report(ReportFilter{Agents: []string{"agent:a1", UnknownAgent}})); got != 117 {
		t.Fatalf("agent filter tokens = %d", got)
	}
	if got := tokens(report(ReportFilter{Models: []string{""}})); got != 7 {
		t.Fatalf("unknown-model filter tokens = %d", got)
	}
	if got := tokens(report(ReportFilter{Runtimes: []string{"claude-desktop"}, Models: []string{"gpt-6"}})); got != 0 {
		t.Fatalf("dimensions must combine: %d tokens", got)
	}
	filtered := report(ReportFilter{Sources: []string{"claude"}})
	if got := tokens(filtered); got != 1000 {
		t.Fatalf("source filter tokens = %d", got)
	}
	if !reflect.DeepEqual(filtered.Facets, wantFacets) {
		t.Fatalf("facets must not shrink with the filter: %+v", filtered.Facets)
	}
}

func TestNextCollectSinceResumesFromTheLastSuccessfulPass(t *testing.T) {
	initial := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if got := nextCollectSince(time.Time{}, initial); !got.Equal(initial) {
		t.Fatalf("before any pass succeeds = %v, want the initial window", got)
	}
	// The machine slept for a day after this pass. The next pass must still
	// reach back to it rather than to two minutes before "now".
	last := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	if got, want := nextCollectSince(last, initial), last.Add(-collectOverlap); !got.Equal(want) {
		t.Fatalf("since = %v, want %v", got, want)
	}
}

func TestCollectProgressRetriesAFailingWindowForALimitedTime(t *testing.T) {
	start := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	failure := errors.New("codex: invalid receipt")
	var progress collectProgress
	if !progress.finish(start, nil) || !progress.last.Equal(start) {
		t.Fatalf("a successful pass must advance: %+v", progress)
	}
	// A failed pass keeps the window: the next pass reads from the last
	// success again.
	for _, after := range []time.Duration{30 * time.Second, 10 * time.Minute, 59 * time.Minute} {
		if progress.finish(start.Add(after), failure) {
			t.Fatalf("advanced past a pass that failed %v after the last success", after)
		}
	}
	if !progress.last.Equal(start) {
		t.Fatalf("last = %v, want the last successful pass", progress.last)
	}
	// A fault that does not clear stops holding the window open.
	giveUp := start.Add(30*time.Second + maxCollectRetry)
	if !progress.finish(giveUp, failure) || !progress.last.Equal(giveUp) {
		t.Fatalf("a pass failing for over %v must move on: %+v", maxCollectRetry, progress)
	}
	if !progress.finish(giveUp.Add(30*time.Second), failure) {
		t.Fatal("while the fault persists, every pass moves on")
	}
	// Recovery starts the allowance afresh.
	recovered := giveUp.Add(time.Minute)
	progress.finish(recovered, nil)
	if progress.finish(recovered.Add(30*time.Second), failure) {
		t.Fatal("a new fault after a success must be retried again")
	}
}
