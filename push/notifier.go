package push

import (
	"sync"
	"time"
)

// clock is time, replaceable in tests.
type clock interface {
	Now() time.Time
	// AfterFunc runs f after d; the returned func cancels it and reports
	// whether it was still pending.
	AfterFunc(d time.Duration, f func()) func() bool
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) AfterFunc(d time.Duration, f func()) func() bool {
	return time.AfterFunc(d, f).Stop
}

// Batch is what one notification says: one event, or a summary of several of
// the same kind.
type Batch struct {
	Kind   string
	Events []Event
	At     time.Time
}

// Render writes the batch in a Console language.
func (b Batch) Render(lang string) Message {
	if len(b.Events) == 1 {
		return b.Events[0].Render(lang)
	}
	return summarize(b.Kind, b.Events, lang, b.At)
}

// URL is the route a tap on this batch opens.
func (b Batch) URL() string { return b.Render("en").URL }

const (
	seenTTL   = 24 * time.Hour
	seenLimit = 4096
)

// Notifier collapses bursts. The first event of a kind is sent at once and
// opens a window; everything else of that kind arriving within the window is
// held, then sent as one notification when the window closes (a summary when
// there are several). While events keep coming, a kind is notified at most
// once per window. An event ID is notified only once.
type Notifier struct {
	window time.Duration
	clock  clock
	emit   func(Batch)

	mu     sync.Mutex
	seen   map[string]time.Time
	bursts map[string]*burst
	closed bool
}

type burst struct {
	pending []Event
	cancel  func() bool
}

func newNotifier(window time.Duration, c clock, emit func(Batch)) *Notifier {
	return &Notifier{window: window, clock: c, emit: emit, seen: map[string]time.Time{}, bursts: map[string]*burst{}}
}

// Publish takes one event.
func (n *Notifier) Publish(e Event) {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return
	}
	now := n.clock.Now()
	if at, ok := n.seen[e.ID]; ok && now.Sub(at) < seenTTL {
		n.mu.Unlock()
		return
	}
	n.remember(e.ID, now)
	if e.At.IsZero() {
		e.At = now
	}
	if b := n.bursts[e.Kind]; b != nil {
		b.pending = replaceTagged(b.pending, e)
		n.mu.Unlock()
		return
	}
	n.bursts[e.Kind] = &burst{cancel: n.clock.AfterFunc(n.window, func() { n.flush(e.Kind) })}
	n.mu.Unlock()
	n.emit(Batch{Kind: e.Kind, Events: []Event{e}, At: e.At})
}

// flush ends a window: what it held goes out as one notification and opens
// the next window; with nothing held, the kind is quiet again.
func (n *Notifier) flush(kind string) {
	n.mu.Lock()
	b := n.bursts[kind]
	if b == nil || n.closed {
		n.mu.Unlock()
		return
	}
	if len(b.pending) == 0 {
		delete(n.bursts, kind)
		n.mu.Unlock()
		return
	}
	batch := Batch{Kind: kind, Events: b.pending, At: n.clock.Now()}
	b.pending = nil
	b.cancel = n.clock.AfterFunc(n.window, func() { n.flush(kind) })
	n.mu.Unlock()
	n.emit(batch)
}

// Close drops whatever is held; nothing more is sent.
func (n *Notifier) Close() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.closed = true
	for kind, b := range n.bursts {
		b.cancel()
		delete(n.bursts, kind)
	}
}

func (n *Notifier) remember(id string, now time.Time) {
	if len(n.seen) >= seenLimit {
		for key, at := range n.seen {
			if now.Sub(at) >= seenTTL {
				delete(n.seen, key)
			}
		}
		// Still full: forget the oldest half rather than grow without bound.
		if len(n.seen) >= seenLimit {
			cutoff := now
			for _, at := range n.seen {
				if at.Before(cutoff) {
					cutoff = at
				}
			}
			cutoff = cutoff.Add(now.Sub(cutoff) / 2)
			for key, at := range n.seen {
				if at.Before(cutoff) {
					delete(n.seen, key)
				}
			}
		}
	}
	n.seen[id] = now
}

// replaceTagged keeps one held event per tag: the latest.
func replaceTagged(pending []Event, e Event) []Event {
	for i := range pending {
		if pending[i].Tag == e.Tag {
			pending[i] = e
			return pending
		}
	}
	return append(pending, e)
}

// Presence turns link connects and disconnects into machine events. A machine
// counts as offline only after staying away for the grace period, so a
// reconnect after a network blip says nothing; "back online" follows only an
// "offline" that was sent.
type Presence struct {
	grace   time.Duration
	clock   clock
	name    func(deviceID string) string
	publish func(Event)

	mu    sync.Mutex
	state map[string]*presenceState
}

type presenceState struct {
	cancel    func() bool
	announced bool
}

func newPresence(grace time.Duration, c clock, name func(string) string, publish func(Event)) *Presence {
	return &Presence{grace: grace, clock: c, name: name, publish: publish, state: map[string]*presenceState{}}
}

// Changed records that a device connected (online) or disconnected.
func (p *Presence) Changed(deviceID string, online bool) {
	p.mu.Lock()
	st := p.state[deviceID]
	if st == nil {
		st = &presenceState{}
		p.state[deviceID] = st
	}
	if !online {
		if st.cancel == nil && !st.announced {
			st.cancel = p.clock.AfterFunc(p.grace, func() { p.expire(deviceID, st) })
		}
		p.mu.Unlock()
		return
	}
	if st.cancel != nil {
		st.cancel()
		st.cancel = nil
		p.mu.Unlock()
		return
	}
	announced := st.announced
	st.announced = false
	p.mu.Unlock()
	if announced {
		p.publish(MachineEvent(deviceID, p.name(deviceID), true, p.clock.Now()))
	}
}

func (p *Presence) expire(deviceID string, st *presenceState) {
	p.mu.Lock()
	if p.state[deviceID] != st || st.cancel == nil {
		p.mu.Unlock()
		return
	}
	st.cancel = nil
	st.announced = true
	p.mu.Unlock()
	p.publish(MachineEvent(deviceID, p.name(deviceID), false, p.clock.Now()))
}

// Forget drops a device that was unpaired, so it is not reported offline.
func (p *Presence) Forget(deviceID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if st := p.state[deviceID]; st != nil && st.cancel != nil {
		st.cancel()
	}
	delete(p.state, deviceID)
}
