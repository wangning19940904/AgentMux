package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"
)

type fakeKeepAwakeProcess struct {
	mu      sync.Mutex
	started bool
	killed  bool
	done    chan struct{}
	once    sync.Once
}

func newFakeKeepAwakeProcess() *fakeKeepAwakeProcess {
	return &fakeKeepAwakeProcess{done: make(chan struct{})}
}

func (p *fakeKeepAwakeProcess) Start() error {
	p.mu.Lock()
	p.started = true
	p.mu.Unlock()
	return nil
}

func (p *fakeKeepAwakeProcess) Wait() error {
	<-p.done
	return nil
}

func (p *fakeKeepAwakeProcess) Kill() error {
	p.mu.Lock()
	p.killed = true
	p.mu.Unlock()
	p.exit()
	return nil
}

func (p *fakeKeepAwakeProcess) exit() { p.once.Do(func() { close(p.done) }) }

func (p *fakeKeepAwakeProcess) state() (started, killed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.started, p.killed
}

// fakeActivitySimulator models the HID idle timer. When resets is true a
// nudge sets the idle time back to zero; otherwise it is ignored, like a
// system that drops synthetic input.
type fakeActivitySimulator struct {
	mu     sync.Mutex
	idle   time.Duration
	resets bool
	nudges int
	err    error
}

func (s *fakeActivitySimulator) IdleTime() (time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.idle, s.err
}

func (s *fakeActivitySimulator) Nudge() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nudges++
	if s.resets {
		s.idle = 0
	}
	return nil
}

func (s *fakeActivitySimulator) setIdle(idle time.Duration) {
	s.mu.Lock()
	s.idle = idle
	s.mu.Unlock()
}

func (s *fakeActivitySimulator) nudgeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nudges
}

func newTestKeepAwakeManager(now time.Time, command func(KeepAwakeOptions, int) keepAwakeProcess) *keepAwakeManager {
	return &keepAwakeManager{
		supported:        true,
		now:              func() time.Time { return now },
		command:          command,
		activityInterval: time.Hour,
		activityIdle:     30 * time.Second,
	}
}

func TestCaffeinateArgsByMode(t *testing.T) {
	tests := []struct {
		name    string
		options KeepAwakeOptions
		want    []string
	}{
		{
			name:    "display timed",
			options: KeepAwakeOptions{Mode: KeepAwakeModeDisplay, DurationMinutes: 90},
			want:    []string{"/usr/bin/caffeinate", "-d", "-u", "-i", "-s", "-w", "42", "-t", "5400"},
		},
		{
			name:    "system until stopped",
			options: KeepAwakeOptions{Mode: KeepAwakeModeSystem},
			want:    []string{"/usr/bin/caffeinate", "-i", "-s", "-w", "42"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := caffeinateArgs(test.options, 42); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("caffeinate args = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestKeepAwakeManagerStartsReplacesAndStopsAssertion(t *testing.T) {
	now := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.Local)
	var requested []KeepAwakeOptions
	var processes []*fakeKeepAwakeProcess
	manager := newTestKeepAwakeManager(now, func(options KeepAwakeOptions, _ int) keepAwakeProcess {
		requested = append(requested, options)
		process := newFakeKeepAwakeProcess()
		processes = append(processes, process)
		return process
	})

	status, err := manager.Start(KeepAwakeOptions{DurationMinutes: 90})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Supported || !status.Enabled || status.Mode != KeepAwakeModeDisplay || status.DurationMinutes != 90 || status.RemainingSeconds != 90*60 {
		t.Fatalf("started status = %+v", status)
	}
	if started, killed := processes[0].state(); !started || killed {
		t.Fatalf("first process = started %t, killed %t", started, killed)
	}

	status, err = manager.Start(KeepAwakeOptions{Mode: KeepAwakeModeSystem, SimulateActivity: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, killed := processes[0].state(); !killed {
		t.Fatal("replaced process was not stopped")
	}
	if !status.Enabled || status.Mode != KeepAwakeModeSystem || status.DurationMinutes != 0 || status.RemainingSeconds != 0 || status.EndsAt != "" {
		t.Fatalf("until-stopped status = %+v", status)
	}
	if status.SimulateActivity || requested[1].SimulateActivity {
		t.Fatalf("system mode must not simulate activity: status %+v, request %+v", status, requested[1])
	}

	manager.Stop()
	status = manager.Status()
	if status.Enabled {
		t.Fatalf("stopped status = %+v", status)
	}
	if status.Mode != KeepAwakeModeSystem {
		t.Fatalf("stopped status should keep the last mode, got %+v", status)
	}
	if _, killed := processes[1].state(); !killed {
		t.Fatal("active process was not stopped")
	}
}

func TestKeepAwakeManagerClearsStateWhenCaffeinateExits(t *testing.T) {
	process := newFakeKeepAwakeProcess()
	manager := newTestKeepAwakeManager(time.Now(), func(KeepAwakeOptions, int) keepAwakeProcess { return process })
	if _, err := manager.Start(KeepAwakeOptions{DurationMinutes: 1}); err != nil {
		t.Fatal(err)
	}
	process.exit()
	deadline := time.Now().Add(time.Second)
	for manager.Status().Enabled {
		if time.Now().After(deadline) {
			t.Fatal("status still enabled after caffeinate exited")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestKeepAwakeActivitySimulation(t *testing.T) {
	tests := []struct {
		name       string
		idle       time.Duration
		resets     bool
		err        error
		wantState  string
		wantNudges int
	}{
		{name: "user active", idle: 5 * time.Second, resets: true, wantState: keepAwakeActivityWaiting, wantNudges: 0},
		{name: "idle and nudge works", idle: 40 * time.Second, resets: true, wantState: keepAwakeActivityActive, wantNudges: 1},
		{name: "idle and nudge ignored", idle: 40 * time.Second, resets: false, wantState: keepAwakeActivityBlocked, wantNudges: 1},
		{name: "idle time unreadable", err: errors.New("no ioreg"), wantState: keepAwakeActivityError, wantNudges: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			process := newFakeKeepAwakeProcess()
			simulator := &fakeActivitySimulator{idle: test.idle, resets: test.resets, err: test.err}
			manager := newTestKeepAwakeManager(time.Now(), func(KeepAwakeOptions, int) keepAwakeProcess { return process })
			manager.simulator = simulator
			// Run ticks synchronously instead of through the background loop.
			manager.activityInterval = time.Hour
			if _, err := manager.Start(KeepAwakeOptions{SimulateActivity: true}); err != nil {
				t.Fatal(err)
			}
			defer manager.Stop()
			manager.mu.Lock()
			runID := manager.runID
			manager.mu.Unlock()
			// The background loop also runs one immediate tick; wait for it
			// so the counts below are deterministic.
			waitForKeepAwake(t, func() bool {
				return manager.Status().ActivityState != keepAwakeActivityWaiting || test.wantState == keepAwakeActivityWaiting
			})
			simulator.setIdle(test.idle)
			before := simulator.nudgeCount()
			manager.nudgeIfIdle(runID, make(chan struct{}))

			status := manager.Status()
			if status.ActivityState != test.wantState {
				t.Fatalf("activity state = %q, want %q (status %+v)", status.ActivityState, test.wantState, status)
			}
			if got := simulator.nudgeCount() - before; got != test.wantNudges {
				t.Fatalf("nudges = %d, want %d", got, test.wantNudges)
			}
			if (status.LastActivityAt != "") != (test.wantState == keepAwakeActivityActive) {
				t.Fatalf("last activity = %q for state %q", status.LastActivityAt, status.ActivityState)
			}
		})
	}
}

func waitForKeepAwake(t *testing.T, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for keep-awake state")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestKeepAwakeAPIValidatesRequestAndReturnsStatus(t *testing.T) {
	s, _ := newTestServer(t)
	var requested []KeepAwakeOptions
	s.keepAwake = newTestKeepAwakeManager(time.Now(), func(options KeepAwakeOptions, _ int) keepAwakeProcess {
		requested = append(requested, options)
		return newFakeKeepAwakeProcess()
	})
	s.keepAwake.simulator = &fakeActivitySimulator{}
	t.Cleanup(s.keepAwake.Stop)

	rec := doJSON(t, s, http.MethodPut, "/api/v1/system/keep-awake", map[string]any{
		"enabled": true, "duration_minutes": 0, "mode": "display", "simulate_activity": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("put status = %d body = %s", rec.Code, rec.Body.String())
	}
	var status KeepAwakeStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Supported || !status.Enabled || status.DurationMinutes != 0 || !status.SimulateActivity || status.Mode != KeepAwakeModeDisplay {
		t.Fatalf("put response = %+v", status)
	}

	// Legacy shape: a positive duration starts a timed assertion.
	rec = doJSON(t, s, http.MethodPut, "/api/v1/system/keep-awake", map[string]int{"duration_minutes": 45})
	if rec.Code != http.StatusOK {
		t.Fatalf("legacy put status = %d body = %s", rec.Code, rec.Body.String())
	}
	if last := requested[len(requested)-1]; last.DurationMinutes != 45 || last.Mode != KeepAwakeModeDisplay {
		t.Fatalf("legacy request = %+v", last)
	}

	rec = doJSON(t, s, http.MethodGet, "/api/v1/system/keep-awake", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d body = %s", rec.Code, rec.Body.String())
	}

	for _, body := range []any{
		map[string]any{"enabled": true, "duration_minutes": maxKeepAwakeMinutes + 1},
		map[string]any{"enabled": true, "mode": "screensaver"},
		map[string]int{},
	} {
		rec = doJSON(t, s, http.MethodPut, "/api/v1/system/keep-awake", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %v: status = %d body = %s", body, rec.Code, rec.Body.String())
		}
	}

	rec = doJSON(t, s, http.MethodPut, "/api/v1/system/keep-awake", map[string]bool{"enabled": false})
	if rec.Code != http.StatusOK {
		t.Fatalf("stop status = %d body = %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Enabled {
		t.Fatalf("stop response = %+v", status)
	}
}

func TestKeepAwakeIsNoOpWhenUnsupported(t *testing.T) {
	manager := &keepAwakeManager{supported: false, now: time.Now}
	status, err := manager.Start(KeepAwakeOptions{DurationMinutes: 60})
	if err != nil {
		t.Fatal(err)
	}
	if status.Supported || status.Enabled {
		t.Fatalf("unsupported status = %+v", status)
	}
}
