package usage

import (
	"sort"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// Claude Code writes one transcript line per content block of a reply, each
// repeating the reply's whole usage. Rows collected before the parser kept
// the reply's ID hold one record per line, so a reply with three blocks was
// counted three times. Those rows stay in the ledger; a report keeps one row
// per reply.
//
// Lines of one reply share the session, the model and all four token counts
// and are written while the reply streams. Different replies of one session
// practically never share all four counts (the cache-read count grows with
// every turn), and the gap bound keeps apart the rare ones that do.
const claudeReplyGap = 5 * time.Minute

// claudeSubagentProject is what the parser used to record for a sub-agent's
// transcript: the name of the directory it sits in instead of the project.
const claudeSubagentProject = "subagents"

func reconcileClaudeUsage(records []core.UsageRecord) []core.UsageRecord {
	type fingerprint struct {
		host, session, model                 string
		input, output, cacheRead, cacheWrite int64
	}
	groups := make(map[fingerprint][]int)
	sessionProject := make(map[[2]string]string)
	subagentRows := false
	for i, record := range records {
		if record.Source != "claude" {
			continue
		}
		if record.Project == claudeSubagentProject {
			subagentRows = true
		} else if record.Project != "" && record.SessionID != "" {
			sessionProject[[2]string{record.Host, record.SessionID}] = record.Project
		}
		if record.SessionID == "" || record.Timestamp.IsZero() || totalTokens(record) == 0 {
			continue
		}
		key := fingerprint{record.Host, record.SessionID, record.Model,
			record.InputTokens, record.OutputTokens, record.CacheReadTokens, record.CacheWriteTokens}
		groups[key] = append(groups[key], i)
	}
	duplicates := make(map[int]bool)
	for _, indices := range groups {
		if len(indices) < 2 {
			continue
		}
		sort.SliceStable(indices, func(i, j int) bool {
			return records[indices[i]].Timestamp.Before(records[indices[j]].Timestamp)
		})
		// Walk runs of rows written close together: each run is one reply.
		for start := 0; start < len(indices); {
			end := start + 1
			for end < len(indices) && records[indices[end]].Timestamp.Sub(records[indices[end-1]].Timestamp) <= claudeReplyGap {
				end++
			}
			dropClaudeReplyDuplicates(records, indices[start:end], duplicates)
			start = end
		}
	}
	if len(duplicates) == 0 && !subagentRows {
		return records
	}
	result := make([]core.UsageRecord, 0, len(records)-len(duplicates))
	for i, record := range records {
		if duplicates[i] {
			continue
		}
		// A sub-agent shares its parent's session, which names the project.
		if record.Source == "claude" && record.Project == claudeSubagentProject {
			if project := sessionProject[[2]string{record.Host, record.SessionID}]; project != "" {
				record.Project = project
			}
		}
		result = append(result, record)
	}
	return result
}

// dropClaudeReplyDuplicates keeps one row of a run. Rows that carry a reply
// ID are exact: each distinct ID is a reply of its own and stays, and the
// rows without an ID are then the same replies seen by the older parser.
func dropClaudeReplyDuplicates(records []core.UsageRecord, run []int, duplicates map[int]bool) {
	identified := false
	for _, index := range run {
		if records[index].RequestID != "" {
			identified = true
			break
		}
	}
	keptLegacy := false
	for _, index := range run {
		if records[index].RequestID != "" {
			continue
		}
		if identified || keptLegacy {
			duplicates[index] = true
			continue
		}
		keptLegacy = true
	}
}
