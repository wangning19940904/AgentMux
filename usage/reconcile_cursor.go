package usage

import (
	"sort"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// Cursor's dashboard may omit the generation ID carried by a Stop hook. In
// that case its synthetic request ID cannot upgrade the hook at insertion.
// Reconcile only corroborated token observations and retain the dashboard's
// recorded amount. An unmatched hook is left untouched: its input convention
// must not be guessed from the model name or token values alone.
func reconcileCursorUsage(records []core.UsageRecord) []core.UsageRecord {
	type fingerprint struct {
		host, session, model          string
		output, cacheRead, cacheWrite int64
	}
	type candidates struct{ dashboard, hook []int }
	groups := make(map[fingerprint]*candidates)
	for i, record := range records {
		if record.Source != "cursor" || record.SessionID == "" || record.Model == "" ||
			record.Timestamp.IsZero() || record.TokenQuality != core.UsageTokenQualityExact || totalTokens(record) <= 0 {
			continue
		}
		if record.Provenance != cursorProvenanceDashboard && record.Provenance != cursorProvenanceHook {
			continue
		}
		key := fingerprint{record.Host, record.SessionID, cursorUsageModel(record.Model),
			record.OutputTokens, record.CacheReadTokens, record.CacheWriteTokens}
		group := groups[key]
		if group == nil {
			group = &candidates{}
			groups[key] = group
		}
		if record.Provenance == cursorProvenanceDashboard {
			group.dashboard = append(group.dashboard, i)
		} else {
			group.hook = append(group.hook, i)
		}
	}
	duplicates := make(map[int]bool)
	for _, group := range groups {
		sort.SliceStable(group.hook, func(i, j int) bool {
			return records[group.hook[i]].Timestamp.Before(records[group.hook[j]].Timestamp)
		})
		used := make(map[int]bool)
		for _, hookIndex := range group.hook {
			hook := records[hookIndex]
			best, bestDelay := -1, usageReconciliationWindow+time.Nanosecond
			for _, dashboardIndex := range group.dashboard {
				if used[dashboardIndex] {
					continue
				}
				dashboard := records[dashboardIndex]
				if hook.InputTokens != dashboard.InputTokens &&
					hook.InputTokens != dashboard.InputTokens+dashboard.CacheReadTokens+dashboard.CacheWriteTokens {
					continue
				}
				delay := hook.Timestamp.Sub(dashboard.Timestamp).Abs()
				if delay < bestDelay {
					best, bestDelay = dashboardIndex, delay
				}
			}
			if best >= 0 {
				used[best], duplicates[hookIndex] = true, true
			}
		}
	}
	if len(duplicates) == 0 {
		return records
	}
	result := make([]core.UsageRecord, 0, len(records)-len(duplicates))
	for i, record := range records {
		if !duplicates[i] {
			result = append(result, record)
		}
	}
	return result
}

func cursorUsageModel(model string) string {
	// Claude dashboard names add the effort to the base model ID used by
	// Stop hooks. Restrict this normalization to that known model family;
	// do not collapse arbitrary model variants such as Grok high-fast.
	if strings.HasPrefix(model, "claude-") {
		for _, effort := range []string{"-low", "-medium", "-high", "-max"} {
			if strings.HasSuffix(model, effort) {
				return strings.TrimSuffix(model, effort)
			}
		}
	}
	return model
}
