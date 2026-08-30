package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

const observationWriteBatchSize = 250

// recordObservationBatch performs one event insert and at most two summary
// upserts per affected trace/span. PostgreSQL resolves both idempotency keys in
// the INSERT itself, avoiding a read-before-write race and its WAL lookup cost.
func (s *Store) recordObservationBatch(ctx context.Context, envelopes []core.ObservationEnvelope) (map[string]bool, error) {
	inserted := make(map[string]bool, len(envelopes))
	if len(envelopes) == 0 {
		return inserted, nil
	}
	tx, err := s.observe.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var values []string
	args := make([]any, 0, len(envelopes)*16)
	normalized := make(map[string]core.ObservationEnvelope, len(envelopes))
	now := observationTime(time.Now().UTC())
	for _, envelope := range envelopes {
		envelope.Content = nil
		envelope.Normalize()
		if err := envelope.Validate(); err != nil {
			return nil, err
		}
		raw, err := json.Marshal(envelope)
		if err != nil {
			return nil, fmt.Errorf("marshal observation envelope: %w", err)
		}
		payloadID := ""
		if envelope.PayloadRef != nil {
			payloadID = envelope.PayloadRef.ID
		}
		values = append(values, "(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)")
		args = append(args,
			envelope.EventID, nullObservationString(envelope.DedupeKey), envelope.TraceID, envelope.SpanID,
			envelope.ParentSpanID, envelope.Sequence, observationTime(envelope.Time), envelope.Kind, envelope.Name,
			envelope.Lifecycle, envelope.Source, envelope.Quality, envelope.Status, payloadID, string(raw), now,
		)
		normalized[envelope.EventID] = envelope
	}
	rows, err := tx.QueryContext(ctx, `INSERT INTO observation_events
		(event_id,dedupe_key,trace_id,span_id,parent_span_id,sequence,timestamp,kind,name,lifecycle,source,quality,status,payload_id,envelope_json,created_at)
		VALUES `+strings.Join(values, ",")+`
		ON CONFLICT DO NOTHING RETURNING event_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("batch insert observation events: %w", err)
	}
	for rows.Next() {
		var eventID string
		if err := rows.Scan(&eventID); err != nil {
			rows.Close()
			return nil, err
		}
		inserted[eventID] = true
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	traceGroups := map[string][]core.ObservationEnvelope{}
	spanGroups := map[string][]core.ObservationEnvelope{}
	for eventID := range inserted {
		envelope := normalized[eventID]
		traceGroups[envelope.TraceID] = append(traceGroups[envelope.TraceID], envelope)
		spanGroups[envelope.SpanID] = append(spanGroups[envelope.SpanID], envelope)
	}
	for _, group := range traceGroups {
		sortObservationGroup(group)
		if err := upsertObservationTraceTx(ctx, tx, group[0]); err != nil {
			return nil, err
		}
		if len(group) > 1 {
			if err := upsertObservationTraceTx(ctx, tx, group[len(group)-1]); err != nil {
				return nil, err
			}
		}
	}
	for _, group := range spanGroups {
		sortObservationGroup(group)
		if err := upsertObservationSpanTx(ctx, tx, group[0]); err != nil {
			return nil, err
		}
		if len(group) > 1 {
			if err := upsertObservationSpanTx(ctx, tx, group[len(group)-1]); err != nil {
				return nil, err
			}
		}
	}
	for traceID := range traceGroups {
		if _, err := tx.ExecContext(ctx, `INSERT INTO observation_dirty_traces(trace_id,touched_at)
			VALUES(?,?) ON CONFLICT(trace_id) DO UPDATE SET touched_at=excluded.touched_at`, traceID, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return inserted, nil
}

func sortObservationGroup(group []core.ObservationEnvelope) {
	sort.SliceStable(group, func(i, j int) bool {
		if group[i].Time.Equal(group[j].Time) {
			return group[i].Sequence < group[j].Sequence
		}
		return group[i].Time.Before(group[j].Time)
	})
}

// attachObservationPayload runs only after the event INSERT returned its ID.
// This ordering prevents replayed or concurrently duplicated events from
// creating encrypted payload rows that immediately need to be deleted.
func (s *Store) attachObservationPayload(ctx context.Context, envelope core.ObservationEnvelope) error {
	envelope.Content = nil
	raw, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal secured observation envelope: %w", err)
	}
	payloadID := ""
	if envelope.PayloadRef != nil {
		payloadID = envelope.PayloadRef.ID
	}
	attributes := marshalObservationJSON(envelope.Attributes)
	tx, err := s.observe.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE observation_events
		SET payload_id=?,envelope_json=? WHERE event_id=?`, payloadID, string(raw), envelope.EventID)
	if err != nil {
		return fmt.Errorf("attach observation event payload: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return fmt.Errorf("attach observation event payload: event %s disappeared", envelope.EventID)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE observation_spans SET
		payload_id=CASE WHEN ?<>'' THEN ? ELSE payload_id END,
		attributes=? WHERE span_id=?`, payloadID, payloadID, attributes, envelope.SpanID); err != nil {
		return fmt.Errorf("attach observation span payload: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE observation_traces SET
		attributes=? WHERE trace_id=?`, attributes, envelope.TraceID); err != nil {
		return fmt.Errorf("attach observation trace payload metadata: %w", err)
	}
	return tx.Commit()
}

func upsertObservationTraceTx(ctx context.Context, tx *dbTx, envelope core.ObservationEnvelope) error {
	usage := observationUsage(envelope.Usage)
	timestamp := observationTime(envelope.Time)
	now := observationTime(time.Now().UTC())
	endedAt := ""
	if envelope.Lifecycle == core.ObservationLifecycleEnd || observationTerminalStatus(envelope.Status) {
		endedAt = timestamp
	}
	rootSpanID := ""
	if envelope.ParentSpanID == "" {
		rootSpanID = envelope.SpanID
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO observation_traces
		(trace_id,root_span_id,name,started_at,ended_at,agent_id,agent_name,runtime_id,conversation_id,session_id,turn_id,
		 source,provenance,quality,status,error_json,model_json,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,
		 reasoning_tokens,tool_tokens,total_tokens,cost_usd,attributes,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(trace_id) DO UPDATE SET
		 root_span_id=CASE WHEN observation_traces.root_span_id='' AND excluded.root_span_id<>'' THEN excluded.root_span_id ELSE observation_traces.root_span_id END,
		 name=CASE WHEN observation_traces.name='' THEN excluded.name ELSE observation_traces.name END,
		 started_at=CASE WHEN observation_traces.started_at<excluded.started_at THEN observation_traces.started_at ELSE excluded.started_at END,
		 ended_at=CASE WHEN excluded.ended_at<>'' THEN CASE WHEN observation_traces.ended_at>excluded.ended_at THEN observation_traces.ended_at ELSE excluded.ended_at END ELSE observation_traces.ended_at END,
		 agent_id=COALESCE(NULLIF(observation_traces.agent_id,''),excluded.agent_id),
		 agent_name=COALESCE(NULLIF(observation_traces.agent_name,''),excluded.agent_name),
		 runtime_id=COALESCE(NULLIF(observation_traces.runtime_id,''),excluded.runtime_id),
		 conversation_id=COALESCE(NULLIF(observation_traces.conversation_id,''),excluded.conversation_id),
		 session_id=COALESCE(NULLIF(observation_traces.session_id,''),excluded.session_id),
		 turn_id=COALESCE(NULLIF(observation_traces.turn_id,''),excluded.turn_id),
		 source=COALESCE(NULLIF(observation_traces.source,''),excluded.source),
		 provenance=CASE WHEN excluded.provenance<>'null' AND excluded.provenance<>'[]' THEN excluded.provenance ELSE observation_traces.provenance END,
		 quality=CASE WHEN observation_traces.quality IN ('','legacy','inferred','partial') THEN excluded.quality ELSE observation_traces.quality END,
		 status=CASE WHEN excluded.status IN ('ok','error','cancelled') THEN excluded.status WHEN observation_traces.status IN ('ok','error','cancelled') THEN observation_traces.status ELSE excluded.status END,
		 error_json=CASE WHEN excluded.error_json<>'null' THEN excluded.error_json ELSE observation_traces.error_json END,
		 model_json=CASE WHEN excluded.model_json<>'null' THEN excluded.model_json ELSE observation_traces.model_json END,
		 input_tokens=CASE WHEN observation_traces.input_tokens>excluded.input_tokens THEN observation_traces.input_tokens ELSE excluded.input_tokens END,
		 output_tokens=CASE WHEN observation_traces.output_tokens>excluded.output_tokens THEN observation_traces.output_tokens ELSE excluded.output_tokens END,
		 cache_read_tokens=CASE WHEN observation_traces.cache_read_tokens>excluded.cache_read_tokens THEN observation_traces.cache_read_tokens ELSE excluded.cache_read_tokens END,
		 cache_write_tokens=CASE WHEN observation_traces.cache_write_tokens>excluded.cache_write_tokens THEN observation_traces.cache_write_tokens ELSE excluded.cache_write_tokens END,
		 reasoning_tokens=CASE WHEN observation_traces.reasoning_tokens>excluded.reasoning_tokens THEN observation_traces.reasoning_tokens ELSE excluded.reasoning_tokens END,
		 tool_tokens=CASE WHEN observation_traces.tool_tokens>excluded.tool_tokens THEN observation_traces.tool_tokens ELSE excluded.tool_tokens END,
		 total_tokens=CASE WHEN observation_traces.total_tokens>excluded.total_tokens THEN observation_traces.total_tokens ELSE excluded.total_tokens END,
		 cost_usd=CASE WHEN observation_traces.cost_usd>excluded.cost_usd THEN observation_traces.cost_usd ELSE excluded.cost_usd END,
		 attributes=CASE WHEN excluded.attributes<>'null' THEN excluded.attributes ELSE observation_traces.attributes END,
		 updated_at=excluded.updated_at`,
		envelope.TraceID, rootSpanID, observationName(envelope), timestamp, endedAt,
		envelope.AgentID, envelope.AgentName, envelope.RuntimeID, envelope.ConversationID, envelope.SessionID, envelope.TurnID,
		envelope.Source, marshalObservationJSON(envelope.Provenance), envelope.Quality, envelope.Status,
		marshalObservationJSON(envelope.Error), marshalObservationJSON(envelope.Model),
		usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens, usage.ReasoningTokens,
		usage.ToolTokens, usage.TotalTokens, usage.CostUSD, marshalObservationJSON(envelope.Attributes), now, now)
	if err != nil {
		return fmt.Errorf("upsert observation trace: %w", err)
	}
	return nil
}

func upsertObservationSpanTx(ctx context.Context, tx *dbTx, envelope core.ObservationEnvelope) error {
	usage := observationUsage(envelope.Usage)
	timestamp := observationTime(envelope.Time)
	now := observationTime(time.Now().UTC())
	endedAt := ""
	if envelope.Lifecycle == core.ObservationLifecycleEnd || observationTerminalStatus(envelope.Status) {
		endedAt = timestamp
	}
	duration := int64(0)
	if envelope.Model != nil {
		duration = envelope.Model.DurationMillis
	}
	if envelope.Tool != nil && envelope.Tool.DurationMillis > duration {
		duration = envelope.Tool.DurationMillis
	}
	payloadID := ""
	if envelope.PayloadRef != nil {
		payloadID = envelope.PayloadRef.ID
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO observation_spans
		(span_id,trace_id,parent_span_id,kind,name,sequence,started_at,ended_at,duration_ms,agent_id,runtime_id,conversation_id,
		 session_id,turn_id,source,provenance,quality,status,error_json,model_json,tool_json,payload_id,input_tokens,output_tokens,
		 cache_read_tokens,cache_write_tokens,reasoning_tokens,tool_tokens,total_tokens,cost_usd,attributes,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(span_id) DO UPDATE SET
		 trace_id=excluded.trace_id,
		 parent_span_id=COALESCE(NULLIF(observation_spans.parent_span_id,''),excluded.parent_span_id),
		 kind=excluded.kind,
		 name=COALESCE(NULLIF(observation_spans.name,''),excluded.name),
		 sequence=CASE WHEN observation_spans.sequence=0 THEN excluded.sequence ELSE CASE WHEN observation_spans.sequence<excluded.sequence THEN observation_spans.sequence ELSE excluded.sequence END END,
		 started_at=CASE WHEN observation_spans.started_at<excluded.started_at THEN observation_spans.started_at ELSE excluded.started_at END,
		 ended_at=CASE WHEN excluded.ended_at<>'' THEN CASE WHEN observation_spans.ended_at>excluded.ended_at THEN observation_spans.ended_at ELSE excluded.ended_at END ELSE observation_spans.ended_at END,
		 duration_ms=CASE WHEN observation_spans.duration_ms>excluded.duration_ms THEN observation_spans.duration_ms ELSE excluded.duration_ms END,
		 agent_id=COALESCE(NULLIF(observation_spans.agent_id,''),excluded.agent_id),
		 runtime_id=COALESCE(NULLIF(observation_spans.runtime_id,''),excluded.runtime_id),
		 conversation_id=COALESCE(NULLIF(observation_spans.conversation_id,''),excluded.conversation_id),
		 session_id=COALESCE(NULLIF(observation_spans.session_id,''),excluded.session_id),
		 turn_id=COALESCE(NULLIF(observation_spans.turn_id,''),excluded.turn_id),
		 source=COALESCE(NULLIF(observation_spans.source,''),excluded.source),
		 provenance=CASE WHEN excluded.provenance<>'null' AND excluded.provenance<>'[]' THEN excluded.provenance ELSE observation_spans.provenance END,
		 quality=CASE WHEN observation_spans.quality IN ('','legacy','inferred','partial') THEN excluded.quality ELSE observation_spans.quality END,
		 status=CASE WHEN excluded.status IN ('ok','error','cancelled') THEN excluded.status WHEN observation_spans.status IN ('ok','error','cancelled') THEN observation_spans.status ELSE excluded.status END,
		 error_json=CASE WHEN excluded.error_json<>'null' THEN excluded.error_json ELSE observation_spans.error_json END,
		 model_json=CASE WHEN excluded.model_json<>'null' THEN excluded.model_json ELSE observation_spans.model_json END,
		 tool_json=CASE WHEN excluded.tool_json<>'null' THEN excluded.tool_json ELSE observation_spans.tool_json END,
		 payload_id=COALESCE(NULLIF(excluded.payload_id,''),observation_spans.payload_id),
		 input_tokens=CASE WHEN observation_spans.input_tokens>excluded.input_tokens THEN observation_spans.input_tokens ELSE excluded.input_tokens END,
		 output_tokens=CASE WHEN observation_spans.output_tokens>excluded.output_tokens THEN observation_spans.output_tokens ELSE excluded.output_tokens END,
		 cache_read_tokens=CASE WHEN observation_spans.cache_read_tokens>excluded.cache_read_tokens THEN observation_spans.cache_read_tokens ELSE excluded.cache_read_tokens END,
		 cache_write_tokens=CASE WHEN observation_spans.cache_write_tokens>excluded.cache_write_tokens THEN observation_spans.cache_write_tokens ELSE excluded.cache_write_tokens END,
		 reasoning_tokens=CASE WHEN observation_spans.reasoning_tokens>excluded.reasoning_tokens THEN observation_spans.reasoning_tokens ELSE excluded.reasoning_tokens END,
		 tool_tokens=CASE WHEN observation_spans.tool_tokens>excluded.tool_tokens THEN observation_spans.tool_tokens ELSE excluded.tool_tokens END,
		 total_tokens=CASE WHEN observation_spans.total_tokens>excluded.total_tokens THEN observation_spans.total_tokens ELSE excluded.total_tokens END,
		 cost_usd=CASE WHEN observation_spans.cost_usd>excluded.cost_usd THEN observation_spans.cost_usd ELSE excluded.cost_usd END,
		 attributes=CASE WHEN excluded.attributes<>'null' THEN excluded.attributes ELSE observation_spans.attributes END,
		 updated_at=excluded.updated_at`,
		envelope.SpanID, envelope.TraceID, envelope.ParentSpanID, envelope.Kind, observationName(envelope), envelope.Sequence,
		timestamp, endedAt, duration, envelope.AgentID, envelope.RuntimeID, envelope.ConversationID, envelope.SessionID, envelope.TurnID,
		envelope.Source, marshalObservationJSON(envelope.Provenance), envelope.Quality, envelope.Status,
		marshalObservationJSON(envelope.Error), marshalObservationJSON(envelope.Model), marshalObservationJSON(envelope.Tool), payloadID,
		usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens, usage.ReasoningTokens,
		usage.ToolTokens, usage.TotalTokens, usage.CostUSD, marshalObservationJSON(envelope.Attributes), now, now)
	if err != nil {
		return fmt.Errorf("upsert observation span: %w", err)
	}
	return nil
}

// MaterializeDirtyObservationTraces refreshes counts and deduplicated usage
// outside the event transaction.
func (s *Store) MaterializeDirtyObservationTraces(ctx context.Context, limit int) (int, error) {
	if !s.IsPostgres() {
		return 0, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	tx, err := s.observe.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT trace_id FROM observation_dirty_traces
		ORDER BY touched_at LIMIT ? FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return 0, err
	}
	var traceIDs []string
	for rows.Next() {
		var traceID string
		if err := rows.Scan(&traceID); err != nil {
			rows.Close()
			return 0, err
		}
		traceIDs = append(traceIDs, traceID)
	}
	rows.Close()
	if len(traceIDs) == 0 {
		return 0, tx.Commit()
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(traceIDs)), ",")
	args := make([]any, len(traceIDs))
	for index := range traceIDs {
		args[index] = traceIDs[index]
	}
	if _, err := tx.ExecContext(ctx, `UPDATE observation_traces t SET
		span_count=c.span_count,event_count=c.event_count,updated_at=?
		FROM (
			SELECT ids.trace_id,
				(SELECT COUNT(*) FROM observation_spans s WHERE s.trace_id=ids.trace_id) AS span_count,
				(SELECT COUNT(*) FROM observation_events e WHERE e.trace_id=ids.trace_id) AS event_count
			FROM (SELECT trace_id FROM observation_dirty_traces WHERE trace_id IN (`+placeholders+`)) ids
		) c WHERE t.trace_id=c.trace_id`, append([]any{observationTime(time.Now().UTC())}, args...)...); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `WITH candidates AS (
			SELECT s.trace_id,
				CASE
					WHEN COALESCE(s.model_json->>'request_id','')=''
						THEN 'span:' || s.trace_id || ':' || s.span_id
					ELSE 'request:' ||
						CASE
							WHEN lower(trim(s.runtime_id)) LIKE '%claude%' THEN 'claude'
							WHEN lower(trim(s.runtime_id)) LIKE '%codex%' THEN 'codex'
							WHEN trim(s.runtime_id)<>'' THEN lower(trim(s.runtime_id))
							WHEN lower(s.source) LIKE '%claude%' THEN 'claude'
							WHEN lower(s.source) LIKE '%codex%' THEN 'codex'
							ELSE lower(s.source)
						END || ':' || (s.model_json->>'request_id') || ':' ||
						CASE
							WHEN COALESCE(s.model_json->>'attempt','') ~ '^[1-9][0-9]*$'
								THEN s.model_json->>'attempt'
							ELSE '1'
						END
				END AS usage_key,
				CASE
					WHEN lower(s.source)='agentmux.internal' THEN 0
					WHEN lower(s.source) LIKE '%otel%' OR lower(s.source) LIKE '%app-server%' THEN 1
					WHEN lower(s.source) LIKE '%hook%' THEN 2
					WHEN lower(s.source) LIKE '%proxy%' THEN 3
					WHEN lower(s.source) LIKE '%transcript%' THEN 4
					ELSE 5
				END AS source_rank,
				CASE WHEN s.total_tokens>0 THEN s.total_tokens ELSE
					s.input_tokens+s.output_tokens+s.cache_read_tokens+s.cache_write_tokens+
					s.reasoning_tokens+s.tool_tokens END AS completeness,
				s.input_tokens,s.output_tokens,s.cache_read_tokens,s.cache_write_tokens,
				s.reasoning_tokens,s.tool_tokens,s.total_tokens,s.cost_usd,s.span_id
			FROM observation_spans s
			WHERE s.trace_id IN (`+placeholders+`) AND s.kind='model.request'
		), selected AS (
			SELECT *, row_number() OVER (
				PARTITION BY trace_id,usage_key
				ORDER BY source_rank,completeness DESC,span_id
			) AS choice
			FROM candidates
		), usage AS (
			SELECT trace_id,
				SUM(input_tokens) AS input_tokens,
				SUM(output_tokens) AS output_tokens,
				SUM(cache_read_tokens) AS cache_read_tokens,
				SUM(cache_write_tokens) AS cache_write_tokens,
				SUM(reasoning_tokens) AS reasoning_tokens,
				SUM(tool_tokens) AS tool_tokens,
				SUM(total_tokens) AS total_tokens,
				SUM(cost_usd) AS cost_usd
			FROM selected WHERE choice=1 GROUP BY trace_id
		)
		UPDATE observation_traces t SET
			input_tokens=u.input_tokens,output_tokens=u.output_tokens,
			cache_read_tokens=u.cache_read_tokens,cache_write_tokens=u.cache_write_tokens,
			reasoning_tokens=u.reasoning_tokens,tool_tokens=u.tool_tokens,
			total_tokens=u.total_tokens,cost_usd=u.cost_usd
		FROM usage u WHERE t.trace_id=u.trace_id`, args...); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM observation_dirty_traces WHERE trace_id IN (`+placeholders+`)`, args...); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(traceIDs), nil
}
