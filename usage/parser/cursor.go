package parser

import (
	"context"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// cursorCollector remains inert by design because Cursor collection requires
// explicit user consent before reading its local login state. The usage-level
// CursorUsageManager owns the read-only SQLite, native hook, API enrichment,
// quality labeling and checkpointing workflow; keeping this adapter registered
// preserves configuration compatibility without bypassing that consent gate.
type cursorCollector struct {
	root string
}

func (c *cursorCollector) Source() string { return "cursor" }

func (c *cursorCollector) Collect(ctx context.Context, since time.Time) ([]core.UsageRecord, error) {
	return nil, nil
}
