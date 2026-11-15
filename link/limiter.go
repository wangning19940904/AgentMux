package link

import (
	"net"
	"sync"
	"time"
)

// attemptLimiter counts attempts per source over a sliding window, so a
// source that guesses at a secret is held off for the rest of the window.
// The hub counts every pairing attempt; a node listening for its hub counts
// only the failed ones, so the hub's own redials never lock it out.
type attemptLimiter struct {
	limit int
	span  time.Duration

	mu       sync.Mutex
	attempts map[string][]time.Time
}

const limiterSources = 4096

func newAttemptLimiter(limit int, span time.Duration) *attemptLimiter {
	return &attemptLimiter{limit: limit, span: span, attempts: map[string][]time.Time{}}
}

// recent drops the attempts that left the window. The caller holds mu.
func (l *attemptLimiter) recent(addr string, now time.Time) []time.Time {
	kept := l.attempts[addr][:0]
	for _, at := range l.attempts[addr] {
		if now.Sub(at) < l.span {
			kept = append(kept, at)
		}
	}
	if len(kept) == 0 {
		delete(l.attempts, addr)
		return nil
	}
	l.attempts[addr] = kept
	return kept
}

// blocked reports whether the source used up its attempts in the window.
func (l *attemptLimiter) blocked(addr string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(addr, now)) >= l.limit
}

// record counts one attempt from the source.
func (l *attemptLimiter) record(addr string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.add(addr, l.recent(addr, now), now)
}

// allow counts one attempt, unless the source has none left.
func (l *attemptLimiter) allow(addr string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	recent := l.recent(addr, now)
	if len(recent) >= l.limit {
		return false
	}
	l.add(addr, recent, now)
	return true
}

// add appends an attempt and keeps the number of sources bounded. The caller
// holds mu.
func (l *attemptLimiter) add(addr string, recent []time.Time, now time.Time) {
	l.attempts[addr] = append(recent, now)
	if len(l.attempts) > limiterSources {
		for key, times := range l.attempts {
			if len(times) == 0 || now.Sub(times[len(times)-1]) >= l.span {
				delete(l.attempts, key)
			}
		}
	}
}

// limiterSource is the source an attempt is counted against: the address, or
// for IPv6 its /64, which one machine usually holds whole.
func limiterSource(host string) string {
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() != nil {
		return host
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}
