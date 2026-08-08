package server

import (
	"encoding/json"
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
	p.once.Do(func() { close(p.done) })
	return nil
}

func (p *fakeKeepAwakeProcess) state() (started, killed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.started, p.killed
}

func TestCaffeinateCommandPreventsIdleDisplayAndSystemSleep(t *testing.T) {
	process := newCaffeinateProcess(90).(*caffeinateProcess)
	want := []string{"/usr/bin/caffeinate", "-d", "-i", "-u", "-t", "90"}
	if !reflect.DeepEqual(process.command.Args, want) {
		t.Fatalf("caffeinate args = %#v, want %#v", process.command.Args, want)
	}
}

func TestKeepAwakeManagerStartsReplacesAndStopsAssertion(t *testing.T) {
	now := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.Local)
	var seconds []int64
	var processes []*fakeKeepAwakeProcess
	manager := &keepAwakeManager{
		supported: true,
		now:       func() time.Time { return now },
		command: func(duration int64) keepAwakeProcess {
			seconds = append(seconds, duration)
			process := newFakeKeepAwakeProcess()
			processes = append(processes, process)
			return process
		},
	}

	status, err := manager.Set(90)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Supported || !status.Enabled || status.DurationMinutes != 90 || status.RemainingSeconds != 90*60 {
		t.Fatalf("started status = %+v", status)
	}
	if started, killed := processes[0].state(); !started || killed {
		t.Fatalf("first process = started %t, killed %t", started, killed)
	}

	status, err = manager.Set(30)
	if err != nil {
		t.Fatal(err)
	}
	if _, killed := processes[0].state(); !killed {
		t.Fatal("replaced process was not stopped")
	}
	if !status.Enabled || status.DurationMinutes != 30 || !reflect.DeepEqual(seconds, []int64{5400, 1800}) {
		t.Fatalf("replacement status = %+v, durations = %#v", status, seconds)
	}

	status, err = manager.Set(0)
	if err != nil {
		t.Fatal(err)
	}
	if status.Enabled || status.DurationMinutes != 0 {
		t.Fatalf("stopped status = %+v", status)
	}
	if _, killed := processes[1].state(); !killed {
		t.Fatal("active process was not stopped")
	}
}

func TestKeepAwakeAPIValidatesDurationAndReturnsStatus(t *testing.T) {
	s, _ := newTestServer(t)
	process := newFakeKeepAwakeProcess()
	s.keepAwake = &keepAwakeManager{
		supported: true,
		now:       time.Now,
		command:   func(int64) keepAwakeProcess { return process },
	}
	t.Cleanup(s.keepAwake.Stop)

	rec := doJSON(t, s, http.MethodPut, "/api/v1/system/keep-awake", map[string]int{"duration_minutes": 45})
	if rec.Code != http.StatusOK {
		t.Fatalf("put status = %d body = %s", rec.Code, rec.Body.String())
	}
	var status KeepAwakeStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Supported || !status.Enabled || status.DurationMinutes != 45 {
		t.Fatalf("put response = %+v", status)
	}

	rec = doJSON(t, s, http.MethodGet, "/api/v1/system/keep-awake", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d body = %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPut, "/api/v1/system/keep-awake", map[string]int{"duration_minutes": maxKeepAwakeMinutes + 1})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid duration status = %d body = %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPut, "/api/v1/system/keep-awake", map[string]int{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing duration status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestKeepAwakeIsNoOpWhenUnsupported(t *testing.T) {
	manager := &keepAwakeManager{supported: false, now: time.Now}
	status, err := manager.Set(60)
	if err != nil {
		t.Fatal(err)
	}
	if status.Supported || status.Enabled {
		t.Fatalf("unsupported status = %+v", status)
	}
}
