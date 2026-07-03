package usage

import (
	"fmt"
	"strings"
	"time"
)

// ParseSince parses a relative ("7d","2w","3m") or absolute ("2006-01-02")
// since expression into an absolute time.
func ParseSince(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	if len(s) >= 2 {
		unit := s[len(s)-1]
		numStr := s[:len(s)-1]
		var n int
		if _, err := fmt.Sscanf(numStr, "%d", &n); err == nil {
			now := time.Now()
			switch unit {
			case 'd':
				return now.AddDate(0, 0, -n), nil
			case 'w':
				return now.AddDate(0, 0, -7*n), nil
			case 'm':
				return now.AddDate(0, -n, 0), nil
			case 'y':
				return now.AddDate(-n, 0, 0), nil
			}
		}
	}
	return time.Time{}, fmt.Errorf("invalid since expression %q", s)
}

// RenderTable renders a Report as a terminal-friendly table.
func RenderTable(r *Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Usage report (%s)\n", r.Period)
	fmt.Fprintf(&b, "%s\n", strings.Repeat("-", 64))
	fmt.Fprintf(&b, "Total: in %s / out %s / cache-r %s / cache-w %s  =  $%.2f  (%d records)\n\n",
		humanTokens(r.Totals.InputTokens), humanTokens(r.Totals.OutputTokens),
		humanTokens(r.Totals.CacheReadTokens), humanTokens(r.Totals.CacheWriteTokens),
		r.Totals.CostUSD, r.Totals.Records)

	fmt.Fprintf(&b, "%-22s %12s %12s %10s\n", r.Period, "in", "out", "cost")
	for _, bucket := range r.Buckets {
		fmt.Fprintf(&b, "%-22s %12s %12s %9.2f$\n", bucket.Key,
			humanTokens(bucket.Totals.InputTokens),
			humanTokens(bucket.Totals.OutputTokens),
			bucket.Totals.CostUSD)
	}

	if len(r.ByModel) > 0 {
		fmt.Fprintf(&b, "\nBy model:\n")
		for _, m := range r.ByModel {
			fmt.Fprintf(&b, "  %-32s %12s  $%.2f\n", m.Model, humanTokens(m.Tokens), m.CostUSD)
		}
	}
	if len(r.BySource) > 0 {
		fmt.Fprintf(&b, "\nBy source:\n")
		for _, s := range r.BySource {
			fmt.Fprintf(&b, "  %-12s %12s  $%.2f\n", s.Source, humanTokens(s.Tokens), s.CostUSD)
		}
	}
	return b.String()
}

func humanTokens(n int64) string {
	f := float64(n)
	switch {
	case f >= 1e9:
		return fmt.Sprintf("%.2fB", f/1e9)
	case f >= 1e6:
		return fmt.Sprintf("%.2fM", f/1e6)
	case f >= 1e3:
		return fmt.Sprintf("%.2fK", f/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}
