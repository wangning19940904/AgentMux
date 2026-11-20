package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

// Usage-aware turn scheduling. Every agent turn on this machine spends the
// subscription of one framework account: a framework kind plus the sign-in
// it uses. The TurnScheduler admits turns per account by priority class, so a
// person's chat turn is not stuck behind automated work when an account is
// busy, and holds automated work while an account is at its usage limit.
// See docs/architecture/usage-aware-scheduling.md.

// TurnClass is a turn's priority; a lower value goes first.
type TurnClass int

const (
	// TurnInteractive is a person's chat turn, a direct API invocation and a
	// Super Agent router turn for a person's message.
	TurnInteractive TurnClass = iota
	// TurnTicket is Super Agent worker and helper turns, router turns for
	// Ticket updates and plans, and orchestration tasks.
	TurnTicket
	// TurnScheduled is cron and webhook triggers and router turns for timers
	// and supervision signals.
	TurnScheduled
	// TurnBackground is memory passes and guard reviews.
	TurnBackground

	turnClassCount = int(TurnBackground) + 1
)

var turnClassNames = [turnClassCount]string{"interactive", "ticket", "scheduled", "background"}

func (c TurnClass) String() string {
	if c < 0 || int(c) >= turnClassCount {
		return fmt.Sprintf("class(%d)", int(c))
	}
	return turnClassNames[c]
}

// ParseTurnClass reads a class name as String writes it.
func ParseTurnClass(name string) (TurnClass, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for index, candidate := range turnClassNames {
		if candidate == name {
			return TurnClass(index), true
		}
	}
	return 0, false
}

// TurnClassHeader lets an HTTP invocation name its class. It can only lower
// a turn from the default, interactive.
const TurnClassHeader = "X-AgentMux-Turn-Class"

type turnClassKey struct{}
type turnAwaitedKey struct{}

// WithTurnClass names the class of the turns started with ctx.
func WithTurnClass(ctx context.Context, class TurnClass) context.Context {
	return context.WithValue(ctx, turnClassKey{}, class)
}

// TurnClassFrom returns the class WithTurnClass set, if any.
func TurnClassFrom(ctx context.Context) (TurnClass, bool) {
	if ctx == nil {
		return 0, false
	}
	class, ok := ctx.Value(turnClassKey{}).(TurnClass)
	return class, ok
}

// WithAwaitedTurn marks the turns started with ctx as awaited by a turn that
// is running: a helper its worker polls for, or a Super Agent router turn
// the agent's workers depend on. An awaited turn is admitted past the
// account's limit, since waiting for a place the waiting turn holds would
// deadlock. It still counts as running and still waits out a usage limit.
func WithAwaitedTurn(ctx context.Context) context.Context {
	return context.WithValue(ctx, turnAwaitedKey{}, true)
}

func turnAwaitedFrom(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	awaited, _ := ctx.Value(turnAwaitedKey{}).(bool)
	return awaited
}

// accountProfileEnv are the environment variables that make a framework use
// another sign-in, configuration or key than the machine's default.
var accountProfileEnv = []string{
	"CLAUDE_CONFIG_DIR", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL",
	"CODEX_HOME", "OPENAI_API_KEY", "OPENAI_BASE_URL",
	"CURSOR_API_KEY",
}

// AccountProfile names the sign-in an Agent's env selects, as a short hash of
// the values that select one, or "" for the machine's default. The values
// never leave the process.
func AccountProfile(env map[string]string) string {
	hash := sha256.New()
	found := false
	for _, key := range accountProfileEnv {
		value := strings.TrimSpace(env[key])
		if value == "" {
			continue
		}
		found = true
		hash.Write([]byte(key + "=" + value + "\x00"))
	}
	if !found {
		return ""
	}
	return hex.EncodeToString(hash.Sum(nil))[:12]
}

// AccountKey is the scheduler's key for a framework kind and a profile.
func AccountKey(runtimeID, profile string) string {
	runtimeID = strings.TrimSpace(runtimeID)
	if profile = strings.TrimSpace(profile); profile != "" {
		return runtimeID + "#" + profile
	}
	return runtimeID
}

// TurnSchedulerSettings is one machine's scheduling policy.
type TurnSchedulerSettings struct {
	// Enabled turns limits and usage-limit holds on. Off, every turn is
	// admitted at once; running turns are still counted for visibility.
	Enabled bool `json:"enabled"`
	// MaxConcurrent is the most turns that run at once on one account,
	// except interactive ones. 0 means no limit.
	MaxConcurrent int `json:"max_concurrent"`
	// InteractiveHeadroom is how many places beyond MaxConcurrent only
	// interactive turns may take.
	InteractiveHeadroom int `json:"interactive_headroom"`
}

const (
	maxTurnConcurrent = 64
	maxTurnHeadroom   = 16
	// defaultUsageHold is how long a usage limit without a known reset holds
	// an account before the next turn finds out whether it is still there.
	defaultUsageHold = 15 * time.Minute
)

// DefaultTurnSchedulerSettings is off, with the limits used once it is
// turned on.
func DefaultTurnSchedulerSettings() TurnSchedulerSettings {
	return TurnSchedulerSettings{Enabled: false, MaxConcurrent: 2, InteractiveHeadroom: 1}
}

// Normalized clamps the settings to their ranges.
func (s TurnSchedulerSettings) Normalized() TurnSchedulerSettings {
	s.MaxConcurrent = min(max(s.MaxConcurrent, 0), maxTurnConcurrent)
	s.InteractiveHeadroom = min(max(s.InteractiveHeadroom, 0), maxTurnHeadroom)
	return s
}

// limit is the most running turns a class may start beside; 0 is none.
func (s TurnSchedulerSettings) limit(class TurnClass) int {
	if !s.Enabled || s.MaxConcurrent <= 0 {
		return 0
	}
	if class == TurnInteractive {
		return s.MaxConcurrent + s.InteractiveHeadroom
	}
	return s.MaxConcurrent
}

// UsageLimitSource reports whether a framework kind's default sign-in is at
// its usage limit and until when, as framework health knows it.
type UsageLimitSource func(runtimeID string) (resetsAt time.Time, limited bool)

// UsageLimitClassifier reports whether an error is a framework's usage-limit
// message, and the reset it names (zero when it names none).
type UsageLimitClassifier func(errText string, now time.Time) (resetsAt time.Time, limited bool)

// TurnRequest is one turn asking for a place.
type TurnRequest struct {
	RuntimeID string
	// Profile is AccountProfile of the Agent's env.
	Profile        string
	Class          TurnClass
	Awaited        bool
	AgentID        string
	ConversationID string
}

func (r TurnRequest) account() string { return AccountKey(r.RuntimeID, r.Profile) }

// turnClock is the time source; tests replace it.
type turnClock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) func() bool
}

type realTurnClock struct{}

func (realTurnClock) Now() time.Time { return time.Now() }
func (realTurnClock) AfterFunc(d time.Duration, f func()) func() bool {
	return time.AfterFunc(d, f).Stop
}

// TurnSchedulerOptions configures NewTurnScheduler. Every field is optional.
type TurnSchedulerOptions struct {
	Settings TurnSchedulerSettings
	Limits   UsageLimitSource
	Classify UsageLimitClassifier
	Log      *slog.Logger
	clock    turnClock
}

// TurnScheduler admits turns per account. A nil *TurnScheduler admits
// everything.
type TurnScheduler struct {
	limits   UsageLimitSource
	classify UsageLimitClassifier
	log      *slog.Logger
	clock    turnClock

	mu       sync.Mutex
	settings TurnSchedulerSettings
	accounts map[string]*turnAccount
}

type turnAccount struct {
	key, runtimeID, profile string
	running                 int
	runningByClass          [turnClassCount]int
	// awaited are waiting awaited turns, which only a hold stops; queues
	// are the other waiting turns by class, each in arrival order.
	awaited []*turnWaiter
	queues  [turnClassCount][]*turnWaiter

	heldUntil  time.Time
	holdReason string
	stopHold   func() bool
	// disproved is the latest reset a finished turn showed was already
	// over, so framework health's older record does not hold the account
	// again.
	disproved time.Time
}

type turnWaiter struct {
	req      TurnRequest
	since    time.Time
	ready    chan struct{}
	admitted bool
	lease    *TurnLease
}

// NewTurnScheduler builds a scheduler.
func NewTurnScheduler(opts TurnSchedulerOptions) *TurnScheduler {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.clock == nil {
		opts.clock = realTurnClock{}
	}
	return &TurnScheduler{
		limits: opts.Limits, classify: opts.Classify, log: opts.Log, clock: opts.clock,
		settings: opts.Settings.Normalized(), accounts: map[string]*turnAccount{},
	}
}

// Settings returns the current settings.
func (s *TurnScheduler) Settings() TurnSchedulerSettings {
	if s == nil {
		return DefaultTurnSchedulerSettings()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

// Configure replaces the settings. Waiting turns the new settings allow
// start at once.
func (s *TurnScheduler) Configure(settings TurnSchedulerSettings) TurnSchedulerSettings {
	if s == nil {
		return settings.Normalized()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings = settings.Normalized()
	for _, account := range s.accounts {
		s.dispatchLocked(account)
	}
	return s.settings
}

// TurnLease is an admitted turn's place. Done gives it back.
type TurnLease struct {
	s       *TurnScheduler
	account *turnAccount
	class   TurnClass
	once    sync.Once
}

// TurnWaitError is why a turn stopped waiting for a place.
type TurnWaitError struct {
	Account string
	// HeldUntil is set when the account was held at its usage limit.
	HeldUntil time.Time
	Err       error
}

func (e *TurnWaitError) Error() string {
	if !e.HeldUntil.IsZero() {
		return fmt.Sprintf("waited for %s's usage limit to reset at %s: %v", e.Account, e.HeldUntil.Local().Format("15:04 Jan 2"), e.Err)
	}
	return fmt.Sprintf("waited for a place on %s: %v", e.Account, e.Err)
}

func (e *TurnWaitError) Unwrap() error { return e.Err }

// Admit waits until the turn may start, or until ctx ends. A nil lease with
// a nil error means there is nothing to give back.
func (s *TurnScheduler) Admit(ctx context.Context, req TurnRequest) (*TurnLease, error) {
	if s == nil || strings.TrimSpace(req.RuntimeID) == "" {
		return nil, nil
	}
	if req.Class < 0 || int(req.Class) >= turnClassCount {
		req.Class = TurnInteractive
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Framework health speaks for the default sign-in only, and is read
	// outside the lock.
	var healthReset time.Time
	if req.Class != TurnInteractive && req.Profile == "" && s.limits != nil && s.Settings().Enabled {
		if resetsAt, limited := s.limits(req.RuntimeID); limited {
			healthReset = resetsAt
		}
	}

	s.mu.Lock()
	account := s.accountLocked(req)
	now := s.clock.Now()
	if !healthReset.IsZero() && healthReset.After(now) && healthReset.After(account.disproved) {
		s.holdLocked(account, healthReset, "usage limit")
	}
	waiter := &turnWaiter{req: req, since: now, ready: make(chan struct{})}
	if req.Awaited {
		account.awaited = append(account.awaited, waiter)
	} else {
		account.queues[req.Class] = append(account.queues[req.Class], waiter)
	}
	s.dispatchLocked(account)
	if waiter.admitted {
		s.mu.Unlock()
		return waiter.lease, nil
	}
	s.mu.Unlock()

	select {
	case <-waiter.ready:
		return waiter.lease, nil
	case <-ctx.Done():
	}
	s.mu.Lock()
	if waiter.admitted {
		// Admitted as ctx ended: give the place straight back, without
		// taking the cancellation as news about the account.
		s.mu.Unlock()
		waiter.lease.Done(ctx.Err().Error())
		return nil, ctx.Err()
	}
	s.removeWaiterLocked(account, waiter)
	err := &TurnWaitError{Account: account.key, Err: ctx.Err()}
	if req.Class != TurnInteractive && account.heldUntil.After(s.clock.Now()) {
		err.HeldUntil = account.heldUntil
	}
	s.dispatchLocked(account)
	s.forgetIdleLocked(account)
	s.mu.Unlock()
	return nil, err
}

func (s *TurnScheduler) accountLocked(req TurnRequest) *turnAccount {
	key := req.account()
	account := s.accounts[key]
	if account == nil {
		account = &turnAccount{key: key, runtimeID: strings.TrimSpace(req.RuntimeID), profile: strings.TrimSpace(req.Profile)}
		s.accounts[key] = account
	}
	return account
}

// dispatchLocked starts every waiting turn the rules allow: awaited turns
// first, then each class in order, each in arrival order. A turn that has to
// wait for a place stops every turn behind it, of its class and below.
func (s *TurnScheduler) dispatchLocked(account *turnAccount) {
	held := s.settings.Enabled && account.heldUntil.After(s.clock.Now())
	kept := account.awaited[:0]
	for _, waiter := range account.awaited {
		if held && waiter.req.Class != TurnInteractive {
			kept = append(kept, waiter)
			continue
		}
		s.admitLocked(account, waiter)
	}
	clear(account.awaited[len(kept):])
	account.awaited = kept
	for class := range turnClassCount {
		if held && TurnClass(class) != TurnInteractive {
			return
		}
		limit := s.settings.limit(TurnClass(class))
		for len(account.queues[class]) > 0 {
			if limit > 0 && account.running >= limit {
				return
			}
			waiter := account.queues[class][0]
			account.queues[class][0] = nil
			account.queues[class] = account.queues[class][1:]
			s.admitLocked(account, waiter)
		}
	}
}

func (s *TurnScheduler) admitLocked(account *turnAccount, waiter *turnWaiter) {
	account.running++
	account.runningByClass[waiter.req.Class]++
	waiter.admitted = true
	waiter.lease = &TurnLease{s: s, account: account, class: waiter.req.Class}
	close(waiter.ready)
}

func (s *TurnScheduler) removeWaiterLocked(account *turnAccount, waiter *turnWaiter) {
	remove := func(list []*turnWaiter) []*turnWaiter {
		for index, candidate := range list {
			if candidate == waiter {
				return append(list[:index], list[index+1:]...)
			}
		}
		return list
	}
	account.awaited = remove(account.awaited)
	account.queues[waiter.req.Class] = remove(account.queues[waiter.req.Class])
}

// holdLocked holds the account's non-interactive turns until until, or
// longer if it is already held longer.
func (s *TurnScheduler) holdLocked(account *turnAccount, until time.Time, reason string) {
	if !until.After(account.heldUntil) {
		return
	}
	if account.stopHold != nil {
		account.stopHold()
	}
	account.heldUntil, account.holdReason = until, reason
	s.log.Info("turn scheduler holds account", "account", account.key, "until", until, "reason", reason)
	delay := max(until.Sub(s.clock.Now()), 0)
	account.stopHold = s.clock.AfterFunc(delay, func() { s.holdExpired(account, until) })
}

func (s *TurnScheduler) holdExpired(account *turnAccount, until time.Time) {
	var healthReset time.Time
	if account.profile == "" && s.limits != nil {
		if resetsAt, limited := s.limits(account.runtimeID); limited {
			healthReset = resetsAt
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !account.heldUntil.Equal(until) {
		return // replaced by a later hold, or cleared
	}
	account.stopHold = nil
	if healthReset.After(s.clock.Now()) && healthReset.After(until) {
		s.holdLocked(account, healthReset, "usage limit")
		return
	}
	account.heldUntil, account.holdReason = time.Time{}, ""
	s.log.Info("turn scheduler releases account", "account", account.key)
	s.dispatchLocked(account)
	s.forgetIdleLocked(account)
}

func (s *TurnScheduler) clearHoldLocked(account *turnAccount) {
	if account.heldUntil.IsZero() {
		return
	}
	if account.stopHold != nil {
		account.stopHold()
		account.stopHold = nil
	}
	if account.heldUntil.After(account.disproved) {
		account.disproved = account.heldUntil
	}
	s.log.Info("turn scheduler releases account after a finished turn", "account", account.key)
	account.heldUntil, account.holdReason = time.Time{}, ""
}

// forgetIdleLocked drops an account with nothing running, waiting or held.
// What it disproved is forgotten with it; framework health's record expires
// on its own.
func (s *TurnScheduler) forgetIdleLocked(account *turnAccount) {
	if account.running > 0 || len(account.awaited) > 0 || !account.heldUntil.IsZero() || account.disproved.After(s.clock.Now()) {
		return
	}
	for _, queue := range account.queues {
		if len(queue) > 0 {
			return
		}
	}
	if s.accounts[account.key] == account {
		delete(s.accounts, account.key)
	}
}

// Done gives the place back. errText is the turn's first error, or "" when
// it finished without one: a usage-limit error holds the account, and a
// clean finish ends a hold early.
func (l *TurnLease) Done(errText string) {
	if l == nil {
		return
	}
	l.once.Do(func() {
		s := l.s
		now := s.clock.Now()
		var resetsAt time.Time
		limited := false
		if errText != "" && s.classify != nil {
			resetsAt, limited = s.classify(errText, now)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		account := l.account
		account.running--
		account.runningByClass[l.class]--
		if s.settings.Enabled {
			switch {
			case limited:
				if !resetsAt.After(now) {
					resetsAt = now.Add(defaultUsageHold)
				}
				s.holdLocked(account, resetsAt, "usage limit")
			case errText == "":
				s.clearHoldLocked(account)
			}
		}
		s.dispatchLocked(account)
		s.forgetIdleLocked(account)
	})
}

// watch forwards events and gives the place back when they end, with the
// turn's first error. If ctx ends first, the place is given back and the
// rest of the events are drained.
func (l *TurnLease) watch(ctx context.Context, events <-chan *Event) <-chan *Event {
	out := make(chan *Event, 32)
	go func() {
		defer close(out)
		errText := ""
		defer func() { l.Done(errText) }()
		for event := range events {
			if event != nil && event.Type == EventError && errText == "" {
				errText = firstNonEmptyString(errString(event.Err), event.Text, "error")
			}
			select {
			case out <- event:
			case <-ctx.Done():
				if errText == "" {
					errText = ctx.Err().Error()
				}
				go func() {
					for range events {
					}
				}()
				return
			}
		}
	}()
	return out
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// TurnSchedulerSnapshot is what the Console shows.
type TurnSchedulerSnapshot struct {
	Settings TurnSchedulerSettings `json:"settings"`
	Accounts []TurnAccountSnapshot `json:"accounts"`
}

// TurnAccountSnapshot is one account's running and waiting turns.
type TurnAccountSnapshot struct {
	Account        string               `json:"account"`
	RuntimeID      string               `json:"runtime_id"`
	Profile        string               `json:"profile,omitempty"`
	Limit          int                  `json:"limit"`
	Running        int                  `json:"running"`
	RunningByClass map[string]int       `json:"running_by_class,omitempty"`
	HeldUntil      *time.Time           `json:"held_until,omitempty"`
	HoldReason     string               `json:"hold_reason,omitempty"`
	Queued         []TurnWaiterSnapshot `json:"queued"`
}

// TurnWaiterSnapshot is one waiting turn.
type TurnWaiterSnapshot struct {
	Class          string    `json:"class"`
	Awaited        bool      `json:"awaited,omitempty"`
	AgentID        string    `json:"agent_id,omitempty"`
	ConversationID string    `json:"conversation_id,omitempty"`
	WaitingSince   time.Time `json:"waiting_since"`
	Held           bool      `json:"held,omitempty"`
}

// Snapshot returns every account in use, by key.
func (s *TurnScheduler) Snapshot() TurnSchedulerSnapshot {
	if s == nil {
		return TurnSchedulerSnapshot{Settings: DefaultTurnSchedulerSettings(), Accounts: []TurnAccountSnapshot{}}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	out := TurnSchedulerSnapshot{Settings: s.settings, Accounts: make([]TurnAccountSnapshot, 0, len(s.accounts))}
	for _, account := range s.accounts {
		held := s.settings.Enabled && account.heldUntil.After(now)
		view := TurnAccountSnapshot{
			Account: account.key, RuntimeID: account.runtimeID, Profile: account.profile,
			Limit: s.settings.limit(TurnTicket), Running: account.running, Queued: []TurnWaiterSnapshot{},
		}
		for class, count := range account.runningByClass {
			if count > 0 {
				if view.RunningByClass == nil {
					view.RunningByClass = map[string]int{}
				}
				view.RunningByClass[TurnClass(class).String()] = count
			}
		}
		if held {
			until := account.heldUntil
			view.HeldUntil, view.HoldReason = &until, account.holdReason
		}
		add := func(waiter *turnWaiter) {
			view.Queued = append(view.Queued, TurnWaiterSnapshot{
				Class: waiter.req.Class.String(), Awaited: waiter.req.Awaited,
				AgentID: waiter.req.AgentID, ConversationID: waiter.req.ConversationID,
				WaitingSince: waiter.since, Held: held && waiter.req.Class != TurnInteractive,
			})
		}
		for _, waiter := range account.awaited {
			add(waiter)
		}
		for _, queue := range account.queues {
			for _, waiter := range queue {
				add(waiter)
			}
		}
		out.Accounts = append(out.Accounts, view)
	}
	sort.Slice(out.Accounts, func(i, j int) bool { return out.Accounts[i].Account < out.Accounts[j].Account })
	return out
}

// turnRequestFor builds the request of a turn about to be sent.
func turnRequestFor(ctx context.Context, data map[string]string) TurnRequest {
	class, ok := TurnClassFrom(ctx)
	if !ok {
		switch data["origin"] {
		case OriginCron, OriginWebhook:
			class = TurnScheduled
		default:
			class = TurnInteractive
		}
	}
	return TurnRequest{
		RuntimeID: data["runtime_id"], Profile: data["account_profile"], Class: class,
		Awaited: turnAwaitedFrom(ctx), AgentID: data["agent_id"], ConversationID: data["conversation_id"],
	}
}

// setAccountProfile records the workspace's account profile in hook data,
// beside its runtime_id, so the turn is admitted on the right account.
func setAccountProfile(data map[string]string, workspace WorkspaceInitOptions) {
	if workspace.AccountProfile != "" {
		data["account_profile"] = workspace.AccountProfile
	}
}

// SetTurnScheduler installs the scheduler every turn is admitted by. Called
// by bootstrap before channels start; nil admits everything.
func (e *Engine) SetTurnScheduler(scheduler *TurnScheduler) {
	e.turnSchedulerMu.Lock()
	e.turnScheduler = scheduler
	e.turnSchedulerMu.Unlock()
}

func (e *Engine) currentTurnScheduler() *TurnScheduler {
	e.turnSchedulerMu.RLock()
	defer e.turnSchedulerMu.RUnlock()
	return e.turnScheduler
}
