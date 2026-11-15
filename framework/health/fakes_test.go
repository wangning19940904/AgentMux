package health

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/framework"
)

// fakeClock fires After channels only when the test advances it.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []fakeWaiter
}

type fakeWaiter struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 11, 15, 8, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.waiters = append(c.waiters, fakeWaiter{at: c.now.Add(d), ch: ch})
	return ch
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	kept := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			w.ch <- c.now
			continue
		}
		kept = append(kept, w)
	}
	c.waiters = kept
}

func (c *fakeClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiters)
}

// waitFor polls cond until it holds, failing the test after a while.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// memStore is an in-memory Store.
type memStore struct {
	mu   sync.Mutex
	data map[string]string
}

func newMemStore() *memStore { return &memStore{data: map[string]string{}} }

func (m *memStore) GetSetting(_ context.Context, key string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	return v, ok, nil
}

func (m *memStore) SetSetting(_ context.Context, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = value
	return nil
}

// fakeChecker answers checks from a script per kind and records each call.
type fakeChecker struct {
	mu       sync.Mutex
	clock    *fakeClock
	calls    []fakeCall
	answers  map[string][]framework.SmokeResult
	running  int
	maxAtOne int
	// gate, when set, blocks every check until the test sends on it.
	gate chan struct{}
}

type fakeCall struct {
	kind string
	at   time.Time
}

func newFakeChecker(clock *fakeClock) *fakeChecker {
	return &fakeChecker{clock: clock, answers: map[string][]framework.SmokeResult{}}
}

// script queues results for kind; when the queue is empty a check passes.
func (f *fakeChecker) script(kind string, results ...framework.SmokeResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers[kind] = append(f.answers[kind], results...)
}

func (f *fakeChecker) check(ctx context.Context, kind string, _ time.Duration) (framework.SmokeResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{kind: kind, at: f.clock.Now()})
	f.running++
	f.maxAtOne = max(f.maxAtOne, f.running)
	gate := f.gate
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.running--
		f.mu.Unlock()
	}()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return framework.SmokeResult{}, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	result := framework.SmokeResult{Kind: kind, OK: true, Reply: "ok"}
	if queue := f.answers[kind]; len(queue) > 0 {
		result, f.answers[kind] = queue[0], queue[1:]
		result.Kind = kind
	}
	result.CheckedAt = f.clock.Now()
	return result, nil
}

func (f *fakeChecker) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeChecker) callsSnapshot() []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeCall(nil), f.calls...)
}

func failure(kind framework.SmokeErrorKind, detail string) framework.SmokeResult {
	return framework.SmokeResult{ErrorKind: kind, ErrorDetail: detail}
}

type testEnv struct {
	clock   *fakeClock
	checker *fakeChecker
	store   *memStore
	svc     *Service
}

func newTestEnv(t *testing.T, kinds ...string) *testEnv {
	t.Helper()
	clock := newFakeClock()
	checker := newFakeChecker(clock)
	st := newMemStore()
	svc := New(Options{
		Store: st, Check: checker.check, Clock: clock,
		Kinds:      func() []string { return append([]string(nil), kinds...) },
		Registered: func(string) bool { return true },
		CheckGap:   10 * time.Second, Poll: time.Minute, StartDelay: 30 * time.Second,
	})
	return &testEnv{clock: clock, checker: checker, store: st, svc: svc}
}

// start runs the periodic loop until the test ends.
func (e *testEnv) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.svc.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func (e *testEnv) enable(t *testing.T, on bool) {
	t.Helper()
	if _, err := e.svc.SetSettings(context.Background(), Settings{Enabled: on, IntervalMinutes: 30}); err != nil {
		t.Fatal(err)
	}
}
