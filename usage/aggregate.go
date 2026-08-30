package usage

import (
	"sort"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// Report is the aggregated usage view returned to clients.
type Report struct {
	Period    string        `json:"period"`
	From      string        `json:"from,omitempty"`
	To        string        `json:"to,omitempty"`
	Timezone  string        `json:"timezone,omitempty"`
	Totals    Totals        `json:"totals"`
	Buckets   []Bucket      `json:"buckets"`
	ByModel   []ModelStat   `json:"by_model"`
	BySource  []SourceStat  `json:"by_source"`
	ByAgent   []AgentStat   `json:"by_agent,omitempty"`
	ByRuntime []RuntimeStat `json:"by_runtime,omitempty"`
}

// Totals are grand totals across all records.
type Totals struct {
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	Records          int     `json:"records"`  // model request count
	Sessions         int     `json:"sessions"` // distinct non-empty sessions
	EstimatedTokens  int64   `json:"estimated_tokens"`
	EstimatedRecords int     `json:"estimated_records"`
}

// Bucket is one period bucket (a day/week/month/session/block).
type Bucket struct {
	Key       string        `json:"key"`
	Totals    Totals        `json:"totals"`
	ByRuntime []RuntimeStat `json:"by_runtime,omitempty"`
}

// ModelStat aggregates by model.
type ModelStat struct {
	Model           string  `json:"model"`
	Tokens          int64   `json:"tokens"`
	CostUSD         float64 `json:"cost_usd"`
	Records         int     `json:"records"`
	EstimatedTokens int64   `json:"estimated_tokens,omitempty"`
}

// SourceStat aggregates by data source.
type SourceStat struct {
	Source          string  `json:"source"`
	Tokens          int64   `json:"tokens"`
	CostUSD         float64 `json:"cost_usd"`
	Records         int     `json:"records"`
	EstimatedTokens int64   `json:"estimated_tokens,omitempty"`
}

// AgentStat aggregates by agent (the project/agent id on each record).
type AgentStat struct {
	Agent    string  `json:"agent"`
	Tokens   int64   `json:"tokens"`
	CostUSD  float64 `json:"cost_usd"`
	Records  int     `json:"records"`
	Sessions int     `json:"sessions"`
}

// RuntimeStat aggregates by runtime/framework (claude, codex, ...).
type RuntimeStat struct {
	Runtime         string  `json:"runtime"`
	Tokens          int64   `json:"tokens"`
	CostUSD         float64 `json:"cost_usd"`
	Records         int     `json:"records"`
	EstimatedTokens int64   `json:"estimated_tokens,omitempty"`
}

// Aggregate buckets records by the requested period.
func Aggregate(period string, recs []core.UsageRecord) *Report {
	r := &Report{
		Period:    period,
		Buckets:   []Bucket{},
		ByModel:   []ModelStat{},
		BySource:  []SourceStat{},
		ByAgent:   []AgentStat{},
		ByRuntime: []RuntimeStat{},
	}
	bucketMap := map[string]*Totals{}
	modelMap := map[string]*ModelStat{}
	sourceMap := map[string]*SourceStat{}
	agentMap := map[string]*AgentStat{}
	runtimeMap := map[string]*RuntimeStat{}
	bucketRuntimeMap := map[string]map[string]*RuntimeStat{}
	sessionSet := map[string]struct{}{}
	bucketSessionSets := map[string]map[string]struct{}{}
	agentSessionSets := map[string]map[string]struct{}{}

	for _, rec := range recs {
		addTotals(&r.Totals, rec)
		recCount := recordCount(rec)
		session := usageSessionKey(rec)
		if session != "" && addUnique(sessionSet, session) {
			r.Totals.Sessions++
		}

		key := bucketKey(period, rec)
		bt := bucketMap[key]
		if bt == nil {
			bt = &Totals{}
			bucketMap[key] = bt
		}
		addTotals(bt, rec)
		if session != "" {
			set := bucketSessionSets[key]
			if set == nil {
				set = map[string]struct{}{}
				bucketSessionSets[key] = set
			}
			if addUnique(set, session) {
				bt.Sessions++
			}
		}

		ms := modelMap[rec.Model]
		if ms == nil {
			ms = &ModelStat{Model: rec.Model}
			modelMap[rec.Model] = ms
		}
		ms.Tokens += totalTokens(rec)
		ms.CostUSD += rec.CostUSD
		ms.Records += recCount
		if rec.TokenQuality == core.UsageTokenQualityEstimated {
			ms.EstimatedTokens += totalTokens(rec)
		}

		ss := sourceMap[rec.Source]
		if ss == nil {
			ss = &SourceStat{Source: rec.Source}
			sourceMap[rec.Source] = ss
		}
		ss.Tokens += totalTokens(rec)
		ss.CostUSD += rec.CostUSD
		ss.Records += recCount
		if rec.TokenQuality == core.UsageTokenQualityEstimated {
			ss.EstimatedTokens += totalTokens(rec)
		}

		agentKey := rec.Project
		if agentKey == "" {
			agentKey = "unknown"
		}
		as := agentMap[agentKey]
		if as == nil {
			as = &AgentStat{Agent: agentKey}
			agentMap[agentKey] = as
		}
		as.Tokens += totalTokens(rec)
		as.CostUSD += rec.CostUSD
		as.Records += recCount
		if session != "" {
			set := agentSessionSets[agentKey]
			if set == nil {
				set = map[string]struct{}{}
				agentSessionSets[agentKey] = set
			}
			if addUnique(set, session) {
				as.Sessions++
			}
		}

		runtimeKey := rec.RuntimeID
		if runtimeKey == "" {
			runtimeKey = rec.Source
		}
		if runtimeKey == "" {
			runtimeKey = "unknown"
		}
		rs := runtimeMap[runtimeKey]
		if rs == nil {
			rs = &RuntimeStat{Runtime: runtimeKey}
			runtimeMap[runtimeKey] = rs
		}
		rs.Tokens += totalTokens(rec)
		rs.CostUSD += rec.CostUSD
		rs.Records += recCount
		if rec.TokenQuality == core.UsageTokenQualityEstimated {
			rs.EstimatedTokens += totalTokens(rec)
		}

		bucketRuntimes := bucketRuntimeMap[key]
		if bucketRuntimes == nil {
			bucketRuntimes = map[string]*RuntimeStat{}
			bucketRuntimeMap[key] = bucketRuntimes
		}
		bucketRuntime := bucketRuntimes[runtimeKey]
		if bucketRuntime == nil {
			bucketRuntime = &RuntimeStat{Runtime: runtimeKey}
			bucketRuntimes[runtimeKey] = bucketRuntime
		}
		bucketRuntime.Tokens += totalTokens(rec)
		bucketRuntime.CostUSD += rec.CostUSD
		bucketRuntime.Records += recCount
		if rec.TokenQuality == core.UsageTokenQualityEstimated {
			bucketRuntime.EstimatedTokens += totalTokens(rec)
		}
	}

	for k, t := range bucketMap {
		bucket := Bucket{Key: k, Totals: *t, ByRuntime: []RuntimeStat{}}
		for _, runtime := range bucketRuntimeMap[k] {
			bucket.ByRuntime = append(bucket.ByRuntime, *runtime)
		}
		sort.Slice(bucket.ByRuntime, func(i, j int) bool { return bucket.ByRuntime[i].Tokens > bucket.ByRuntime[j].Tokens })
		r.Buckets = append(r.Buckets, bucket)
	}
	sort.Slice(r.Buckets, func(i, j int) bool { return r.Buckets[i].Key < r.Buckets[j].Key })
	for _, ms := range modelMap {
		r.ByModel = append(r.ByModel, *ms)
	}
	sort.Slice(r.ByModel, func(i, j int) bool { return r.ByModel[i].CostUSD > r.ByModel[j].CostUSD })
	for _, ss := range sourceMap {
		r.BySource = append(r.BySource, *ss)
	}
	sort.Slice(r.BySource, func(i, j int) bool { return r.BySource[i].CostUSD > r.BySource[j].CostUSD })
	for _, as := range agentMap {
		r.ByAgent = append(r.ByAgent, *as)
	}
	sort.Slice(r.ByAgent, func(i, j int) bool { return r.ByAgent[i].CostUSD > r.ByAgent[j].CostUSD })
	for _, rs := range runtimeMap {
		r.ByRuntime = append(r.ByRuntime, *rs)
	}
	sort.Slice(r.ByRuntime, func(i, j int) bool { return r.ByRuntime[i].CostUSD > r.ByRuntime[j].CostUSD })
	return r
}

func addTotals(t *Totals, rec core.UsageRecord) {
	t.InputTokens += rec.InputTokens
	t.OutputTokens += rec.OutputTokens
	t.CacheReadTokens += rec.CacheReadTokens
	t.CacheWriteTokens += rec.CacheWriteTokens
	t.CostUSD += rec.CostUSD
	requests := rec.Requests
	if requests <= 0 {
		requests = 1
	}
	t.Records += int(requests)
	if rec.TokenQuality == core.UsageTokenQualityEstimated {
		t.EstimatedTokens += totalTokens(rec)
		t.EstimatedRecords += int(requests)
	}
}

func totalTokens(rec core.UsageRecord) int64 {
	return rec.InputTokens + rec.OutputTokens + rec.CacheReadTokens + rec.CacheWriteTokens
}

// recordCount mirrors addTotals: a record represents at least one message,
// but may carry an explicit request count.
func recordCount(rec core.UsageRecord) int {
	requests := rec.Requests
	if requests <= 0 {
		requests = 1
	}
	return int(requests)
}

// usageSessionKey keeps session identities distinct across machines and
// frameworks while collapsing every model request inside one session.
func usageSessionKey(rec core.UsageRecord) string {
	sessionID := strings.TrimSpace(rec.SessionID)
	if sessionID == "" {
		return ""
	}
	source := strings.TrimSpace(rec.Source)
	if source == "" {
		source = strings.TrimSpace(rec.RuntimeID)
	}
	return strings.Join([]string{strings.TrimSpace(rec.Host), source, sessionID}, "\x00")
}

func addUnique(set map[string]struct{}, value string) bool {
	if _, exists := set[value]; exists {
		return false
	}
	set[value] = struct{}{}
	return true
}

func bucketKey(period string, rec core.UsageRecord) string {
	ts := rec.Timestamp
	switch period {
	case "hourly":
		return ts.Format("2006-01-02 15:00")
	case "weekly":
		y, w := ts.ISOWeek()
		return isoWeek(y, w)
	case "monthly":
		return ts.Format("2006-01")
	case "session":
		return rec.Source + ":" + rec.SessionID
	case "blocks":
		// 5-hour billing windows aligned to the hour.
		block := ts.Truncate(5 * time.Hour)
		return block.Format("2006-01-02 15:00")
	default: // daily
		return ts.Format("2006-01-02")
	}
}

func isoWeek(y, w int) string {
	ws := time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
	_ = ws
	return formatISOWeek(y, w)
}

func formatISOWeek(y, w int) string {
	ww := w
	prefix := "W"
	if ww < 10 {
		return itoa(y) + "-" + prefix + "0" + itoa(ww)
	}
	return itoa(y) + "-" + prefix + itoa(ww)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
