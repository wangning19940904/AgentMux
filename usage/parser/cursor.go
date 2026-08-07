package parser

import (
	"context"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// cursorCollector is a declared-but-inert source: Cursor's local state
// database (state.vscdb) does not expose a stable per-call token schema
// across versions, so rather than emit misleading numbers this collector
// yields nothing. It exists so "cursor" stays a valid configured source and
// version-specific extraction can be added without touching the collector
// contract.
type cursorCollector struct {
	root string
}

func (c *cursorCollector) Source() string { return "cursor" }

func (c *cursorCollector) Collect(ctx context.Context, since time.Time) ([]core.UsageRecord, error) {
	return nil, nil
}
