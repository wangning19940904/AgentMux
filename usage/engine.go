// Package usage implements the token-usage engine: it runs registered
// collectors (local + SSH-synced), normalizes records into core.UsageRecord,
// prices them via the pricing table, persists to the store, and aggregates
// into daily/weekly/monthly/session/blocks reports.
package usage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
	"github.com/wangning19940904/AgentMux/usage/parser"
	"github.com/wangning19940904/AgentMux/usage/pricing"
)

// Engine coordinates collection, pricing, persistence and aggregation.
type Engine struct {
	cfg    *config.Config
	st     *store.Store
	log    *slog.Logger
	pricer *pricing.Pricer
	cursor *CursorUsageManager

	collectMu         sync.Mutex
	runtimeBackfilled map[string]bool // guarded by collectMu
}

// NewEngine builds a usage Engine.
func NewEngine(cfg *config.Config, st *store.Store, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	cacheDir := cfg.Usage.CacheDir
	engine := &Engine{
		cfg:    cfg,
		st:     st,
		log:    log,
		pricer: pricing.New(cacheDir, cfg.Usage.Offline),
	}
	if home, err := os.UserHomeDir(); err == nil {
		engine.cursor = NewCursorUsageManager(st, log, home, engine.recordBatch)
	}
	return engine
}

// Collect runs all configured local collectors and persists priced records.
// A source that fails does not stop the others, and whatever it did read is
// kept. The failure is returned so the caller reads the same window again
// rather than moving past records that were never stored.
func (e *Engine) Collect(ctx context.Context, since time.Time) error {
	e.collectMu.Lock()
	defer e.collectMu.Unlock()
	var failures []error
	for _, src := range e.cfg.Usage.Sources {
		// Cursor reads credentials and local message metadata only after the
		// explicit Connect action. Its manager owns that consent-gated path.
		if src == "cursor" {
			continue
		}
		col, err := parser.NewCollector(src, "")
		if err != nil {
			e.log.Warn("skip source", "source", src, "err", err)
			continue
		}
		if err := e.backfillUsageRuntimes(ctx, src, "", ""); err != nil {
			e.log.Warn("backfill usage clients", "source", src, "err", err)
		}
		recs, err := col.Collect(ctx, since)
		if err != nil {
			e.log.Warn("collect failed", "source", src, "err", err, "records_kept", len(recs))
			failures = append(failures, fmt.Errorf("%s: %w", src, err))
		}
		e.price(recs)
		if err := e.st.UpsertUsage(ctx, recs); err != nil {
			return err
		}
		e.log.Debug("collected", "source", src, "records", len(recs))
	}
	return errors.Join(failures...)
}

// Run once per source and host on startup, independently of the collection
// checkpoint. Old records need client metadata even when their logs have not
// changed since the last collection. Failed scans are retried on the next tick.
func (e *Engine) backfillUsageRuntimes(ctx context.Context, source, root, host string) error {
	key := source + "\x00" + host + "\x00" + root
	if e.runtimeBackfilled[key] {
		return nil
	}
	sessions, err := parser.SessionRuntimes(ctx, source, root)
	if err != nil {
		return err
	}
	if err := e.st.BackfillUsageRuntimes(ctx, source, host, sessions); err != nil {
		return err
	}
	if e.runtimeBackfilled == nil {
		e.runtimeBackfilled = map[string]bool{}
	}
	e.runtimeBackfilled[key] = true
	return nil
}

// Record prices and persists one request-level record emitted by the live
// AgentSession decorator. The returned cost is written back onto the trace so
// the Usage view and trace timeline use the same pricing source.
func (e *Engine) Record(ctx context.Context, record core.UsageRecord) (float64, error) {
	if record.Timestamp.IsZero() {
		record.Timestamp = time.Now().UTC()
	}
	if record.CostKind != core.UsageCostKindRecorded {
		record.CostKind = core.UsageCostKindCalculated
		record.CostUSD = e.pricer.Cost(record.Model,
			record.InputTokens, record.OutputTokens,
			record.CacheReadTokens, record.CacheWriteTokens)
	}
	if record.TokenQuality == "" {
		record.TokenQuality = core.UsageTokenQualityExact
	}
	if record.Provenance == "" {
		record.Provenance = "agentmux.internal"
		record.ProvenanceRank = 100
	}
	record.RuntimeID = record.UsageRuntime()
	if err := e.st.UpsertUsage(ctx, []core.UsageRecord{record}); err != nil {
		return record.CostUSD, err
	}
	return record.CostUSD, nil
}

// usageCollectCheckpointKey persists the timestamp of the last completed
// collection so a restart resumes incrementally instead of re-parsing the full
// backfill window (tens of GB of session files) on every cold start.
const usageCollectCheckpointKey = "usage:last_collect_at"

// Start keeps legacy transcript-backed usage materialized without requiring a
// manual `amux usage collect`. Live request records are inserted independently.
//
// The initial collection resumes from a persisted checkpoint (minus a small
// overlap) when one exists, so only the first run on a fresh store pays the
// full backfill cost. Subsequent restarts collect just what changed while the
// daemon was down.
func (e *Engine) Start(ctx context.Context, backfill time.Duration) {
	if e.cursor != nil {
		go e.cursor.Start(ctx)
	}
	if backfill <= 0 {
		backfill = 30 * 24 * time.Hour
	}
	since := e.initialCollectSince(ctx, backfill)
	// Each pass reads from the start of the last pass that succeeded, not from
	// "now". Parsers keep only records stamped at or after that time, so a
	// fixed look-back loses whatever was written before a pass that ran late:
	// after the machine slept, or when one pass took longer than the look-back.
	var progress collectProgress
	collect := func(since time.Time) {
		started := time.Now().UTC()
		err := e.Collect(ctx, since)
		if err != nil && ctx.Err() != nil {
			return
		}
		if err != nil {
			e.log.Warn("usage collection failed", "err", err)
		}
		if progress.finish(started, err) {
			e.saveCollectCheckpoint(ctx, started)
		}
	}
	collect(since)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			collect(nextCollectSince(progress.last, since))
		}
	}
}

// collectProgress tracks how far collection has reliably read.
type collectProgress struct {
	last         time.Time // start of the last pass collection moved past
	failingSince time.Time // start of the first failed pass since a success
}

// maxCollectRetry is how long a failing pass keeps the window open. A fault
// that clears (a file caught mid-write, a locked database) is read again in
// full. One that does not clear must not make every pass re-read a window
// that grows without end, so after this long collection moves on; whatever
// the failing source did return has been stored either way.
const maxCollectRetry = time.Hour

// finish records a pass that started at started and reports whether
// collection may move past it.
func (p *collectProgress) finish(started time.Time, err error) bool {
	if err == nil {
		p.failingSince = time.Time{}
	} else {
		if p.failingSince.IsZero() {
			p.failingSince = started
		}
		if started.Sub(p.failingSince) < maxCollectRetry {
			return false
		}
	}
	p.last = started
	return true
}

// collectOverlap is re-read on every pass. Store request IDs and the legacy
// composite key make this idempotent while tolerating late transcript writes.
const collectOverlap = 2 * time.Minute

// nextCollectSince resumes from the last successful pass, or from the initial
// window while no pass has succeeded yet.
func nextCollectSince(lastCollected, initial time.Time) time.Time {
	if lastCollected.IsZero() {
		return initial
	}
	return lastCollected.Add(-collectOverlap)
}

// initialCollectSince resolves the start time for the first collection: the
// persisted checkpoint minus a one-hour overlap when available, otherwise the
// full backfill window. The checkpoint is clamped so it can never widen the
// window beyond backfill.
func (e *Engine) initialCollectSince(ctx context.Context, backfill time.Duration) time.Time {
	full := time.Now().UTC().Add(-backfill)
	if e.st == nil {
		return full
	}
	value, ok, err := e.st.GetSetting(ctx, usageCollectCheckpointKey)
	if err != nil || !ok {
		return full
	}
	checkpoint, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return full
	}
	resume := checkpoint.Add(-time.Hour)
	if resume.Before(full) {
		return full
	}
	return resume
}

// saveCollectCheckpoint records when the pass that just succeeded started:
// records written while it ran may not have been read.
func (e *Engine) saveCollectCheckpoint(ctx context.Context, started time.Time) {
	if e.st == nil {
		return
	}
	if err := e.st.SetSetting(ctx, usageCollectCheckpointKey, started.UTC().Format(time.RFC3339Nano)); err != nil && ctx.Err() == nil {
		e.log.Warn("persist usage collect checkpoint failed", "err", err)
	}
}

func (e *Engine) price(recs []core.UsageRecord) {
	for i := range recs {
		if recs[i].TokenQuality == "" {
			recs[i].TokenQuality = core.UsageTokenQualityExact
		}
		if recs[i].CostKind == core.UsageCostKindRecorded {
			continue
		}
		recs[i].CostKind = core.UsageCostKindCalculated
		if recs[i].CostUSD != 0 {
			continue
		}
		recs[i].CostUSD = e.pricer.Cost(recs[i].Model,
			recs[i].InputTokens, recs[i].OutputTokens,
			recs[i].CacheReadTokens, recs[i].CacheWriteTokens)
	}
}

func (e *Engine) recordBatch(ctx context.Context, records []core.UsageRecord) error {
	if len(records) == 0 {
		return nil
	}
	e.price(records)
	return e.st.UpsertUsage(ctx, records)
}

// UsageSources exposes consent-gated usage collectors to the management API.
func (e *Engine) UsageSources(ctx context.Context) []CursorUsageSourceStatus {
	if e == nil || e.cursor == nil {
		return []CursorUsageSourceStatus{}
	}
	return []CursorUsageSourceStatus{e.cursor.Status(ctx)}
}

// UsageSourceAction applies a management action to a named usage source.
func (e *Engine) UsageSourceAction(ctx context.Context, source, action string) (CursorUsageActionResult, error) {
	if e == nil || e.cursor == nil || strings.ToLower(strings.TrimSpace(source)) != "cursor" {
		return CursorUsageActionResult{}, fmt.Errorf("unsupported usage source %q", source)
	}
	return e.cursor.Action(ctx, action)
}

// Report aggregates canonical usage records from since onward.
func (e *Engine) Report(ctx context.Context, period string, since time.Time) (*Report, error) {
	return e.ReportRange(ctx, period, since, time.Time{})
}

// ReportRange aggregates the canonical usage_records table inside [since,
// until). Insertion identities prevent replays of each collector, and report
// reconciliation removes matching Codex live/transcript and Cursor
// hook/dashboard observations without changing the ledger. Observation
// rollups are not added to these records.
//
// Costs are recalculated for every report with one pricing snapshot so rows
// imported under an old fallback price cannot make a mixed-price total.
func (e *Engine) ReportRange(ctx context.Context, period string, since, until time.Time) (*Report, error) {
	return e.ReportRangeInLocation(ctx, period, since, until, time.Local)
}

// ReportRangeInLocation aggregates usage in the caller-selected reporting
// timezone. Query bounds remain absolute instants while bucket labels and the
// reported date range use location, so fleet reports align across hosts whose
// process-local timezone differs.
func (e *Engine) ReportRangeInLocation(ctx context.Context, period string, since, until time.Time, location *time.Location) (*Report, error) {
	return e.ReportQuery(ctx, ReportQuery{Period: period, Since: since, Until: until, Location: location})
}

// ReportQuery selects what a report covers: a period to bucket by, an
// absolute time range [Since, Until) (zero = unbounded), the timezone bucket
// labels use, and optional dimension filters.
type ReportQuery struct {
	Period   string
	Since    time.Time
	Until    time.Time
	Location *time.Location
	Filter   ReportFilter
}

// reconciliationFetchMargin is how far outside the requested range records are
// read so that both observations of one request are seen together. It covers
// the widest pairing window of the reconcilers.
const reconciliationFetchMargin = claudeReplyGap

// ReportQuery aggregates the records a query selects. The report's facets
// list every model, framework and agent seen in the time range before the
// dimension filters apply, so a client can offer them as filter choices.
func (e *Engine) ReportQuery(ctx context.Context, query ReportQuery) (*Report, error) {
	location := query.Location
	if location == nil {
		location = time.Local
	}
	since, until := query.Since, query.Until
	// Fetch the delivery overlap before matching. Otherwise one observation
	// just before midnight and its counterpart just after midnight would
	// survive separately in adjacent daily reports.
	querySince, queryUntil := since, until
	if !querySince.IsZero() {
		querySince = querySince.Add(-reconciliationFetchMargin)
	}
	if !queryUntil.IsZero() {
		queryUntil = queryUntil.Add(reconciliationFetchMargin)
	}
	recs, err := e.st.QueryUsageRange(ctx, querySince, queryUntil)
	if err != nil {
		return nil, err
	}
	recs = reconcileCodexUsage(recs)
	recs = reconcileCursorUsage(recs)
	recs = reconcileClaudeUsage(recs)
	bounded := recs[:0]
	for _, record := range recs {
		if (!since.IsZero() && record.Timestamp.Before(since)) || (!until.IsZero() && !record.Timestamp.Before(until)) {
			continue
		}
		bounded = append(bounded, record)
	}
	recs = bounded
	// Filters apply after reconciliation: a duplicate observation must be
	// dropped even when only its counterpart matches the filter.
	facets := usageFacets(recs)
	recs = query.Filter.apply(recs)
	e.reprice(recs)
	for i := range recs {
		recs[i].Timestamp = recs[i].Timestamp.In(location)
	}
	report := Aggregate(query.Period, recs)
	report.Facets = facets
	if !since.IsZero() {
		report.From = since.In(location).Format("2006-01-02")
	}
	if !until.IsZero() {
		report.To = until.In(location).AddDate(0, 0, -1).Format("2006-01-02")
	}
	report.Timezone = location.String()
	return report, nil
}

func (e *Engine) reprice(recs []core.UsageRecord) {
	for i := range recs {
		if recs[i].CostKind == core.UsageCostKindRecorded {
			continue
		}
		recs[i].CostUSD = e.pricer.Cost(recs[i].Model,
			recs[i].InputTokens, recs[i].OutputTokens,
			recs[i].CacheReadTokens, recs[i].CacheWriteTokens)
	}
}
