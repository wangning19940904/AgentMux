package remote

import (
	"sort"
	"sync"
	"time"
)

// Path choice.
//
// One computer can be reachable several ways: from this machine itself, over
// SSH, over its own link to this hub, or through the hub this machine is
// linked to. pathChooser picks one per machine and keeps it:
//
//   - Classes come first: local, then direct (SSH or the machine's link),
//     then through the hub. Within a class, the lower round-trip time wins.
//   - A healthy active path is kept. When it fails, the next usable path is
//     used at once.
//   - The chooser moves back to another path only when it is clearly better
//     (a better class, or the same class with a round-trip time over 30% and
//     at least 5 ms lower) and has stayed so for the hold period.
//   - A path that failed is not tried again for a while: 2 seconds after the
//     first failure, doubling up to a minute.
//
// The chooser only keeps state and decides; the Manager dials and reports
// back what happened. Machines reached one way never get here.

const (
	defaultPathHold = 60 * time.Second
	pathBackoffMin  = 2 * time.Second
	pathBackoffMax  = time.Minute
	// A same-class path must save this much, and over 30%, to be worth
	// moving to.
	pathRTTMargin = 5 * time.Millisecond
)

// pathClass orders the ways of reaching a machine: lower is preferred.
func pathClass(via string) int {
	switch via {
	case PathLocal:
		return 0
	case PathSSH, PathLink:
		return 1
	default:
		return 2
	}
}

// pathCandidate is one path of a machine as the chooser sees it.
type pathCandidate struct {
	// key names the path across calls: its kind and the entry it uses.
	key string
	via string
	// available says whether the path can be tried at all now, as a linked
	// device must be connected. Backoff is the chooser's own business.
	available bool
}

// pathHealth is what the chooser knows of one path.
type pathHealth struct {
	checked   bool
	ok        bool
	rtt       time.Duration
	fails     int
	failUntil time.Time
	err       string
}

type pendingSwitch struct {
	key   string
	since time.Time
}

type pathChooser struct {
	now  func() time.Time
	hold time.Duration

	mu     sync.Mutex
	health map[string]*pathHealth
	// active is each machine's path in use, by machine key.
	active map[string]string
	// better is a path clearly better than the active one, and since when
	// it has been, by machine key.
	better map[string]pendingSwitch
}

func newPathChooser(now func() time.Time, hold time.Duration) *pathChooser {
	if now == nil {
		now = time.Now
	}
	if hold <= 0 {
		hold = defaultPathHold
	}
	return &pathChooser{
		now: now, hold: hold,
		health: map[string]*pathHealth{}, active: map[string]string{}, better: map[string]pendingSwitch{},
	}
}

// order returns the paths to try for a machine now, best first: the active
// path while it is usable (or the path to move to once the hold period is
// over), then the other usable paths. Paths that are unavailable or backing
// off are left out. When nothing is usable, wait is how long until the first
// path's backoff ends, or zero when no path is available at all.
func (c *pathChooser) order(machine string, candidates []pathCandidate) (keys []string, wait time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	var usable []pathCandidate
	for _, candidate := range candidates {
		if !candidate.available {
			continue
		}
		health := c.health[candidate.key]
		if health != nil && now.Before(health.failUntil) {
			if left := health.failUntil.Sub(now); wait == 0 || left < wait {
				wait = left
			}
			continue
		}
		usable = append(usable, candidate)
	}
	if len(usable) == 0 {
		return nil, wait
	}
	sort.SliceStable(usable, func(i, j int) bool { return c.before(usable[i], usable[j]) })

	activeKey := c.active[machine]
	active := -1
	for index, candidate := range usable {
		if candidate.key == activeKey {
			active = index
		}
	}
	if active < 0 {
		// No path in use, or it stopped being usable: the best usable one
		// is tried first, and the first that works becomes active.
		delete(c.better, machine)
		return candidateKeys(usable), 0
	}
	first := usable[active]
	// Is there a path worth leaving the active one for, and has it been so
	// long enough?
	var challenger *pathCandidate
	for index := range usable {
		if index != active && c.clearlyBetter(usable[index], usable[active]) {
			challenger = &usable[index]
			break // usable is sorted, so this is the best of them
		}
	}
	switch pending, ok := c.better[machine]; {
	case challenger == nil:
		delete(c.better, machine)
	case !ok || pending.key != challenger.key:
		c.better[machine] = pendingSwitch{key: challenger.key, since: now}
	case now.Sub(pending.since) >= c.hold:
		first = *challenger
	}
	keys = []string{first.key}
	for _, candidate := range usable {
		if candidate.key != first.key {
			keys = append(keys, candidate.key)
		}
	}
	return keys, 0
}

func candidateKeys(candidates []pathCandidate) []string {
	keys := make([]string, len(candidates))
	for index, candidate := range candidates {
		keys[index] = candidate.key
	}
	return keys
}

// before orders usable paths: class, then a known round trip before an
// unknown one, then the smaller.
func (c *pathChooser) before(a, b pathCandidate) bool {
	if pathClass(a.via) != pathClass(b.via) {
		return pathClass(a.via) < pathClass(b.via)
	}
	ra, rb := c.rtt(a.key), c.rtt(b.key)
	if (ra > 0) != (rb > 0) {
		return ra > 0
	}
	return ra < rb
}

// clearlyBetter says q is worth leaving p for.
func (c *pathChooser) clearlyBetter(q, p pathCandidate) bool {
	if pathClass(q.via) != pathClass(p.via) {
		return pathClass(q.via) < pathClass(p.via)
	}
	rq, rp := c.rtt(q.key), c.rtt(p.key)
	return rq > 0 && rp > 0 && rq*10 < rp*7 && rp-rq >= pathRTTMargin
}

func (c *pathChooser) rtt(key string) time.Duration {
	if health := c.health[key]; health != nil {
		return health.rtt
	}
	return 0
}

func (c *pathChooser) entry(key string) *pathHealth {
	health := c.health[key]
	if health == nil {
		health = &pathHealth{}
		c.health[key] = health
	}
	return health
}

// connected records that machine was reached through key, which becomes its
// active path.
func (c *pathChooser) connected(machine, key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	health := c.entry(key)
	health.checked, health.ok, health.fails, health.failUntil, health.err = true, true, 0, time.Time{}, ""
	if c.active[machine] != key {
		c.active[machine] = key
		delete(c.better, machine)
	}
}

// measured records a round trip through key that worked. Noise does not
// count as a change, so paths do not trade places over a millisecond.
func (c *pathChooser) measured(key string, rtt time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	health := c.entry(key)
	health.checked, health.ok, health.fails, health.failUntil, health.err = true, true, 0, time.Time{}, ""
	if rtt <= 0 {
		rtt = time.Microsecond
	}
	diff := rtt - health.rtt
	if diff < 0 {
		diff = -diff
	}
	if health.rtt == 0 || diff > max(2*time.Millisecond, health.rtt/5) {
		health.rtt = rtt
	}
}

// failed records that key did not work. It is not tried again until its
// backoff ends.
func (c *pathChooser) failed(key string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	health := c.entry(key)
	health.checked, health.ok = true, false
	health.fails = min(health.fails+1, 16)
	health.failUntil = c.now().Add(pathBackoff(health.fails))
	if err != nil {
		health.err = err.Error()
	}
}

// pathBackoff is how long a path waits after its n-th failure in a row.
func pathBackoff(fails int) time.Duration {
	wait := pathBackoffMin
	for range fails - 1 {
		wait *= 2
		if wait >= pathBackoffMax {
			return pathBackoffMax
		}
	}
	return wait
}

// pathReport is what the chooser says about one path, for the machine list.
type pathReport struct {
	checked, ok, backingOff bool
	rtt                     time.Duration
	err                     string
}

func (c *pathChooser) report(key string) pathReport {
	c.mu.Lock()
	defer c.mu.Unlock()
	health := c.health[key]
	if health == nil {
		return pathReport{}
	}
	return pathReport{
		checked: health.checked, ok: health.ok, backingOff: c.now().Before(health.failUntil),
		rtt: health.rtt, err: health.err,
	}
}

// activePath is the machine's path in use, or "".
func (c *pathChooser) activePath(machine string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active[machine]
}
