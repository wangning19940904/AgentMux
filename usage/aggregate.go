package usage

import (
	"sort"
	"time"

	"github.com/agentnexus/agentnexus/core"
)

// Report is the aggregated usage view returned to clients.
type Report struct {
	Period  string        `json:"period"`
	Totals  Totals        `json:"totals"`
	Buckets []Bucket      `json:"buckets"`
	ByModel []ModelStat   `json:"by_model"`
	BySource []SourceStat `json:"by_source"`
}

// Totals are grand totals across all records.
type Totals struct {
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	Records          int     `json:"records"`
}

// Bucket is one period bucket (a day/week/month/session/block).
type Bucket struct {
	Key     string  `json:"key"`
	Totals  Totals  `json:"totals"`
}

// ModelStat aggregates by model.
type ModelStat struct {
	Model   string  `json:"model"`
	Tokens  int64   `json:"tokens"`
	CostUSD float64 `json:"cost_usd"`
}

// SourceStat aggregates by data source.
type SourceStat struct {
	Source  string  `json:"source"`
	Tokens  int64   `json:"tokens"`
	CostUSD float64 `json:"cost_usd"`
}

// Aggregate buckets records by the requested period.
func Aggregate(period string, recs []core.UsageRecord) *Report {
	r := &Report{Period: period}
	bucketMap := map[string]*Totals{}
	modelMap := map[string]*ModelStat{}
	sourceMap := map[string]*SourceStat{}

	for _, rec := range recs {
		addTotals(&r.Totals, rec)

		key := bucketKey(period, rec)
		bt := bucketMap[key]
		if bt == nil {
			bt = &Totals{}
			bucketMap[key] = bt
		}
		addTotals(bt, rec)

		ms := modelMap[rec.Model]
		if ms == nil {
			ms = &ModelStat{Model: rec.Model}
			modelMap[rec.Model] = ms
		}
		ms.Tokens += totalTokens(rec)
		ms.CostUSD += rec.CostUSD

		ss := sourceMap[rec.Source]
		if ss == nil {
			ss = &SourceStat{Source: rec.Source}
			sourceMap[rec.Source] = ss
		}
		ss.Tokens += totalTokens(rec)
		ss.CostUSD += rec.CostUSD
	}

	for k, t := range bucketMap {
		r.Buckets = append(r.Buckets, Bucket{Key: k, Totals: *t})
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
	return r
}

func addTotals(t *Totals, rec core.UsageRecord) {
	t.InputTokens += rec.InputTokens
	t.OutputTokens += rec.OutputTokens
	t.CacheReadTokens += rec.CacheReadTokens
	t.CacheWriteTokens += rec.CacheWriteTokens
	t.CostUSD += rec.CostUSD
	t.Records++
}

func totalTokens(rec core.UsageRecord) int64 {
	return rec.InputTokens + rec.OutputTokens + rec.CacheReadTokens + rec.CacheWriteTokens
}

func bucketKey(period string, rec core.UsageRecord) string {
	ts := rec.Timestamp
	switch period {
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
