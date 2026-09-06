// Package usage implements the token-usage engine: it runs registered
// collectors (local + SSH-synced), normalizes records into core.UsageRecord,
// prices them via the pricing table, persists to the store, and aggregates
// into daily/weekly/monthly/session/blocks reports.
package usage

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
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
func (e *Engine) Collect(ctx context.Context, since time.Time) error {
	e.collectMu.Lock()
	defer e.collectMu.Unlock()
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
			e.log.Warn("collect failed", "source", src, "err", err)
			continue
		}
		e.price(recs)
		if err := e.st.UpsertUsage(ctx, recs); err != nil {
			return err
		}
		e.log.Debug("collected", "source", src, "records", len(recs))
	}
	return nil
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
	if err := e.st.UpsertUsage(ctx, []core.UsageRecord{record}); err != nil {
		return record.CostUSD, err
	}
	return record.CostUSD, nil
}

// ProxyCost applies the same pricing table to one local-routing attempt.
func (e *Engine) ProxyCost(trace core.ProxyTrace) float64 {
	return e.pricer.Cost(trace.UpstreamModel,
		trace.InputTokens, trace.OutputTokens,
		trace.CacheReadTokens, trace.CacheWriteTokens)
}

// RecordProxy materializes one failover attempt for the compatibility Usage
// API. Attempt is part of the request key so retries are counted separately.
func (e *Engine) RecordProxy(ctx context.Context, trace core.ProxyTrace) error {
	requestID := trace.RequestID
	if requestID != "" && trace.Attempt > 0 {
		requestID += ":attempt:" + strconv.Itoa(trace.Attempt)
	}
	_, err := e.Record(ctx, core.UsageRecord{
		Source: proxyUsageSource(trace.Tool), SessionID: trace.SessionID,
		TraceID: trace.TraceID, RequestID: requestID, RuntimeID: trace.Tool,
		Project: trace.ProjectDir, Model: trace.UpstreamModel, Timestamp: trace.Timestamp,
		InputTokens: trace.InputTokens, OutputTokens: trace.OutputTokens,
		CacheReadTokens: trace.CacheReadTokens, CacheWriteTokens: trace.CacheWriteTokens,
	})
	return err
}

func proxyUsageSource(tool string) string {
	value := strings.ToLower(strings.TrimSpace(tool))
	switch {
	case strings.Contains(value, "claude"):
		return "claude"
	case strings.Contains(value, "codex"):
		return "codex"
	default:
		return value
	}
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
	if err := e.Collect(ctx, since); err != nil && ctx.Err() == nil {
		e.log.Warn("initial usage collection failed", "err", err)
	} else if err == nil {
		e.saveCollectCheckpoint(ctx)
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			// Re-read a small overlap. Store request IDs and the legacy composite
			// key make this idempotent while tolerating late transcript writes.
			if err := e.Collect(ctx, now.UTC().Add(-2*time.Minute)); err != nil && ctx.Err() == nil {
				e.log.Warn("incremental usage collection failed", "err", err)
			} else if err == nil {
				e.saveCollectCheckpoint(ctx)
			}
		}
	}
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

func (e *Engine) saveCollectCheckpoint(ctx context.Context) {
	if e.st == nil {
		return
	}
	if err := e.st.SetSetting(ctx, usageCollectCheckpointKey, time.Now().UTC().Format(time.RFC3339Nano)); err != nil && ctx.Err() == nil {
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
// until). usage_records is already protected by request and transcript
// identities at insertion time. Keeping the ledger on this one source avoids
// mixing legacy rows with observation rollups of the same transcripts.
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
	if location == nil {
		location = time.Local
	}
	recs, err := e.st.QueryUsageRange(ctx, since, until)
	if err != nil {
		return nil, err
	}
	e.reprice(recs)
	for i := range recs {
		recs[i].Timestamp = recs[i].Timestamp.In(location)
	}
	report := Aggregate(period, recs)
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
