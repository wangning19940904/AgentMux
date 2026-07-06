package core

import (
	"log/slog"
	"sync"

	"github.com/robfig/cron/v3"
)

// ValidateCronExpr checks a standard 5-field cron expression
// (minute hour day-of-month month day-of-week, plus @hourly-style macros).
func ValidateCronExpr(expr string) error {
	_, err := cron.ParseStandard(expr)
	return err
}

// Scheduler drives cron triggers with robfig/cron/v3 (the same engine
// cc-connect uses). Executions are delegated to the run callback so run
// bookkeeping and channel resolution stay in the ConnectService.
type Scheduler struct {
	log  *slog.Logger
	run  func(triggerID string)
	cron *cron.Cron

	mu      sync.Mutex
	entries map[string]cron.EntryID
	specs   map[string]string
}

// NewScheduler builds a stopped scheduler; call Start after the first Sync.
func NewScheduler(log *slog.Logger, run func(triggerID string)) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{
		log:     log,
		run:     run,
		cron:    cron.New(),
		entries: map[string]cron.EntryID{},
		specs:   map[string]string{},
	}
}

// Start begins firing scheduled entries.
func (s *Scheduler) Start() { s.cron.Start() }

// Stop halts the scheduler; running jobs are not interrupted.
func (s *Scheduler) Stop() { s.cron.Stop() }

// Sync reconciles scheduled entries with the given triggers: only enabled
// cron triggers are kept, changed expressions are rescheduled, everything
// else is removed. Safe to call while running.
func (s *Scheduler) Sync(triggers []Trigger) {
	desired := map[string]string{}
	for _, tr := range triggers {
		if tr.Kind == TriggerCron && tr.Enabled && tr.CronExpr != "" {
			desired[tr.ID] = tr.CronExpr
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, entryID := range s.entries {
		if spec, ok := desired[id]; !ok || spec != s.specs[id] {
			s.cron.Remove(entryID)
			delete(s.entries, id)
			delete(s.specs, id)
		}
	}
	for id, spec := range desired {
		if _, ok := s.entries[id]; ok {
			continue
		}
		id := id
		entryID, err := s.cron.AddFunc(spec, func() { s.run(id) })
		if err != nil {
			s.log.Error("schedule trigger", "trigger_id", id, "cron", spec, "err", err)
			continue
		}
		s.entries[id] = entryID
		s.specs[id] = spec
	}
}

// Scheduled returns the number of active cron entries (for status surfaces).
func (s *Scheduler) Scheduled() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}
