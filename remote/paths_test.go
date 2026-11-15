package remote

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

type fakeClock struct{ at time.Time }

func (c *fakeClock) now() time.Time           { return c.at }
func (c *fakeClock) advance(by time.Duration) { c.at = c.at.Add(by) }

func newTestChooser() (*pathChooser, *fakeClock) {
	clock := &fakeClock{at: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)}
	return newPathChooser(clock.now, 0), clock
}

func candidates(paths ...pathCandidate) []pathCandidate { return paths }

func usable(key, via string) pathCandidate {
	return pathCandidate{key: key, via: via, available: true}
}

func expectOrder(t *testing.T, chooser *pathChooser, machine string, paths []pathCandidate, want ...string) {
	t.Helper()
	got, _ := chooser.order(machine, paths)
	if len(want) == 0 {
		want = nil
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestPathChooserPrefersLocalThenDirectThenHub(t *testing.T) {
	chooser, _ := newTestChooser()
	paths := candidates(usable("relay:a", PathRelay), usable("ssh:a", PathSSH), usable("local:", PathLocal), usable("link:a", PathLink))
	expectOrder(t, chooser, "m", paths, "local:", "ssh:a", "link:a", "relay:a")

	// Within a class, a known round trip comes before an unknown one, and
	// the smaller before the larger.
	chooser.measured("link:a", 40*time.Millisecond)
	expectOrder(t, chooser, "m", paths[:2:2], "ssh:a", "relay:a")
	expectOrder(t, chooser, "m2", candidates(usable("ssh:a", PathSSH), usable("link:a", PathLink)), "link:a", "ssh:a")
	chooser.measured("ssh:a", 10*time.Millisecond)
	expectOrder(t, chooser, "m3", candidates(usable("link:a", PathLink), usable("ssh:a", PathSSH)), "ssh:a", "link:a")

	// A path that cannot be tried at all, such as an offline link, is left out.
	expectOrder(t, chooser, "m4", candidates(usable("ssh:a", PathSSH), pathCandidate{key: "link:b", via: PathLink}), "ssh:a")
}

func TestPathChooserKeepsAHealthyActivePath(t *testing.T) {
	chooser, clock := newTestChooser()
	paths := candidates(usable("ssh:a", PathSSH), usable("link:a", PathLink))
	chooser.measured("ssh:a", 50*time.Millisecond)
	chooser.measured("link:a", 45*time.Millisecond)
	expectOrder(t, chooser, "m", paths, "link:a", "ssh:a")
	chooser.connected("m", "link:a")

	// SSH becomes a little faster, but not clearly: 10% or 3 ms is noise.
	chooser.measured("ssh:a", 41*time.Millisecond)
	chooser.measured("link:a", 47*time.Millisecond)
	for range 5 {
		clock.advance(time.Minute)
		expectOrder(t, chooser, "m", paths, "link:a", "ssh:a")
	}

	// 30% faster but by less than 5 ms is not clearly better either.
	chooser2, clock2 := newTestChooser()
	chooser2.measured("ssh:a", 6*time.Millisecond)
	chooser2.measured("link:a", 9*time.Millisecond)
	chooser2.connected("m", "link:a")
	clock2.advance(2 * time.Minute)
	expectOrder(t, chooser2, "m", paths, "link:a", "ssh:a")
	clock2.advance(2 * time.Minute)
	expectOrder(t, chooser2, "m", paths, "link:a", "ssh:a")
}

func TestPathChooserFailsOverAtOnce(t *testing.T) {
	chooser, _ := newTestChooser()
	paths := candidates(usable("local:", PathLocal), usable("ssh:a", PathSSH), usable("relay:a", PathRelay))
	chooser.connected("m", "ssh:a")
	expectOrder(t, chooser, "m", paths, "ssh:a", "local:", "relay:a")

	// The active path fails: the next usable path is first right away, with
	// no waiting for a hold period.
	chooser.failed("ssh:a", errors.New("connection refused"))
	expectOrder(t, chooser, "m", paths, "local:", "relay:a")
	chooser.failed("local:", errors.New("down"))
	expectOrder(t, chooser, "m", paths, "relay:a")
	chooser.connected("m", "relay:a")
	if active := chooser.activePath("m"); active != "relay:a" {
		t.Fatalf("active = %q", active)
	}
	report := chooser.report("ssh:a")
	if !report.checked || report.ok || !report.backingOff || report.err != "connection refused" {
		t.Fatalf("report of a failed path = %+v", report)
	}
}

func TestPathChooserMovesBackExactlyOnceAfterTheHoldPeriod(t *testing.T) {
	chooser, clock := newTestChooser()
	paths := candidates(usable("link:a", PathLink), usable("relay:a", PathRelay))
	chooser.connected("m", "link:a")
	chooser.failed("link:a", errors.New("offline"))
	expectOrder(t, chooser, "m", paths, "relay:a")
	chooser.connected("m", "relay:a")

	// The direct path comes back: a better class, so clearly better, but
	// the relay is kept until that has lasted the hold period.
	clock.advance(3 * time.Second)
	chooser.measured("link:a", 20*time.Millisecond)
	switches := 0
	active := "relay:a"
	for elapsed := time.Duration(0); elapsed <= 3*time.Minute; elapsed += 5 * time.Second {
		order, _ := chooser.order("m", paths)
		if order[0] != active {
			// The caller tries the first path and reports it connected.
			switches++
			if elapsed < defaultPathHold {
				t.Fatalf("switched after %s, before the %s hold", elapsed, defaultPathHold)
			}
			chooser.connected("m", order[0])
			active = order[0]
		}
		clock.advance(5 * time.Second)
	}
	if switches != 1 || active != "link:a" {
		t.Fatalf("switches = %d, active = %q; want exactly one switch to link:a", switches, active)
	}
}

func TestPathChooserRestartsTheHoldWhenTheBetterPathFalters(t *testing.T) {
	chooser, clock := newTestChooser()
	paths := candidates(usable("ssh:a", PathSSH), usable("link:a", PathLink))
	chooser.measured("ssh:a", 100*time.Millisecond)
	chooser.measured("link:a", 100*time.Millisecond)
	chooser.connected("m", "ssh:a")

	chooser.measured("link:a", 20*time.Millisecond)
	expectOrder(t, chooser, "m", paths, "ssh:a", "link:a")
	clock.advance(40 * time.Second)
	// It stops being clearly better for a moment ...
	chooser.measured("link:a", 95*time.Millisecond)
	expectOrder(t, chooser, "m", paths, "ssh:a", "link:a")
	// ... so the next stretch of being better starts the clock again.
	chooser.measured("link:a", 20*time.Millisecond)
	expectOrder(t, chooser, "m", paths, "ssh:a", "link:a")
	clock.advance(40 * time.Second)
	expectOrder(t, chooser, "m", paths, "ssh:a", "link:a")
	clock.advance(25 * time.Second)
	expectOrder(t, chooser, "m", paths, "link:a", "ssh:a")
}

func TestPathChooserBacksOffFailedPaths(t *testing.T) {
	chooser, clock := newTestChooser()
	paths := candidates(usable("ssh:a", PathSSH))
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, time.Minute, time.Minute}
	for _, backoff := range want {
		chooser.failed("ssh:a", errors.New("refused"))
		order, wait := chooser.order("m", paths)
		if order != nil || wait != backoff {
			t.Fatalf("after a failure: order %v, wait %s; want none for %s", order, wait, backoff)
		}
		clock.advance(backoff - time.Millisecond)
		if order, _ := chooser.order("m", paths); order != nil {
			t.Fatalf("tried %v before its %s backoff ended", order, backoff)
		}
		clock.advance(time.Millisecond)
		expectOrder(t, chooser, "m", paths, "ssh:a")
	}
	// Success resets it.
	chooser.measured("ssh:a", 10*time.Millisecond)
	chooser.failed("ssh:a", errors.New("refused"))
	if _, wait := chooser.order("m", paths); wait != 2*time.Second {
		t.Fatalf("backoff after a success = %s, want 2s", wait)
	}
	// Nothing available at all is no wait, not a retry time.
	if order, wait := chooser.order("m", candidates(pathCandidate{key: "link:a", via: PathLink})); order != nil || wait != 0 {
		t.Fatalf("no available path: %v, %s", order, wait)
	}
}

func TestPathChooserDropsAnActivePathThatLeftTheMachine(t *testing.T) {
	chooser, _ := newTestChooser()
	chooser.connected("m", "ssh:a")
	// ssh:a now reaches another computer and is no longer among m's paths.
	expectOrder(t, chooser, "m", candidates(usable("link:a", PathLink)), "link:a")
	chooser.connected("m", "link:a")
	if active := chooser.activePath("m"); active != "link:a" {
		t.Fatalf("active = %q", active)
	}
}
