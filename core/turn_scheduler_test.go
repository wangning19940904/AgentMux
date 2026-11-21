package core

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTurnClock is a turnClock whose time moves only when a test advances it.
type fakeTurnClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTurnTimer
}

type fakeTurnTimer struct {
	at      time.Time
	f       func()
	stopped bool
	fired   bool
}

func newFakeTurnClock() *fakeTurnClock {
	return &fakeTurnClock{now: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)}
}

func (c *fakeTurnClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeTurnClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &fakeTurnTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, timer)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		active := !timer.stopped && !timer.fired
		timer.stopped = true
		return active
	}
}

// Advance moves the clock and runs the timers that are due, outside the
// clock's lock.
func (c *fakeTurnClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []func()
	for _, timer := range c.timers {
		if !timer.stopped && !timer.fired && !timer.at.After(c.now) {
			timer.fired = true
			due = append(due, timer.f)
		}
	}
	c.mu.Unlock()
	for _, f := range due {
		f()
	}
}

func newTestTurnScheduler(t *testing.T, settings TurnSchedulerSettings, opts TurnSchedulerOptions) (*TurnScheduler, *fakeTurnClock) {
	t.Helper()
	clock := newFakeTurnClock()
	opts.Settings = settings
	opts.clock = clock
	opts.Log = slog.New(slog.DiscardHandler)
	return NewTurnScheduler(opts), clock
}

func on(class TurnClass, id string) TurnRequest {
	return TurnRequest{RuntimeID: "claudecode", Class: class, ConversationID: id}
}

// admitNow admits a turn that must start at once.
func admitNow(t *testing.T, s *TurnScheduler, req TurnRequest) *TurnLease {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	lease, err := s.Admit(ctx, req)
	if err != nil {
		t.Fatalf("admit %s %s: %v", req.Class, req.ConversationID, err)
	}
	if lease == nil {
		t.Fatalf("admit %s %s: no lease", req.Class, req.ConversationID)
	}
	return lease
}

type pendingTurn struct {
	id     string
	cancel context.CancelFunc
	done   chan struct{}
	lease  *TurnLease
	err    error
}

// admitLater starts a turn's admission in the background and waits until it
// is queued, so the order turns arrive in is the order of calls.
func admitLater(t *testing.T, s *TurnScheduler, req TurnRequest) *pendingTurn {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	turn := &pendingTurn{id: req.ConversationID, cancel: cancel, done: make(chan struct{})}
	before := queuedCount(s)
	go func() {
		defer close(turn.done)
		turn.lease, turn.err = s.Admit(ctx, req)
	}()
	waitTurn(t, "turn "+req.ConversationID+" queued", func() bool {
		select {
		case <-turn.done:
			return true
		default:
		}
		return queuedCount(s) > before
	})
	return turn
}

func (p *pendingTurn) admitted() bool {
	select {
	case <-p.done:
		return p.err == nil && p.lease != nil
	default:
		return false
	}
}

func (p *pendingTurn) wait(t *testing.T) *TurnLease {
	t.Helper()
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		t.Fatalf("turn %s was not admitted", p.id)
	}
	if p.err != nil {
		t.Fatalf("turn %s: %v", p.id, p.err)
	}
	return p.lease
}

func queuedCount(s *TurnScheduler) int {
	count := 0
	for _, account := range s.Snapshot().Accounts {
		count += len(account.Queued)
	}
	return count
}

func waitTurn(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// assertWaiting checks that a turn is still queued after a moment.
func assertWaiting(t *testing.T, turns ...*pendingTurn) {
	t.Helper()
	time.Sleep(20 * time.Millisecond)
	for _, turn := range turns {
		if turn.admitted() {
			t.Fatalf("turn %s was admitted, want it to wait", turn.id)
		}
	}
}

func onlyAccount(t *testing.T, s *TurnScheduler) TurnAccountSnapshot {
	t.Helper()
	accounts := s.Snapshot().Accounts
	if len(accounts) != 1 {
		t.Fatalf("accounts = %+v, want one", accounts)
	}
	return accounts[0]
}

func TestTurnSchedulerPriorityThenArrivalOrder(t *testing.T) {
	s, _ := newTestTurnScheduler(t, TurnSchedulerSettings{Enabled: true, MaxConcurrent: 1}, TurnSchedulerOptions{})
	running := admitNow(t, s, on(TurnTicket, "running"))

	// Arrival order mixes classes; admission follows class, then arrival.
	waiting := []*pendingTurn{
		admitLater(t, s, on(TurnBackground, "b1")),
		admitLater(t, s, on(TurnScheduled, "s1")),
		admitLater(t, s, on(TurnTicket, "t1")),
		admitLater(t, s, on(TurnScheduled, "s2")),
		admitLater(t, s, on(TurnTicket, "t2")),
		admitLater(t, s, on(TurnInteractive, "i1")),
	}
	assertWaiting(t, waiting...)
	if got := len(onlyAccount(t, s).Queued); got != len(waiting) {
		t.Fatalf("queued = %d, want %d", got, len(waiting))
	}

	var order []string
	lease := running
	for range waiting {
		lease.Done("")
		var next *pendingTurn
		waitTurn(t, "the next admission", func() bool {
			for _, turn := range waiting {
				if turn.admitted() && !slices.Contains(order, turn.id) {
					next = turn
					return true
				}
			}
			return false
		})
		order = append(order, next.id)
		lease = next.lease
		// Only one place: nobody else may start meanwhile.
		admittedNow := 0
		for _, turn := range waiting {
			if turn.admitted() {
				admittedNow++
			}
		}
		if admittedNow != len(order) {
			t.Fatalf("%d turns admitted after %v, want %d", admittedNow, order, len(order))
		}
	}
	lease.Done("")
	want := []string{"i1", "t1", "t2", "s1", "s2", "b1"}
	if !slices.Equal(order, want) {
		t.Fatalf("admission order = %v, want %v", order, want)
	}
	if accounts := s.Snapshot().Accounts; len(accounts) != 0 {
		t.Fatalf("idle accounts kept: %+v", accounts)
	}
}

func TestTurnSchedulerLimitAndInteractiveHeadroom(t *testing.T) {
	s, _ := newTestTurnScheduler(t, TurnSchedulerSettings{Enabled: true, MaxConcurrent: 2, InteractiveHeadroom: 1}, TurnSchedulerOptions{})
	t1 := admitNow(t, s, on(TurnTicket, "t1"))
	admitNow(t, s, on(TurnScheduled, "s1"))
	t3 := admitLater(t, s, on(TurnTicket, "t3"))
	assertWaiting(t, t3)

	// A person still gets the headroom place, on top of the limit.
	admitNow(t, s, on(TurnInteractive, "i1"))
	i2 := admitLater(t, s, on(TurnInteractive, "i2"))
	assertWaiting(t, i2)
	account := onlyAccount(t, s)
	if account.Running != 3 || account.Limit != 2 || account.RunningByClass["ticket"] != 1 || account.RunningByClass["interactive"] != 1 {
		t.Fatalf("account = %+v", account)
	}

	// Another framework, or another sign-in of the same one, is its own
	// account.
	admitNow(t, s, TurnRequest{RuntimeID: "codex", Class: TurnTicket})
	admitNow(t, s, TurnRequest{RuntimeID: "claudecode", Profile: AccountProfile(map[string]string{"CLAUDE_CONFIG_DIR": "/tmp/other"}), Class: TurnTicket})

	// A freed place goes to the waiting person first; the Ticket waits
	// because three turns still run, above its limit of two.
	t1.Done("")
	i2.wait(t)
	assertWaiting(t, t3)

	// Raising the limit starts what it allows at once.
	s.Configure(TurnSchedulerSettings{Enabled: true, MaxConcurrent: 4, InteractiveHeadroom: 1})
	t3.wait(t)
}

func TestTurnSchedulerOffAdmitsEverything(t *testing.T) {
	s, _ := newTestTurnScheduler(t, DefaultTurnSchedulerSettings(), TurnSchedulerOptions{
		Limits: func(string) (time.Time, bool) { return time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), true },
	})
	for index := range 10 {
		admitNow(t, s, on(TurnClass(index%turnClassCount), "t"))
	}
	if account := onlyAccount(t, s); account.Running != 10 || account.HeldUntil != nil {
		t.Fatalf("account = %+v, want ten running and no hold", account)
	}
	var nilScheduler *TurnScheduler
	if lease, err := nilScheduler.Admit(context.Background(), on(TurnTicket, "x")); lease != nil || err != nil {
		t.Fatalf("nil scheduler = %v, %v", lease, err)
	}
}

func usageLimitClassifier(text string, now time.Time) (time.Time, bool) {
	if !strings.Contains(text, "usage limit") {
		return time.Time{}, false
	}
	if strings.Contains(text, "resets in an hour") {
		return now.Add(time.Hour), true
	}
	return time.Time{}, true
}

func TestTurnSchedulerHoldsAtUsageLimitUntilReset(t *testing.T) {
	s, clock := newTestTurnScheduler(t, TurnSchedulerSettings{Enabled: true, MaxConcurrent: 4}, TurnSchedulerOptions{Classify: usageLimitClassifier})
	worker := admitNow(t, s, on(TurnTicket, "worker"))
	worker.Done("You've hit your usage limit; resets in an hour")
	account := onlyAccount(t, s)
	if account.HeldUntil == nil || !account.HeldUntil.Equal(clock.Now().Add(time.Hour)) {
		t.Fatalf("held until %v, want an hour from now", account.HeldUntil)
	}

	ticket := admitLater(t, s, on(TurnTicket, "ticket"))
	scheduled := admitLater(t, s, on(TurnScheduled, "cron"))
	assertWaiting(t, ticket, scheduled)
	for _, waiter := range onlyAccount(t, s).Queued {
		if !waiter.Held {
			t.Fatalf("waiter %+v not shown as held", waiter)
		}
	}
	// A person is not held: they get the framework's own message, and
	// nothing is moved for them.
	chat := admitNow(t, s, on(TurnInteractive, "chat"))
	chat.Done("usage limit reached")
	assertWaiting(t, ticket, scheduled)

	clock.Advance(59 * time.Minute)
	assertWaiting(t, ticket, scheduled)
	clock.Advance(time.Minute)
	ticket.wait(t).Done("")
	scheduled.wait(t).Done("")
	if accounts := s.Snapshot().Accounts; len(accounts) != 0 {
		t.Fatalf("accounts after reset = %+v", accounts)
	}
}

func TestTurnSchedulerHoldWithoutKnownResetAndEarlyRelease(t *testing.T) {
	s, clock := newTestTurnScheduler(t, TurnSchedulerSettings{Enabled: true}, TurnSchedulerOptions{Classify: usageLimitClassifier})
	admitNow(t, s, on(TurnTicket, "worker")).Done("usage limit reached")
	held := onlyAccount(t, s).HeldUntil
	if held == nil || !held.Equal(clock.Now().Add(defaultUsageHold)) {
		t.Fatalf("held until %v, want the default hold", held)
	}
	ticket := admitLater(t, s, on(TurnTicket, "ticket"))
	assertWaiting(t, ticket)

	// A person's turn that finishes cleanly shows the limit is gone.
	admitNow(t, s, on(TurnInteractive, "chat")).Done("")
	ticket.wait(t).Done("")

	// An error that is not a usage limit holds nothing.
	admitNow(t, s, on(TurnTicket, "other")).Done("exit status 1")
	if account := onlyAccount(t, s); account.HeldUntil != nil || account.Running != 0 {
		t.Fatalf("account = %+v, want it idle and not held", account)
	}
}

func TestTurnSchedulerHoldsFromFrameworkHealth(t *testing.T) {
	var mu sync.Mutex
	clock := newFakeTurnClock()
	resetsAt := clock.Now().Add(2 * time.Hour)
	limited := true
	s := NewTurnScheduler(TurnSchedulerOptions{
		Settings: TurnSchedulerSettings{Enabled: true}, clock: clock, Log: slog.New(slog.DiscardHandler),
		Limits: func(kind string) (time.Time, bool) {
			mu.Lock()
			defer mu.Unlock()
			return resetsAt, limited && kind == "claudecode"
		},
	})
	ticket := admitLater(t, s, on(TurnTicket, "ticket"))
	assertWaiting(t, ticket)
	// Health speaks for the default sign-in only, and for its own kind.
	admitNow(t, s, TurnRequest{RuntimeID: "claudecode", Profile: "abc", Class: TurnTicket})
	admitNow(t, s, TurnRequest{RuntimeID: "codex", Class: TurnTicket})

	// At the reset health learns of a later one: the hold is extended.
	mu.Lock()
	resetsAt = resetsAt.Add(time.Hour)
	mu.Unlock()
	clock.Advance(2 * time.Hour)
	assertWaiting(t, ticket)

	mu.Lock()
	limited = false
	mu.Unlock()
	clock.Advance(time.Hour)
	ticket.wait(t)
}

func TestTurnSchedulerCancelWhileQueued(t *testing.T) {
	s, _ := newTestTurnScheduler(t, TurnSchedulerSettings{Enabled: true, MaxConcurrent: 1}, TurnSchedulerOptions{Classify: usageLimitClassifier})
	running := admitNow(t, s, on(TurnTicket, "running"))
	first := admitLater(t, s, on(TurnTicket, "first"))
	second := admitLater(t, s, on(TurnTicket, "second"))

	first.cancel()
	<-first.done
	if !errors.Is(first.err, context.Canceled) || first.lease != nil {
		t.Fatalf("cancelled turn = %v, %v", first.lease, first.err)
	}
	var waitErr *TurnWaitError
	if !errors.As(first.err, &waitErr) || waitErr.Account != "claudecode" || !waitErr.HeldUntil.IsZero() {
		t.Fatalf("wait error = %#v", first.err)
	}
	if queued := onlyAccount(t, s).Queued; len(queued) != 1 || queued[0].ConversationID != "second" {
		t.Fatalf("queued = %+v, want only second", queued)
	}
	running.Done("")
	second.wait(t).Done("")

	// A turn that gives up while the account is held says what it waited for.
	admitNow(t, s, on(TurnTicket, "limited")).Done("usage limit resets in an hour")
	held := admitLater(t, s, on(TurnTicket, "held"))
	held.cancel()
	<-held.done
	if !errors.As(held.err, &waitErr) || waitErr.HeldUntil.IsZero() || !strings.Contains(held.err.Error(), "usage limit to reset") {
		t.Fatalf("held wait error = %v", held.err)
	}
	if account := onlyAccount(t, s); len(account.Queued) != 0 || account.Running != 0 {
		t.Fatalf("account = %+v", account)
	}

	// A context that already ended is never queued.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Admit(ctx, on(TurnInteractive, "late")); !errors.Is(err, context.Canceled) {
		t.Fatalf("admit with ended context = %v", err)
	}
}

// A Ticket's worker that holds the account's only place and waits on its own
// brain (or on a helper it started) must not deadlock: the awaited turn is
// admitted past the limit.
func TestTurnSchedulerAwaitedTurnsDoNotDeadlock(t *testing.T) {
	s, clock := newTestTurnScheduler(t, TurnSchedulerSettings{Enabled: true, MaxConcurrent: 1, InteractiveHeadroom: 1}, TurnSchedulerOptions{Classify: usageLimitClassifier})
	worker := admitNow(t, s, on(TurnTicket, "worker"))
	queuedTicket := admitLater(t, s, on(TurnTicket, "next-ticket"))

	brainCtx := WithAwaitedTurn(WithTurnClass(context.Background(), TurnTicket))
	brain := admitNow(t, s, turnRequestFor(brainCtx, map[string]string{"runtime_id": "claudecode", "conversation_id": "brain"}))
	helper := admitNow(t, s, TurnRequest{RuntimeID: "claudecode", Class: TurnTicket, Awaited: true, ConversationID: "helper"})
	if account := onlyAccount(t, s); account.Running != 3 {
		t.Fatalf("running = %d, want the worker, its brain and its helper", account.Running)
	}
	brain.Done("")
	helper.Done("")
	assertWaiting(t, queuedTicket)
	worker.Done("")
	queuedTicket.wait(t).Done("")

	// An awaited turn still waits out a usage limit, and is the first to
	// start at the reset.
	blocker := admitNow(t, s, on(TurnTicket, "blocker"))
	admitNow(t, s, on(TurnInteractive, "chat")).Done("usage limit resets in an hour")
	plain := admitLater(t, s, on(TurnTicket, "plain"))
	awaited := admitLater(t, s, TurnRequest{RuntimeID: "claudecode", Class: TurnTicket, Awaited: true, ConversationID: "awaited"})
	assertWaiting(t, plain, awaited)
	clock.Advance(time.Hour)
	awaited.wait(t).Done("")
	assertWaiting(t, plain) // the blocker still holds the only place
	blocker.Done("")
	plain.wait(t).Done("")
}

func TestTurnRequestClassFromContextAndOrigin(t *testing.T) {
	cases := []struct {
		ctx    context.Context
		origin string
		want   TurnClass
	}{
		{context.Background(), OriginChannel, TurnInteractive},
		{context.Background(), OriginAPI, TurnInteractive},
		{context.Background(), "", TurnInteractive},
		{context.Background(), OriginCron, TurnScheduled},
		{context.Background(), OriginWebhook, TurnScheduled},
		{WithTurnClass(context.Background(), TurnBackground), OriginChannel, TurnBackground},
	}
	for _, tc := range cases {
		req := turnRequestFor(tc.ctx, map[string]string{"origin": tc.origin, "runtime_id": "codex", "account_profile": "p"})
		if req.Class != tc.want || req.account() != "codex#p" || req.Awaited {
			t.Fatalf("origin %q: request = %+v", tc.origin, req)
		}
	}
	for _, class := range []TurnClass{TurnInteractive, TurnTicket, TurnScheduled, TurnBackground} {
		if parsed, ok := ParseTurnClass(" " + strings.ToUpper(class.String())); !ok || parsed != class {
			t.Fatalf("ParseTurnClass(%s) = %v, %v", class, parsed, ok)
		}
	}
	if _, ok := ParseTurnClass("urgent"); ok {
		t.Fatal("unknown class parsed")
	}
	if AccountProfile(nil) != "" || AccountProfile(map[string]string{"PATH": "/bin"}) != "" {
		t.Fatal("env without a sign-in selector has a profile")
	}
	a := AccountProfile(map[string]string{"CODEX_HOME": "/a"})
	if a == "" || a == AccountProfile(map[string]string{"CODEX_HOME": "/b"}) || strings.Contains(a, "/a") {
		t.Fatalf("profile = %q", a)
	}
}

// gatedSession sends its events only when the test releases them.
type gatedSession struct {
	release chan struct{}
	events  []*Event
}

func (s *gatedSession) ID() string { return "gated" }
func (s *gatedSession) Send(ctx context.Context, text string) (<-chan *Event, error) {
	out := make(chan *Event, len(s.events))
	go func() {
		defer close(out)
		<-s.release
		for _, event := range s.events {
			out <- event
		}
	}()
	return out, nil
}
func (s *gatedSession) RespondPermission(context.Context, bool) error { return nil }
func (s *gatedSession) Close(context.Context) error                   { return nil }

// The engine holds a turn's place from its send until its events end, and a
// usage-limit error in them holds the account.
func TestEngineAdmitsTurnsThroughScheduler(t *testing.T) {
	engine := NewEngine(slog.New(slog.DiscardHandler), nil)
	scheduler, clock := newTestTurnScheduler(t, TurnSchedulerSettings{Enabled: true, MaxConcurrent: 1}, TurnSchedulerOptions{Classify: usageLimitClassifier})
	engine.SetTurnScheduler(scheduler)
	ticketCtx := WithTurnClass(context.Background(), TurnTicket)
	data := func() map[string]string { return map[string]string{"runtime_id": "claudecode"} }

	first := &gatedSession{release: make(chan struct{}), events: []*Event{{Type: EventError, Err: errors.New("usage limit; resets in an hour")}}}
	events, err := engine.observeSend(ticketCtx, first, "one", data())
	if err != nil {
		t.Fatal(err)
	}
	secondStarted := make(chan struct{})
	go func() {
		defer close(secondStarted)
		second := &gatedSession{release: make(chan struct{})}
		close(second.release)
		if events, err := engine.observeSend(ticketCtx, second, "two", data()); err == nil {
			for range events {
			}
		}
	}()
	select {
	case <-secondStarted:
		t.Fatal("second turn started while the first held the only place")
	case <-time.After(20 * time.Millisecond):
	}
	close(first.release)
	for range events {
	}
	// The first turn's usage limit now holds the second.
	select {
	case <-secondStarted:
		t.Fatal("second turn started while the account was held")
	case <-time.After(20 * time.Millisecond):
	}
	clock.Advance(time.Hour)
	select {
	case <-secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("second turn did not start at the reset")
	}
}
