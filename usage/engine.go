// Package usage implements the token-usage engine: it runs registered
// collectors (local + SSH-synced), normalizes records into core.UsageRecord,
// prices them via the pricing table, persists to the store, and aggregates
// into daily/weekly/monthly/session/blocks reports.
package usage

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agentnexus/agentnexus/config"
	"github.com/agentnexus/agentnexus/core"
	"github.com/agentnexus/agentnexus/store"
	"github.com/agentnexus/agentnexus/usage/parser"
	"github.com/agentnexus/agentnexus/usage/pricing"
)

// Engine coordinates collection, pricing, persistence and aggregation.
type Engine struct {
	cfg    *config.Config
	st     *store.Store
	log    *slog.Logger
	pricer *pricing.Pricer

	collectMu sync.Mutex
}

// NewEngine builds a usage Engine.
func NewEngine(cfg *config.Config, st *store.Store, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	cacheDir := cfg.Usage.CacheDir
	return &Engine{
		cfg:    cfg,
		st:     st,
		log:    log,
		pricer: pricing.New(cacheDir, cfg.Usage.Offline),
	}
}

// Collect runs all configured local collectors and persists priced records.
func (e *Engine) Collect(ctx context.Context, since time.Time) error {
	e.collectMu.Lock()
	defer e.collectMu.Unlock()
	for _, src := range e.cfg.Usage.Sources {
		col, err := parser.NewCollector(src, "", nil)
		if err != nil {
			e.log.Warn("skip source", "source", src, "err", err)
			continue
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

// Record prices and persists one request-level record emitted by the live
// AgentSession decorator. The returned cost is written back onto the trace so
// the Usage view and trace timeline use the same pricing source.
func (e *Engine) Record(ctx context.Context, record core.UsageRecord) (float64, error) {
	if record.Timestamp.IsZero() {
		record.Timestamp = time.Now().UTC()
	}
	record.CostUSD = e.pricer.Cost(record.Model,
		record.InputTokens, record.OutputTokens,
		record.CacheReadTokens, record.CacheWriteTokens)
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

// Start keeps legacy transcript-backed usage materialized without requiring a
// manual `anx usage collect`. Live request records are inserted independently.
func (e *Engine) Start(ctx context.Context, backfill time.Duration) {
	if backfill <= 0 {
		backfill = 30 * 24 * time.Hour
	}
	if err := e.Collect(ctx, time.Now().UTC().Add(-backfill)); err != nil && ctx.Err() == nil {
		e.log.Warn("initial usage collection failed", "err", err)
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
			}
		}
	}
}

func (e *Engine) price(recs []core.UsageRecord) {
	for i := range recs {
		if recs[i].CostUSD != 0 {
			continue
		}
		recs[i].CostUSD = e.pricer.Cost(recs[i].Model,
			recs[i].InputTokens, recs[i].OutputTokens,
			recs[i].CacheReadTokens, recs[i].CacheWriteTokens)
	}
}

// Report aggregates persisted usage into the requested period view.
func (e *Engine) Report(ctx context.Context, period string, since time.Time) (*Report, error) {
	detailDays := e.cfg.Observability.DetailRetentionDays
	if detailDays <= 0 {
		detailDays = 180
	}
	detailStart := time.Now().UTC().Add(-time.Duration(detailDays) * 24 * time.Hour).Truncate(24 * time.Hour).Add(24 * time.Hour)
	daily, err := e.st.QueryObservationDailyUsage(ctx, since, detailStart)
	if err != nil {
		return nil, err
	}
	detailSince := since
	if detailSince.IsZero() || detailSince.Before(detailStart) {
		detailSince = detailStart
	}
	detail, err := e.st.QueryObservationUsage(ctx, detailSince)
	if err != nil {
		return nil, err
	}
	legacy, err := e.st.QueryUsage(ctx, since)
	if err != nil {
		return nil, err
	}
	recs := mergeUsageRecords(append(daily, detail...), legacy)
	e.price(recs)
	return Aggregate(period, recs), nil
}

// mergeUsageRecords keeps observation-backed Claude/Codex requests as the
// authoritative copy while retaining legacy-only sources such as Cursor and
// Gemini. Old daily observation aggregates cover their source/model/day as a
// whole; recent detailed rows deduplicate by native request ID (or an exact
// transcript fingerprint when the runtime did not expose one).
func mergeUsageRecords(observation, legacy []core.UsageRecord) []core.UsageRecord {
	out := append([]core.UsageRecord(nil), observation...)
	dailyCoverage := map[string]bool{}
	detailCoverage := map[string]bool{}
	for _, record := range observation {
		if record.Requests > 0 && record.SessionID == "" && record.RequestID == "" {
			dailyCoverage[usageDayCoverageKey(record)] = true
			continue
		}
		detailCoverage[usageRecordIdentity(record)] = true
	}
	for _, record := range legacy {
		if dailyCoverage[usageDayCoverageKey(record)] || detailCoverage[usageRecordIdentity(record)] {
			continue
		}
		out = append(out, record)
	}
	return out
}

func usageDayCoverageKey(record core.UsageRecord) string {
	return strings.Join([]string{
		record.Timestamp.UTC().Format("2006-01-02"), normalizeUsageSource(record.Source), strings.ToLower(strings.TrimSpace(record.Model)),
	}, "\x00")
}

func usageRecordIdentity(record core.UsageRecord) string {
	source := normalizeUsageSource(record.Source)
	if requestID := strings.TrimSpace(record.RequestID); requestID != "" {
		return strings.Join([]string{"request", source, requestID}, "\x00")
	}
	return strings.Join([]string{
		"fingerprint", source, strings.TrimSpace(record.SessionID), record.Timestamp.UTC().Format(time.RFC3339Nano),
		strings.ToLower(strings.TrimSpace(record.Model)), strconv.FormatInt(record.InputTokens, 10),
		strconv.FormatInt(record.OutputTokens, 10), strconv.FormatInt(record.CacheReadTokens, 10),
		strconv.FormatInt(record.CacheWriteTokens, 10),
	}, "\x00")
}

func normalizeUsageSource(source string) string {
	source = strings.ToLower(strings.TrimSpace(source))
	switch {
	case strings.Contains(source, "claude"):
		return "claude"
	case strings.Contains(source, "codex"):
		return "codex"
	default:
		return source
	}
}
