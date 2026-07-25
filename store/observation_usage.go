package store

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// QueryObservationUsage materializes request-level usage from finalized model
// spans. For a native request observed by multiple sources it selects the
// highest quality record, so OTel/Proxy/Transcript replay cannot double-count
// the authoritative in-process adapter while retries and later requests in the
// same turn remain distinct.
func (s *Store) QueryObservationUsage(ctx context.Context, since time.Time) ([]core.UsageRecord, error) {
	query := `SELECT sp.trace_id,sp.span_id,sp.session_id,sp.conversation_id,sp.turn_id,sp.runtime_id,
		sp.source,sp.started_at,sp.model_json,sp.input_tokens,sp.output_tokens,
		sp.cache_read_tokens,sp.cache_write_tokens,sp.cost_usd,tr.agent_id
		FROM observation_spans sp JOIN observation_traces tr ON tr.trace_id=sp.trace_id
		WHERE sp.kind='model.request' AND (sp.total_tokens>0 OR sp.input_tokens>0 OR sp.output_tokens>0
			OR sp.cache_read_tokens>0 OR sp.cache_write_tokens>0)
			AND (sp.ended_at<>'' OR sp.status IN ('ok','error','cancelled'))`
	args := []any{}
	if !since.IsZero() {
		query += ` AND sp.started_at>=?`
		args = append(args, observationTime(since))
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type candidate struct {
		record core.UsageRecord
		rank   int
	}
	groups := map[string][]candidate{}
	bestRank := map[string]int{}
	for rows.Next() {
		var traceID, spanID, sessionID, conversationID, turnID, runtimeID, source, startedAt, modelJSON, agentID string
		var input, output, cacheRead, cacheWrite int64
		var cost float64
		if err := rows.Scan(&traceID, &spanID, &sessionID, &conversationID, &turnID, &runtimeID,
			&source, &startedAt, &modelJSON, &input, &output, &cacheRead, &cacheWrite, &cost, &agentID); err != nil {
			return nil, err
		}
		model := core.ObservationModel{}
		_ = json.Unmarshal([]byte(modelJSON), &model)
		logicalRequestID := strings.TrimSpace(model.RequestID)
		requestID := logicalRequestID
		if requestID != "" && model.Attempt > 0 {
			requestID += ":attempt:" + strconv.Itoa(model.Attempt)
		}
		rank := observationUsageSourceRank(source)
		runtimeSource := observationUsageRuntimeSource(runtimeID, source)
		record := core.UsageRecord{
			Source: runtimeSource, SessionID: sessionID,
			ConversationID: conversationID, TraceID: traceID, TurnID: turnID,
			RequestID: requestID, RuntimeID: runtimeID, Project: agentID,
			Model: firstObservationModel(model.Resolved, model.Requested), Timestamp: parseObservationTime(startedAt),
			InputTokens: input, OutputTokens: output, CacheReadTokens: cacheRead,
			CacheWriteTokens: cacheWrite, CostUSD: cost,
		}
		groupKey := observationUsageGroupKey(traceID, spanID, runtimeSource, logicalRequestID, model.Attempt)
		groups[groupKey] = append(groups[groupKey], candidate{record: record, rank: rank})
		if current, ok := bestRank[groupKey]; !ok || rank < current {
			bestRank[groupKey] = rank
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var records []core.UsageRecord
	for groupKey, candidates := range groups {
		best := bestRank[groupKey]
		var selected *core.UsageRecord
		for _, item := range candidates {
			if item.rank != best {
				continue
			}
			if selected == nil || observationUsageCompleteness(item.record) > observationUsageCompleteness(*selected) {
				candidateRecord := item.record
				selected = &candidateRecord
			}
		}
		if selected != nil {
			records = append(records, *selected)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Timestamp.Before(records[j].Timestamp) })
	return records, nil
}

// observationUsageGroupKey correlates the same native request across the
// in-process adapter, native OTel, Proxy and Transcript even when a backfill
// reconstructed a different trace ID. Request IDs are required to be stable
// adapter/native IDs; attempt keeps failover and retry costs distinct. When a
// source cannot provide one, preserving the span is safer than guessing and
// accidentally dropping a real second request in the same turn.
func observationUsageGroupKey(traceID, spanID, runtimeSource, requestID string, attempt int) string {
	if requestID == "" {
		return "span:" + traceID + ":" + spanID
	}
	if attempt <= 0 {
		attempt = 1
	}
	return "request:" + runtimeSource + ":" + requestID + ":" + strconv.Itoa(attempt)
}

func observationUsageCompleteness(record core.UsageRecord) int64 {
	return record.InputTokens + record.OutputTokens + record.CacheReadTokens + record.CacheWriteTokens
}

// MaterializeObservationDailyUsage refreshes every day still represented by
// detailed model spans. It is intended for explicit full rebuilds; startup and
// maintenance callers should use MaterializeObservationDailyUsageSince to
// avoid monopolizing the database while scanning a large observation store.
func (s *Store) MaterializeObservationDailyUsage(ctx context.Context) error {
	return s.MaterializeObservationDailyUsageSince(ctx, time.Time{})
}

// MaterializeObservationDailyUsageSince replaces daily rollups on or after
// since. Days before the refresh window remain untouched, providing compact
// long-term history after detailed traces expire.
func (s *Store) MaterializeObservationDailyUsageSince(ctx context.Context, since time.Time) error {
	records, err := s.QueryObservationUsage(ctx, since)
	if err != nil {
		return err
	}
	type dailyKey struct{ day, agent, runtime, model, source string }
	aggregates := map[dailyKey]core.UsageRecord{}
	for _, record := range records {
		key := dailyKey{
			day: record.Timestamp.UTC().Format("2006-01-02"), agent: record.Project,
			runtime: record.RuntimeID, model: record.Model, source: record.Source,
		}
		current := aggregates[key]
		current.InputTokens += record.InputTokens
		current.OutputTokens += record.OutputTokens
		current.CacheReadTokens += record.CacheReadTokens
		current.CacheWriteTokens += record.CacheWriteTokens
		current.CostUSD += record.CostUSD
		current.Requests++
		aggregates[key] = current
	}
	tx, err := s.observe.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if since.IsZero() {
		if _, err := tx.ExecContext(ctx, `DELETE FROM observation_daily_usage`); err != nil {
			return err
		}
	} else if _, err := tx.ExecContext(ctx, `DELETE FROM observation_daily_usage WHERE day>=?`, since.UTC().Format("2006-01-02")); err != nil {
		return err
	}
	now := observationTime(time.Now().UTC())
	for key, aggregate := range aggregates {
		if _, err := tx.ExecContext(ctx, `INSERT INTO observation_daily_usage
			(day,agent_id,runtime_id,model,source,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,cost_usd,requests,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(day,agent_id,runtime_id,model,source) DO UPDATE SET
			input_tokens=excluded.input_tokens,output_tokens=excluded.output_tokens,
			cache_read_tokens=excluded.cache_read_tokens,cache_write_tokens=excluded.cache_write_tokens,
			cost_usd=excluded.cost_usd,requests=excluded.requests,updated_at=excluded.updated_at`,
			key.day, key.agent, key.runtime, key.model, key.source, aggregate.InputTokens, aggregate.OutputTokens,
			aggregate.CacheReadTokens, aggregate.CacheWriteTokens, aggregate.CostUSD, aggregate.Requests, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) QueryObservationDailyUsage(ctx context.Context, since, before time.Time) ([]core.UsageRecord, error) {
	query := `SELECT day,agent_id,runtime_id,model,source,input_tokens,output_tokens,
		cache_read_tokens,cache_write_tokens,cost_usd,requests FROM observation_daily_usage WHERE 1=1`
	var args []any
	if !since.IsZero() {
		query += ` AND day>=?`
		args = append(args, since.UTC().Format("2006-01-02"))
	}
	if !before.IsZero() {
		query += ` AND day<?`
		args = append(args, before.UTC().Format("2006-01-02"))
	}
	query += ` ORDER BY day,agent_id,runtime_id,model,source`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []core.UsageRecord
	for rows.Next() {
		var day string
		var record core.UsageRecord
		if err := rows.Scan(&day, &record.Project, &record.RuntimeID, &record.Model, &record.Source,
			&record.InputTokens, &record.OutputTokens, &record.CacheReadTokens, &record.CacheWriteTokens,
			&record.CostUSD, &record.Requests); err != nil {
			return nil, err
		}
		record.Timestamp, _ = time.Parse("2006-01-02", day)
		records = append(records, record)
	}
	return records, rows.Err()
}

func observationUsageSourceRank(source string) int {
	source = strings.ToLower(source)
	switch {
	case source == "agentmux.internal":
		return 0
	case strings.Contains(source, "otel") || strings.Contains(source, "app-server"):
		return 1
	case strings.Contains(source, "hook"):
		return 2
	case strings.Contains(source, "proxy"):
		return 3
	case strings.Contains(source, "transcript"):
		return 4
	default:
		return 5
	}
}

func observationUsageRuntimeSource(runtimeID, source string) string {
	value := strings.ToLower(strings.TrimSpace(runtimeID))
	switch {
	case strings.Contains(value, "claude"):
		return "claude"
	case strings.Contains(value, "codex"):
		return "codex"
	case value != "":
		return value
	case strings.Contains(source, "claude"):
		return "claude"
	case strings.Contains(source, "codex"):
		return "codex"
	default:
		return source
	}
}

func firstObservationModel(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
