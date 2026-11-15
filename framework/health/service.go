package health

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/framework"
)

const (
	// RecordsSetting holds the records and the latest test results.
	RecordsSetting = "framework_health_v2"
	// legacyResultsSetting held test results only, before records existed.
	legacyResultsSetting = "framework_health_v1"
	// SettingsSetting holds Settings.
	SettingsSetting = "framework_health_checks"

	maxResults = 200

	// RecheckThrottle: a recheck within this long of the last check returns
	// that check's record instead of running another.
	RecheckThrottle = 30 * time.Second
	// DefaultCheckTimeout bounds one check. A check that times out is asked
	// once more; only a second timeout in a row is recorded.
	DefaultCheckTimeout = 90 * time.Second
	// DefaultCheckGap separates the checks of one round, so a round never
	// starts several frameworks at once.
	DefaultCheckGap = 5 * time.Second
	// DefaultPoll is how often the periodic loop looks for checks that are due.
	DefaultPoll = time.Minute
	// DefaultStartDelay lets the daemon settle before its first round.
	DefaultStartDelay = 30 * time.Second
)

// Store is where records and settings are kept; *store.Store satisfies it.
type Store interface {
	GetSetting(ctx context.Context, key string) (string, bool, error)
	SetSetting(ctx context.Context, key, value string) error
}

// CheckFunc runs one check of a framework with its default model.
type CheckFunc func(ctx context.Context, kind string, timeout time.Duration) (framework.SmokeResult, error)

// Clock is the time source; tests replace it.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now().UTC() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Options configures a Service. Only Store is required.
type Options struct {
	Store Store
	// Check runs one check. Default: framework.SmokeTest with the
	// framework's default model, which leaves no session or transcript behind
	// for frameworks that support health-check runs.
	Check CheckFunc
	// Kinds lists the frameworks this machine can run. Default: catalogued,
	// supported, registered and installed frameworks. Frameworks with a
	// record are checked too, so a CLI that disappears is noticed.
	Kinds func() []string
	// Registered reports whether a framework kind has an agent runtime.
	// Default: core.HasAgent.
	Registered   func(kind string) bool
	Clock        Clock
	Log          *slog.Logger
	CheckTimeout time.Duration
	CheckGap     time.Duration
	Poll         time.Duration
	StartDelay   time.Duration
}

// Service owns the health records of the frameworks on this machine.
type Service struct {
	store      Store
	check      CheckFunc
	kinds      func() []string
	registered func(string) bool
	clock      Clock
	log        *slog.Logger
	timeout    time.Duration
	gap        time.Duration
	poll       time.Duration
	startDelay time.Duration

	mu       sync.Mutex
	loaded   bool
	records  map[string]Record
	results  []framework.SmokeResult
	settings *Settings
	flights  map[string]*flight

	lifeMu  sync.Mutex
	life    context.Context
	stopped bool
	wg      sync.WaitGroup
	wake    chan struct{}
}

type flight struct {
	done chan struct{}
	rec  Record
	err  error
}

// New builds a Service. Call Run to start periodic checks.
func New(opts Options) *Service {
	s := &Service{
		store: opts.Store, check: opts.Check, kinds: opts.Kinds, registered: opts.Registered,
		clock: opts.Clock, log: opts.Log, timeout: opts.CheckTimeout, gap: opts.CheckGap,
		poll: opts.Poll, startDelay: opts.StartDelay,
		records: map[string]Record{}, flights: map[string]*flight{},
		life: context.Background(), wake: make(chan struct{}, 1),
	}
	if s.check == nil {
		s.check = smokeCheck
	}
	if s.kinds == nil {
		s.kinds = RunnableKinds
	}
	if s.registered == nil {
		s.registered = core.HasAgent
	}
	if s.clock == nil {
		s.clock = realClock{}
	}
	if s.log == nil {
		s.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if s.timeout <= 0 {
		s.timeout = DefaultCheckTimeout
	}
	if s.gap < 0 {
		s.gap = 0
	} else if s.gap == 0 {
		s.gap = DefaultCheckGap
	}
	if s.poll <= 0 {
		s.poll = DefaultPoll
	}
	if s.startDelay < 0 {
		s.startDelay = 0
	} else if s.startDelay == 0 {
		s.startDelay = DefaultStartDelay
	}
	return s
}

func smokeCheck(ctx context.Context, kind string, timeout time.Duration) (framework.SmokeResult, error) {
	return framework.SmokeTest(ctx, framework.SmokeOptions{Kind: kind, Timeout: timeout})
}

// RunnableKinds lists the catalogued frameworks this machine can run now.
func RunnableKinds() []string {
	var kinds []string
	for _, spec := range framework.Catalog() {
		if spec.Supported && core.HasAgent(spec.Kind) && framework.IsInstalled(spec.Kind) {
			kinds = append(kinds, spec.Kind)
		}
	}
	return kinds
}

// ---- reading ----

// Status returns the health of one framework on this machine. A framework
// never checked is StateUnknown.
func (s *Service) Status(ctx context.Context, kind string) (Record, error) {
	kind = strings.TrimSpace(kind)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(ctx); err != nil {
		return Record{}, err
	}
	rec, ok := s.records[kind]
	if !ok {
		return unknownRecord(kind), nil
	}
	return rec.effective(s.clock.Now()), nil
}

// Records returns a record for every framework this machine can run or has
// a record of, sorted by kind.
func (s *Service) Records(ctx context.Context) ([]Record, error) {
	kinds := s.kinds()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(ctx); err != nil {
		return nil, err
	}
	now := s.clock.Now()
	seen := map[string]bool{}
	out := make([]Record, 0, len(kinds)+len(s.records))
	for _, rec := range s.records {
		seen[rec.Kind] = true
		out = append(out, rec.effective(now))
	}
	for _, kind := range kinds {
		if !seen[kind] {
			seen[kind] = true
			out = append(out, unknownRecord(kind))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out, nil
}

// Results returns the latest test or check result per framework and model,
// newest first.
func (s *Service) Results(ctx context.Context) ([]framework.SmokeResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(ctx); err != nil {
		return nil, err
	}
	return slices.Clone(s.results), nil
}

// ---- settings ----

// Settings returns the periodic check settings.
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settingsLocked(ctx)
}

func (s *Service) settingsLocked(ctx context.Context) (Settings, error) {
	if s.settings != nil {
		return *s.settings, nil
	}
	settings := DefaultSettings()
	if s.store != nil {
		raw, ok, err := s.store.GetSetting(ctx, SettingsSetting)
		if err != nil {
			return settings, err
		}
		if ok && json.Unmarshal([]byte(raw), &settings) != nil {
			settings = DefaultSettings()
		}
	}
	settings = settings.Normalized()
	s.settings = &settings
	return settings, nil
}

// SetSettings saves the periodic check settings. Turning checks on starts a
// round of the checks that are due.
func (s *Service) SetSettings(ctx context.Context, settings Settings) (Settings, error) {
	settings = settings.Normalized()
	raw, err := json.Marshal(settings)
	if err != nil {
		return Settings{}, err
	}
	s.mu.Lock()
	if s.store != nil {
		if err := s.store.SetSetting(ctx, SettingsSetting, string(raw)); err != nil {
			s.mu.Unlock()
			return Settings{}, err
		}
	}
	s.settings = &settings
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return settings, nil
}

func (s *Service) enabled() bool {
	settings, err := s.Settings(s.lifeContext())
	return err == nil && settings.Enabled
}

// ---- recording ----

// RecordTest keeps the result of an on-demand test. A passing test shows the
// framework works, so it marks it ok. A failing one updates the record only
// when it tested the framework as a check would: its default model, in this
// machine's own environment (plain). A test of a model or an Agent's
// environment being set up does not mark the framework failing.
func (s *Service) RecordTest(ctx context.Context, result framework.SmokeResult, plain bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(ctx); err != nil {
		return err
	}
	s.addResultLocked(result)
	if result.OK || (plain && result.Model == "") {
		s.putLocked(recordFromResult(result, SourceTest, s.clock.Now()))
	}
	return s.saveLocked(ctx)
}

// RecordFailure records a real run's failure when it is the framework's
// (ClassifyRunError) and reports whether it was. It runs no check.
func (s *Service) RecordFailure(ctx context.Context, kind, errText string) (Record, bool, error) {
	kind = strings.TrimSpace(kind)
	errorKind, ok := ClassifyRunError(errText)
	if kind == "" || !ok {
		return Record{}, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(ctx); err != nil {
		return Record{}, true, err
	}
	rec := s.putLocked(recordFromRun(kind, errorKind, errText, s.clock.Now()))
	return rec, true, s.saveLocked(ctx)
}

// putLocked stores rec, carrying over what it does not itself know.
func (s *Service) putLocked(rec Record) Record {
	prev, had := s.records[rec.Kind]
	if rec.ProbedAt == nil && had {
		rec.ProbedAt = prev.ProbedAt
	}
	switch rec.State {
	case StateOK:
		at := rec.CheckedAt
		rec.LastOKAt = &at
		rec.Failures = 0
	default:
		rec.Failures = 1
		if had && (prev.State == StateFailing || prev.State == StateUsageLimit) {
			rec.Failures = prev.Failures + 1
		}
		if had {
			rec.LastOKAt = prev.LastOKAt
		}
		// A run that hits a usage limit again without saying when it resets
		// does not forget a reset learned earlier.
		if rec.State == StateUsageLimit && rec.ResetsAt == nil && prev.heldUntilReset(s.clock.Now()) {
			rec.ResetsAt = prev.ResetsAt
		}
	}
	if had && prev.State != rec.State {
		s.log.Info("framework health changed", "kind", rec.Kind, "from", prev.State, "to", rec.State,
			"source", rec.Source, "error_kind", rec.ErrorKind)
	}
	s.records[rec.Kind] = rec
	return rec
}

func (s *Service) addResultLocked(result framework.SmokeResult) {
	kept := s.results[:0:0]
	for _, existing := range s.results {
		if existing.Kind != result.Kind || existing.Model != result.Model {
			kept = append(kept, existing)
		}
	}
	kept = append(kept, result)
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].CheckedAt.After(kept[j].CheckedAt) })
	if len(kept) > maxResults {
		kept = kept[:maxResults]
	}
	s.results = kept
}

// ---- rechecks ----

// Recheck checks a framework now and returns its record. Concurrent calls
// for one framework share one check, and a check within RecheckThrottle of
// the last one is not repeated: its record is returned. A usage limit whose
// reset is still ahead is not checked before the reset.
func (s *Service) Recheck(ctx context.Context, kind string) (Record, error) {
	kind = strings.TrimSpace(kind)
	if kind == "" || !s.registered(kind) {
		return Record{}, framework.ErrUnknownFramework
	}
	return s.recheck(ctx, kind, SourceRecheck, true)
}

func (s *Service) recheck(ctx context.Context, kind string, source Source, throttle bool) (Record, error) {
	s.mu.Lock()
	if err := s.loadLocked(ctx); err != nil {
		s.mu.Unlock()
		return Record{}, err
	}
	now := s.clock.Now()
	rec, had := s.records[kind]
	if had && rec.heldUntilReset(now) {
		s.mu.Unlock()
		return rec, nil
	}
	f := s.flights[kind]
	if f == nil {
		if throttle && had && rec.ProbedAt != nil && now.Sub(*rec.ProbedAt) < RecheckThrottle {
			s.mu.Unlock()
			return rec.effective(now), nil
		}
		f = &flight{done: make(chan struct{})}
		s.flights[kind] = f
		if !s.spawn(func() { s.fly(kind, source, f) }) {
			delete(s.flights, kind)
			s.mu.Unlock()
			return Record{}, context.Canceled
		}
	}
	s.mu.Unlock()
	select {
	case <-f.done:
		return f.rec, f.err
	case <-ctx.Done():
		return Record{}, ctx.Err()
	}
}

// fly runs one check on the service's own context, so a caller that goes
// away does not cancel the check others wait for.
func (s *Service) fly(kind string, source Source, f *flight) {
	f.rec, f.err = s.probe(s.lifeContext(), kind, source)
	s.mu.Lock()
	delete(s.flights, kind)
	s.mu.Unlock()
	close(f.done)
}

// probe runs a check, asking once more after a timeout, and records it.
func (s *Service) probe(ctx context.Context, kind string, source Source) (Record, error) {
	result, err := s.check(ctx, kind, s.timeout)
	if err == nil && !result.OK && result.ErrorKind == framework.SmokeTimeout {
		s.log.Info("framework health check timed out; asking once more", "kind", kind)
		result, err = s.check(ctx, kind, s.timeout)
	}
	if err != nil {
		return Record{}, err
	}
	if result.Kind == "" {
		result.Kind = kind
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	s.addResultLocked(result)
	rec := recordFromResult(result, source, now)
	rec.ProbedAt = &now
	rec = s.putLocked(rec)
	if err := s.saveLocked(ctx); err != nil {
		s.log.Warn("save framework health", "kind", kind, "err", err)
	}
	return rec, nil
}

// ---- learning from real runs ----

// ObserveRun reports a real run's error for the framework that ran it. When
// periodic checks are on and the error is the framework's, it is recorded
// with source "run" and one check confirms it, so a single blip does not
// leave the framework failing. A usage limit is not confirmed: the check
// would be refused too. It returns at once; the work runs in the background.
func (s *Service) ObserveRun(kind, errText string) {
	kind = strings.TrimSpace(kind)
	if kind == "" || !s.registered(kind) {
		return
	}
	if _, ok := ClassifyRunError(errText); !ok || !s.enabled() {
		return
	}
	ctx := s.lifeContext()
	s.spawn(func() {
		rec, ok, err := s.RecordFailure(ctx, kind, errText)
		if err != nil {
			s.log.Warn("record framework failure", "kind", kind, "err", err)
		}
		if !ok || rec.State == StateUsageLimit {
			return
		}
		if _, err := s.recheck(ctx, kind, SourceRecheck, true); err != nil && ctx.Err() == nil {
			s.log.Warn("confirm framework failure", "kind", kind, "err", err)
		}
	})
}

// EngineSink is a core.EventSink that passes the errors of real runs to
// ObserveRun. Subscribe it with core.Engine.SubscribeEventSink.
func (s *Service) EngineSink() core.EventSink {
	return func(event core.HookEvent, data map[string]string) {
		if event != core.HookError || data == nil {
			return
		}
		s.ObserveRun(data["runtime_id"], data["error"])
	}
}

// ---- periodic checks ----

// Run checks the frameworks that are due, one after another, until ctx ends.
// With periodic checks off it runs nothing. The first round, after a short
// start delay, checks every framework whose last check is older than the
// interval. Run returns after the work it started has stopped.
func (s *Service) Run(ctx context.Context) {
	s.lifeMu.Lock()
	s.life = ctx
	s.lifeMu.Unlock()
	defer func() {
		s.lifeMu.Lock()
		s.stopped = true
		s.lifeMu.Unlock()
		s.wg.Wait()
	}()
	wait := s.startDelay
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.clock.After(wait):
		case <-s.wake:
		}
		wait = s.poll
		if s.enabled() {
			s.runRound(ctx)
		}
	}
}

// runRound checks every framework that is due now, one after another.
func (s *Service) runRound(ctx context.Context) {
	first := true
	for _, kind := range s.roundKinds(ctx) {
		if ctx.Err() != nil || !s.enabled() {
			return
		}
		if !s.due(ctx, kind) {
			continue
		}
		if !first && s.gap > 0 {
			select {
			case <-ctx.Done():
				return
			case <-s.clock.After(s.gap):
			}
		}
		first = false
		if _, err := s.recheck(ctx, kind, SourcePeriodic, false); err != nil && ctx.Err() == nil {
			s.log.Warn("framework health check", "kind", kind, "err", err)
		}
	}
}

func (s *Service) roundKinds(ctx context.Context) []string {
	kinds := s.kinds()
	s.mu.Lock()
	if s.loadLocked(ctx) == nil {
		for kind := range s.records {
			if s.registered(kind) {
				kinds = append(kinds, kind)
			}
		}
	}
	s.mu.Unlock()
	slices.Sort(kinds)
	return slices.Compact(kinds)
}

func (s *Service) due(ctx context.Context, kind string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	settings, err := s.settingsLocked(ctx)
	if err != nil {
		return false
	}
	rec, had := s.records[kind]
	if !had || rec.ProbedAt == nil {
		return true
	}
	now := s.clock.Now()
	if rec.heldUntilReset(now) {
		return false
	}
	if rec.State == StateUsageLimit && rec.ResetsAt != nil {
		return true // the reset has passed
	}
	return now.Sub(*rec.ProbedAt) >= settings.interval()
}

// spawn runs fn in the background unless Run has returned.
func (s *Service) spawn(fn func()) bool {
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	if s.stopped || s.life.Err() != nil {
		return false
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		fn()
	}()
	return true
}

func (s *Service) lifeContext() context.Context {
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	return s.life
}

// ---- storage ----

type persisted struct {
	Records []Record                `json:"records"`
	Results []framework.SmokeResult `json:"results"`
}

func (s *Service) loadLocked(ctx context.Context) error {
	if s.loaded || s.store == nil {
		s.loaded = true
		return nil
	}
	raw, ok, err := s.store.GetSetting(ctx, RecordsSetting)
	if err != nil {
		return err
	}
	var state persisted
	if ok {
		if json.Unmarshal([]byte(raw), &state) != nil {
			state = persisted{} // unreadable state is forgotten, as before
		}
	} else if raw, ok, err = s.store.GetSetting(ctx, legacyResultsSetting); err != nil {
		return err
	} else if ok && json.Unmarshal([]byte(raw), &state.Results) != nil {
		state.Results = nil
	}
	for _, rec := range state.Records {
		if rec.Kind != "" {
			s.records[rec.Kind] = rec
		}
	}
	s.results = state.Results
	s.loaded = true
	return nil
}

func (s *Service) saveLocked(ctx context.Context) error {
	if s.store == nil {
		return nil
	}
	state := persisted{Records: make([]Record, 0, len(s.records)), Results: s.results}
	for _, rec := range s.records {
		state.Records = append(state.Records, rec)
	}
	sort.Slice(state.Records, func(i, j int) bool { return state.Records[i].Kind < state.Records[j].Kind })
	if state.Results == nil {
		state.Results = []framework.SmokeResult{}
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return s.store.SetSetting(context.WithoutCancel(ctx), RecordsSetting, string(raw))
}
