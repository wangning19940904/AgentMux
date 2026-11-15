// Package health keeps one health record per agent framework on this
// machine: whether it can run right now, why not, and when that was learned.
//
// Records come from four places: the on-demand "reply ok" test, periodic
// checks (off by default), rechecks other packages ask for, and failures of
// real runs that can only be the framework's fault (not signed in, CLI
// missing or too old, model rejected, usage limit). A check is the same smoke
// test the Console's Test button runs (framework.SmokeTest).
package health

import (
	"time"

	"github.com/wangning19940904/AgentMux/framework"
)

// State is a framework's health on this machine.
type State string

const (
	// StateOK: the latest evidence is a passing check or test.
	StateOK State = "ok"
	// StateFailing: the latest evidence is a failure the framework owns.
	StateFailing State = "failing"
	// StateUsageLimit: the subscription is at its usage limit, until ResetsAt
	// when that is known.
	StateUsageLimit State = "usage_limit"
	// StateUnknown: never checked, or a usage limit whose reset has passed.
	StateUnknown State = "unknown"
)

// Source says where a record's evidence came from.
type Source string

const (
	SourceTest     Source = "test"     // the Console's Test, POST /api/v1/frameworks/test
	SourcePeriodic Source = "periodic" // a periodic check
	SourceRecheck  Source = "recheck"  // a recheck asked for by a caller or after a failed run
	SourceRun      Source = "run"      // a real invocation that failed for a framework-level reason
)

// Record is the health of one framework on this machine.
type Record struct {
	Kind   string `json:"kind"`
	State  State  `json:"state"`
	Reason string `json:"reason,omitempty"`
	// ErrorKind classifies a failure the same way smoke test results do.
	ErrorKind framework.SmokeErrorKind `json:"error_kind,omitempty"`
	Source    Source                   `json:"source,omitempty"`
	// Model is the model the evidence used; empty is the framework's default.
	Model string `json:"model,omitempty"`
	// CheckedAt is when the evidence was produced. Zero for StateUnknown
	// records that were never checked.
	CheckedAt time.Time `json:"checked_at,omitzero"`
	// ProbedAt is when a check last ran. Periodic checks and the recheck
	// throttle are measured from it; failures of real runs do not move it.
	ProbedAt *time.Time `json:"probed_at,omitempty"`
	// ResetsAt is when a usage limit ends, when the framework said so.
	ResetsAt *time.Time `json:"resets_at,omitempty"`
	// Failures counts failing or usage-limit records in a row.
	Failures int        `json:"consecutive_failures,omitempty"`
	LastOKAt *time.Time `json:"last_ok_at,omitempty"`
}

// heldUntilReset reports whether rec is a usage limit with a reset still
// ahead: checking it would spend a request only to be refused.
func (rec Record) heldUntilReset(now time.Time) bool {
	return rec.State == StateUsageLimit && rec.ResetsAt != nil && now.Before(*rec.ResetsAt)
}

// effective is rec as callers should read it at now.
func (rec Record) effective(now time.Time) Record {
	if rec.State == StateUsageLimit && rec.ResetsAt != nil && !now.Before(*rec.ResetsAt) {
		rec.State = StateUnknown
		rec.Reason = "the usage limit reset at " + rec.ResetsAt.UTC().Format(time.RFC3339) + "; not checked since"
	}
	return rec
}

func unknownRecord(kind string) Record {
	return Record{Kind: kind, State: StateUnknown, Reason: "not checked yet"}
}

// Settings controls periodic checks. Rechecks, tests and the API work
// whatever these say.
type Settings struct {
	// Enabled turns on periodic checks and learning from real runs. Off by
	// default: with it off nothing runs on its own.
	Enabled bool `json:"enabled"`
	// IntervalMinutes is how old a check may get before the next one.
	IntervalMinutes int `json:"interval_minutes"`
}

const (
	DefaultIntervalMinutes = 30
	MinIntervalMinutes     = 5
	MaxIntervalMinutes     = 24 * 60
)

// DefaultSettings are used until settings are saved.
func DefaultSettings() Settings {
	return Settings{IntervalMinutes: DefaultIntervalMinutes}
}

// Normalized fills a missing interval and clamps it into range.
func (s Settings) Normalized() Settings {
	if s.IntervalMinutes <= 0 {
		s.IntervalMinutes = DefaultIntervalMinutes
	}
	s.IntervalMinutes = min(max(s.IntervalMinutes, MinIntervalMinutes), MaxIntervalMinutes)
	return s
}

func (s Settings) interval() time.Duration {
	return time.Duration(s.Normalized().IntervalMinutes) * time.Minute
}
