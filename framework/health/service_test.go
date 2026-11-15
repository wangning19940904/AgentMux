package health

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/framework"
)

// step waits until the loop is idle on its one timer, then advances the clock.
func (e *testEnv) step(t *testing.T, d time.Duration) {
	t.Helper()
	waitFor(t, "the loop to wait on the clock", func() bool { return e.clock.pending() == 1 })
	e.clock.Advance(d)
}

func storeSettings(t *testing.T, st *memStore, settings Settings) {
	t.Helper()
	raw, _ := json.Marshal(settings)
	st.data[SettingsSetting] = string(raw)
}

func TestPeriodicChecksOffRunNothing(t *testing.T) {
	env := newTestEnv(t, "claudecode", "codex")
	env.start(t)
	for range 180 {
		env.step(t, time.Minute)
	}
	env.svc.ObserveRun("claudecode", "Error: Not logged in · Please run /login")
	time.Sleep(20 * time.Millisecond)
	if n := env.checker.callCount(); n != 0 {
		t.Fatalf("checks with periodic checks off = %d, want 0", n)
	}
	if _, ok := env.store.data[RecordsSetting]; ok {
		t.Fatalf("records were written with periodic checks off")
	}
	rec, err := env.svc.Status(context.Background(), "claudecode")
	if err != nil || rec.State != StateUnknown {
		t.Fatalf("status = %+v, %v", rec, err)
	}
}

func TestPeriodicRoundsRunOneAfterAnotherAtTheInterval(t *testing.T) {
	env := newTestEnv(t, "codex", "claudecode")
	storeSettings(t, env.store, Settings{Enabled: true, IntervalMinutes: 30})
	start := env.clock.Now()
	env.start(t)

	env.step(t, 30*time.Second) // start delay: the first round checks both
	waitFor(t, "first check", func() bool { return env.checker.callCount() == 1 })
	env.step(t, 10*time.Second) // the gap between checks
	waitFor(t, "second check", func() bool { return env.checker.callCount() == 2 })
	calls := env.checker.callsSnapshot()
	if calls[0].kind != "claudecode" || calls[1].kind != "codex" {
		t.Fatalf("round order = %+v", calls)
	}
	if got := calls[1].at.Sub(calls[0].at); got != 10*time.Second {
		t.Fatalf("gap between checks = %s", got)
	}
	if calls[0].at.Sub(start) != 30*time.Second {
		t.Fatalf("first round at +%s", calls[0].at.Sub(start))
	}

	// Nothing is due until the interval has passed.
	for range 29 {
		env.step(t, time.Minute)
	}
	time.Sleep(10 * time.Millisecond)
	if n := env.checker.callCount(); n != 2 {
		t.Fatalf("checks before the interval = %d", n)
	}
	env.step(t, time.Minute)
	waitFor(t, "next round", func() bool { return env.checker.callCount() == 3 })
	env.step(t, 10*time.Second)
	waitFor(t, "next round's second check", func() bool { return env.checker.callCount() == 4 })

	env.checker.mu.Lock()
	maxAtOnce := env.checker.maxAtOne
	env.checker.mu.Unlock()
	if maxAtOnce != 1 {
		t.Fatalf("checks at once = %d, want 1", maxAtOnce)
	}
	rec, _ := env.svc.Status(context.Background(), "codex")
	if rec.State != StateOK || rec.Source != SourcePeriodic || rec.ProbedAt == nil {
		t.Fatalf("record = %+v", rec)
	}
}

func TestStartupRoundChecksOnlyRecordsOlderThanTheInterval(t *testing.T) {
	env := newTestEnv(t, "claudecode", "codex")
	storeSettings(t, env.store, Settings{Enabled: true, IntervalMinutes: 30})
	now := env.clock.Now()
	recent, old := now.Add(-10*time.Minute), now.Add(-2*time.Hour)
	raw, _ := json.Marshal(persisted{Records: []Record{
		{Kind: "claudecode", State: StateOK, CheckedAt: recent, ProbedAt: &recent},
		{Kind: "codex", State: StateOK, CheckedAt: old, ProbedAt: &old},
	}})
	env.store.data[RecordsSetting] = string(raw)
	env.start(t)
	env.step(t, 30*time.Second)
	waitFor(t, "startup check", func() bool { return env.checker.callCount() == 1 })
	time.Sleep(10 * time.Millisecond)
	if calls := env.checker.callsSnapshot(); len(calls) != 1 || calls[0].kind != "codex" {
		t.Fatalf("startup round = %+v", calls)
	}
}

func TestTimeoutIsRetriedOnceAndOnlyTwoInARowFail(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	env.checker.script("blip", failure(framework.SmokeTimeout, "no reply within 1m30s"))
	env.checker.script("stuck", failure(framework.SmokeTimeout, "no reply"), failure(framework.SmokeTimeout, "no reply"))
	env.checker.script("signedout", failure(framework.SmokeUnauthenticated, "Not logged in"))

	rec, err := env.svc.Recheck(ctx, "blip")
	if err != nil || rec.State != StateOK {
		t.Fatalf("blip = %+v, %v", rec, err)
	}
	rec, _ = env.svc.Recheck(ctx, "stuck")
	if rec.State != StateFailing || rec.ErrorKind != framework.SmokeTimeout {
		t.Fatalf("stuck = %+v", rec)
	}
	rec, _ = env.svc.Recheck(ctx, "signedout")
	if rec.State != StateFailing || rec.ErrorKind != framework.SmokeUnauthenticated || rec.Reason == "" {
		t.Fatalf("signedout = %+v", rec)
	}
	counts := map[string]int{}
	for _, call := range env.checker.callsSnapshot() {
		counts[call.kind]++
	}
	if counts["blip"] != 2 || counts["stuck"] != 2 || counts["signedout"] != 1 {
		t.Fatalf("checks per kind = %v", counts)
	}
}

func TestRecheckIsSingleFlightAndThrottled(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	gate := make(chan struct{})
	env.checker.gate = gate

	var wg sync.WaitGroup
	records := make([]Record, 5)
	for i := range records {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec, err := env.svc.Recheck(ctx, "codex")
			if err != nil {
				t.Error(err)
			}
			records[i] = rec
		}()
	}
	waitFor(t, "the shared check to start", func() bool { return env.checker.callCount() == 1 })
	time.Sleep(10 * time.Millisecond) // let the other callers join the flight
	close(gate)
	wg.Wait()
	if n := env.checker.callCount(); n != 1 {
		t.Fatalf("concurrent rechecks ran %d checks, want 1", n)
	}
	for _, rec := range records {
		if rec.State != StateOK {
			t.Fatalf("records = %+v", records)
		}
	}

	env.clock.Advance(20 * time.Second)
	if _, err := env.svc.Recheck(ctx, "codex"); err != nil {
		t.Fatal(err)
	}
	if n := env.checker.callCount(); n != 1 {
		t.Fatalf("recheck within the throttle ran a check (%d)", n)
	}
	env.clock.Advance(11 * time.Second)
	if _, err := env.svc.Recheck(ctx, "codex"); err != nil {
		t.Fatal(err)
	}
	if n := env.checker.callCount(); n != 2 {
		t.Fatalf("recheck after the throttle: %d checks, want 2", n)
	}
}

func TestRecheckOutlivesACallerThatGoesAway(t *testing.T) {
	env := newTestEnv(t)
	gate := make(chan struct{})
	env.checker.gate = gate
	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() {
		_, err := env.svc.Recheck(ctx, "codex")
		errs <- err
	}()
	waitFor(t, "check to start", func() bool { return env.checker.callCount() == 1 })
	cancel()
	if err := <-errs; err == nil {
		t.Fatalf("cancelled caller got no error")
	}
	close(gate)
	waitFor(t, "check to be recorded", func() bool {
		rec, _ := env.svc.Status(context.Background(), "codex")
		return rec.State == StateOK
	})
}

func TestUsageLimitIsNotCheckedBeforeItsReset(t *testing.T) {
	env := newTestEnv(t, "claudecode")
	ctx := context.Background()
	env.checker.script("claudecode", failure(framework.SmokeUsageLimit, "You've hit your usage limit. Try again in 2 hours."))
	rec, err := env.svc.Recheck(ctx, "claudecode")
	if err != nil || rec.State != StateUsageLimit || rec.ResetsAt == nil {
		t.Fatalf("record = %+v, %v", rec, err)
	}
	if want := env.clock.Now().Add(2 * time.Hour); !rec.ResetsAt.Equal(want) {
		t.Fatalf("resets_at = %s, want %s", rec.ResetsAt, want)
	}

	env.clock.Advance(time.Hour)
	rec, _ = env.svc.Recheck(ctx, "claudecode")
	if rec.State != StateUsageLimit || env.checker.callCount() != 1 {
		t.Fatalf("held record = %+v after %d checks", rec, env.checker.callCount())
	}
	if env.svc.due(ctx, "claudecode") {
		t.Fatalf("a held usage limit is due for a periodic check")
	}

	env.clock.Advance(time.Hour + time.Minute)
	status, _ := env.svc.Status(ctx, "claudecode")
	if status.State != StateUnknown {
		t.Fatalf("status after the reset = %+v", status)
	}
	if !env.svc.due(ctx, "claudecode") {
		t.Fatalf("a usage limit past its reset is not due")
	}
	rec, _ = env.svc.Recheck(ctx, "claudecode")
	if rec.State != StateOK || env.checker.callCount() != 2 {
		t.Fatalf("after the reset = %+v, %d checks", rec, env.checker.callCount())
	}
}

func TestUsageLimitWithoutResetFollowsTheInterval(t *testing.T) {
	env := newTestEnv(t, "codex")
	ctx := context.Background()
	env.checker.script("codex", failure(framework.SmokeUsageLimit, "usage_limit_exceeded"))
	rec, _ := env.svc.Recheck(ctx, "codex")
	if rec.State != StateUsageLimit || rec.ResetsAt != nil {
		t.Fatalf("record = %+v", rec)
	}
	env.clock.Advance(time.Minute)
	if rec, _ = env.svc.Recheck(ctx, "codex"); rec.State != StateOK {
		t.Fatalf("a usage limit with no known reset was not rechecked: %+v", rec)
	}
}

func TestObserveRunRecordsAndConfirmsWithOneCheck(t *testing.T) {
	env := newTestEnv(t, "claudecode")
	env.enable(t, true)
	ctx := context.Background()

	// A blip: the confirming check passes, so the framework is ok again.
	env.svc.ObserveRun("claudecode", "API Error: 401 Unauthorized · Please run /login")
	waitFor(t, "confirming check", func() bool { return env.checker.callCount() == 1 })
	waitFor(t, "confirmed record", func() bool {
		rec, _ := env.svc.Status(ctx, "claudecode")
		return rec.State == StateOK && rec.Source == SourceRecheck
	})

	// A real failure stays failing after its check.
	env.clock.Advance(time.Minute)
	env.checker.script("claudecode", failure(framework.SmokeUnauthenticated, "Not logged in"))
	env.svc.ObserveRun("claudecode", "Not logged in · Please run /login")
	waitFor(t, "second confirming check", func() bool { return env.checker.callCount() == 2 })
	waitFor(t, "failing record", func() bool {
		rec, _ := env.svc.Status(ctx, "claudecode")
		return rec.State == StateFailing && rec.Failures == 2
	})

	// The work's own failure is not the framework's.
	env.clock.Advance(time.Minute)
	env.svc.ObserveRun("claudecode", "tool Bash failed: curl: (22) The requested URL returned error: 403")
	env.svc.ObserveRun("claudecode", "context canceled")
	env.svc.ObserveRun("claudecode", "API Error: 429 rate_limit_error")
	time.Sleep(20 * time.Millisecond)
	if n := env.checker.callCount(); n != 2 {
		t.Fatalf("work failures ran checks: %d", n)
	}

	// A usage limit is recorded from the run alone; no check spends a request.
	env.svc.ObserveRun("claudecode", "Claude AI usage limit reached|"+itoa(env.clock.Now().Add(3*time.Hour).Unix()))
	waitFor(t, "usage limit record", func() bool {
		rec, _ := env.svc.Status(ctx, "claudecode")
		return rec.State == StateUsageLimit && rec.Source == SourceRun && rec.ResetsAt != nil
	})
	time.Sleep(20 * time.Millisecond)
	if n := env.checker.callCount(); n != 2 {
		t.Fatalf("a usage limit from a run ran a check: %d", n)
	}
}

func TestRecordTestUpdatesRecordsOnlyForPlainTests(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	now := env.clock.Now()
	fail := framework.SmokeResult{Kind: "codex", Model: "gpt-x", ErrorKind: framework.SmokeModelNotFound, ErrorDetail: "unknown model", CheckedAt: now}
	if err := env.svc.RecordTest(ctx, fail, true); err != nil {
		t.Fatal(err)
	}
	if rec, _ := env.svc.Status(ctx, "codex"); rec.State != StateUnknown {
		t.Fatalf("a failed test of a chosen model marked the framework: %+v", rec)
	}
	fail.Model = ""
	_ = env.svc.RecordTest(ctx, fail, false)
	if rec, _ := env.svc.Status(ctx, "codex"); rec.State != StateUnknown {
		t.Fatalf("a failed test in an Agent's environment marked the framework: %+v", rec)
	}
	_ = env.svc.RecordTest(ctx, fail, true)
	if rec, _ := env.svc.Status(ctx, "codex"); rec.State != StateFailing || rec.Source != SourceTest {
		t.Fatalf("plain failed test = %+v", rec)
	}
	pass := framework.SmokeResult{Kind: "codex", Model: "gpt-y", OK: true, CheckedAt: now}
	_ = env.svc.RecordTest(ctx, pass, false)
	if rec, _ := env.svc.Status(ctx, "codex"); rec.State != StateOK || rec.LastOKAt == nil {
		t.Fatalf("passing test = %+v", rec)
	}
	results, _ := env.svc.Results(ctx)
	if len(results) != 3 { // one per framework and model
		t.Fatalf("results = %+v", results)
	}
}

func TestLegacyResultsAreKept(t *testing.T) {
	env := newTestEnv(t, "codex")
	legacy := []framework.SmokeResult{{Kind: "codex", OK: true, CheckedAt: env.clock.Now()}}
	raw, _ := json.Marshal(legacy)
	env.store.data[legacyResultsSetting] = string(raw)
	results, err := env.svc.Results(context.Background())
	if err != nil || len(results) != 1 || results[0].Kind != "codex" {
		t.Fatalf("results = %+v, %v", results, err)
	}
	records, _ := env.svc.Records(context.Background())
	if len(records) != 1 || records[0].State != StateUnknown {
		t.Fatalf("records = %+v", records)
	}
}

func TestSettingsAreNormalized(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	got, _ := env.svc.Settings(ctx)
	if got.Enabled || got.IntervalMinutes != DefaultIntervalMinutes {
		t.Fatalf("defaults = %+v", got)
	}
	got, _ = env.svc.SetSettings(ctx, Settings{Enabled: true, IntervalMinutes: 1})
	if got.IntervalMinutes != MinIntervalMinutes {
		t.Fatalf("clamped = %+v", got)
	}
	fresh := New(Options{Store: env.store})
	if got, _ = fresh.Settings(ctx); !got.Enabled || got.IntervalMinutes != MinIntervalMinutes {
		t.Fatalf("reloaded = %+v", got)
	}
}

func itoa(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestEngineSinkPassesOnlyRunErrors(t *testing.T) {
	env := newTestEnv(t, "claudecode")
	env.enable(t, true)
	sink := env.svc.EngineSink()
	sink(core.HookMessageSent, map[string]string{"runtime_id": "claudecode", "error": "Not logged in"})
	sink(core.HookError, map[string]string{"error": "Not logged in"})
	time.Sleep(20 * time.Millisecond)
	if n := env.checker.callCount(); n != 0 {
		t.Fatalf("non-error events ran checks: %d", n)
	}
	sink(core.HookError, map[string]string{"runtime_id": "claudecode", "error": `exec: "claude": executable file not found in $PATH`})
	waitFor(t, "confirming check", func() bool { return env.checker.callCount() == 1 })
}
