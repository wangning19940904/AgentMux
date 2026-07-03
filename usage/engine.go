// Package usage implements the token-usage engine: it runs registered
// collectors (local + SSH-synced), normalizes records into core.UsageRecord,
// prices them via the pricing table, persists to the store, and aggregates
// into daily/weekly/monthly/session/blocks reports.
package usage

import (
	"context"
	"log/slog"
	"time"

	"github.com/agentnexus/agentnexus/config"
	"github.com/agentnexus/agentnexus/core"
	"github.com/agentnexus/agentnexus/store"
	"github.com/agentnexus/agentnexus/usage/parser"
	"github.com/agentnexus/agentnexus/usage/pricing"
)

// Engine coordinates collection, pricing, persistence and aggregation.
type Engine struct {
	cfg     *config.Config
	st      *store.Store
	log     *slog.Logger
	pricer  *pricing.Pricer
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

func (e *Engine) price(recs []core.UsageRecord) {
	for i := range recs {
		recs[i].CostUSD = e.pricer.Cost(recs[i].Model,
			recs[i].InputTokens, recs[i].OutputTokens,
			recs[i].CacheReadTokens, recs[i].CacheWriteTokens)
	}
}

// Report aggregates persisted usage into the requested period view.
func (e *Engine) Report(ctx context.Context, period string, since time.Time) (*Report, error) {
	recs, err := e.st.QueryUsage(ctx, since)
	if err != nil {
		return nil, err
	}
	return Aggregate(period, recs), nil
}
