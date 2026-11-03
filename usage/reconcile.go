package usage

import (
	"sort"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// Codex app-server notifications can reach the live recorder after the same
// token_count has been written to the rollout. They do not share a request ID.
// Match only identical token components in the same native session, allowing
// a bounded delivery delay. Each live request can replace just one transcript
// row; missing live requests and transcript-only tails remain in the report.
const usageReconciliationWindow = time.Minute

func reconcileCodexUsage(records []core.UsageRecord) []core.UsageRecord {
	type fingerprint struct {
		host, session, model                 string
		input, output, cacheRead, cacheWrite int64
	}
	type candidates struct{ live, transcript []int }
	groups := make(map[fingerprint]*candidates)
	for i, record := range records {
		if record.Source != "codex" || record.SessionID == "" || record.Timestamp.IsZero() || totalTokens(record) == 0 {
			continue
		}
		live := record.RequestID != "" && (record.Provenance == "agentmux.internal" || record.Provenance == "agentmux.proxy")
		transcript := record.RequestID == "" && record.Provenance == ""
		if !live && !transcript {
			continue
		}
		key := fingerprint{record.Host, record.SessionID, record.Model,
			record.InputTokens, record.OutputTokens, record.CacheReadTokens, record.CacheWriteTokens}
		group := groups[key]
		if group == nil {
			group = &candidates{}
			groups[key] = group
		}
		if live {
			group.live = append(group.live, i)
		} else {
			group.transcript = append(group.transcript, i)
		}
	}
	duplicates := make(map[int]bool)
	less := func(indices []int) func(int, int) bool {
		return func(i, j int) bool { return records[indices[i]].Timestamp.Before(records[indices[j]].Timestamp) }
	}
	for _, group := range groups {
		sort.SliceStable(group.live, less(group.live))
		sort.SliceStable(group.transcript, less(group.transcript))
		// Pair in time order to retain the maximum number of one-to-one
		// matches even when several requests have identical token counts.
		for l, t := 0, 0; l < len(group.live) && t < len(group.transcript); {
			delay := records[group.live[l]].Timestamp.Sub(records[group.transcript[t]].Timestamp)
			switch {
			case delay > usageReconciliationWindow:
				t++
			case delay < -usageReconciliationWindow:
				l++
			default:
				duplicates[group.transcript[t]] = true
				l++
				t++
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
