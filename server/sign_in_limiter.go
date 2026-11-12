package server

import (
	"sync"
	"time"
)

// signInLimiter slows down password guessing. Failures are counted in three
// windows: one login from one address, one address across logins, and one
// login across addresses. The narrow window locks quickly; the wide ones stop
// spraying and distributed guessing without letting a stranger lock a person
// out from a single address.
type signInLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[string]*signInFailures
}

type signInFailures struct {
	count       int
	windowStart time.Time
	lockedUntil time.Time
}

type signInRule struct {
	prefix string
	limit  int
}

const (
	signInWindow     = 15 * time.Minute
	signInLockout    = 15 * time.Minute
	signInMaxEntries = 10000
)

var signInRules = []signInRule{
	{prefix: "pair:", limit: 5},
	{prefix: "addr:", limit: 20},
	{prefix: "login:", limit: 50},
}

func newSignInLimiter() *signInLimiter {
	return &signInLimiter{now: time.Now, entries: map[string]*signInFailures{}}
}

func signInKeys(login, addr string) []string {
	return []string{"pair:" + login + "|" + addr, "addr:" + addr, "login:" + login}
}

// retryAfter reports how long a sign-in for login from addr must wait; zero
// means it may proceed.
func (l *signInLimiter) retryAfter(login, addr string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	var wait time.Duration
	for _, key := range signInKeys(login, addr) {
		if entry := l.entries[key]; entry != nil && now.Before(entry.lockedUntil) {
			if remaining := entry.lockedUntil.Sub(now); remaining > wait {
				wait = remaining
			}
		}
	}
	return wait
}

// failure records a failed sign-in.
func (l *signInLimiter) failure(login, addr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.entries) >= signInMaxEntries {
		l.pruneLocked(now)
	}
	for index, key := range signInKeys(login, addr) {
		entry := l.entries[key]
		if entry == nil || now.Sub(entry.windowStart) >= signInWindow {
			entry = &signInFailures{windowStart: now, lockedUntil: entry.lockedUntilOrZero()}
			l.entries[key] = entry
		}
		entry.count++
		if entry.count >= signInRules[index].limit {
			entry.lockedUntil = now.Add(signInLockout)
			entry.count, entry.windowStart = 0, now
		}
	}
}

// success clears the failures of one login from one address. The wider
// counters stay, so a correct guess on one account does not reset a spray.
func (l *signInLimiter) success(login, addr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, signInKeys(login, addr)[0])
}

func (l *signInLimiter) pruneLocked(now time.Time) {
	for key, entry := range l.entries {
		if now.Sub(entry.windowStart) >= signInWindow && !now.Before(entry.lockedUntil) {
			delete(l.entries, key)
		}
	}
}

func (f *signInFailures) lockedUntilOrZero() time.Time {
	if f == nil {
		return time.Time{}
	}
	return f.lockedUntil
}
