package usage

import (
	"context"
	"fmt"
	"time"
)

// Statusline renders a compact one-line usage summary suitable for a Claude
// Code statusline hook (e.g. "$12.34 today · 1.2M tok"). It aggregates today's
// records across all sources.
func (e *Engine) Statusline(ctx context.Context) (string, error) {
	// Local midnight: Truncate works in UTC, which starts "today" hours early
	// or late everywhere else.
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	rep, err := e.Report(ctx, "daily", start)
	if err != nil {
		return "", err
	}
	tok := rep.Totals.InputTokens + rep.Totals.OutputTokens +
		rep.Totals.CacheReadTokens + rep.Totals.CacheWriteTokens
	return fmt.Sprintf("$%.2f today · %s tok", rep.Totals.CostUSD, humanTokens(tok)), nil
}
