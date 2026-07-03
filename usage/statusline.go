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
	start := time.Now().Truncate(24 * time.Hour)
	rep, err := e.Report(ctx, "daily", start)
	if err != nil {
		return "", err
	}
	tok := rep.Totals.InputTokens + rep.Totals.OutputTokens +
		rep.Totals.CacheReadTokens + rep.Totals.CacheWriteTokens
	return fmt.Sprintf("$%.2f today · %s tok", rep.Totals.CostUSD, humanTokens(tok)), nil
}
