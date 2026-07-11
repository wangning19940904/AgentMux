package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/agentnexus/agentnexus/core"
)

const observationSchema = `
CREATE TABLE IF NOT EXISTS observation_traces (
	trace_id TEXT PRIMARY KEY,
	root_span_id TEXT,
	name TEXT,
	started_at TEXT NOT NULL,
	ended_at TEXT,
	agent_id TEXT,
	agent_name TEXT,
	runtime_id TEXT,
	conversation_id TEXT,
	session_id TEXT,
	turn_id TEXT,
	source TEXT,
	provenance TEXT,
	quality TEXT,
	status TEXT,
	error_json TEXT,
	model_json TEXT,
	input_tokens INTEGER DEFAULT 0,
	output_tokens INTEGER DEFAULT 0,
	cache_read_tokens INTEGER DEFAULT 0,
	cache_write_tokens INTEGER DEFAULT 0,
	reasoning_tokens INTEGER DEFAULT 0,
	tool_tokens INTEGER DEFAULT 0,
	total_tokens INTEGER DEFAULT 0,
	cost_usd REAL DEFAULT 0,
	span_count INTEGER DEFAULT 0,
	event_count INTEGER DEFAULT 0,
	attributes TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_observation_traces_started ON observation_traces(started_at DESC);
CREATE INDEX IF NOT EXISTS idx_observation_traces_agent_started ON observation_traces(agent_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_observation_traces_session_started ON observation_traces(session_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_observation_traces_conversation_started ON observation_traces(conversation_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_observation_traces_status_started ON observation_traces(status, started_at DESC);

CREATE TABLE IF NOT EXISTS observation_spans (
	span_id TEXT PRIMARY KEY,
	trace_id TEXT NOT NULL,
	parent_span_id TEXT,
	kind TEXT NOT NULL,
	name TEXT,
	sequence INTEGER DEFAULT 0,
	started_at TEXT NOT NULL,
	ended_at TEXT,
	duration_ms INTEGER DEFAULT 0,
	agent_id TEXT,
	runtime_id TEXT,
	conversation_id TEXT,
	session_id TEXT,
	turn_id TEXT,
	source TEXT,
	provenance TEXT,
	quality TEXT,
	status TEXT,
	error_json TEXT,
	model_json TEXT,
	tool_json TEXT,
	payload_id TEXT,
	input_tokens INTEGER DEFAULT 0,
	output_tokens INTEGER DEFAULT 0,
	cache_read_tokens INTEGER DEFAULT 0,
	cache_write_tokens INTEGER DEFAULT 0,
	reasoning_tokens INTEGER DEFAULT 0,
	tool_tokens INTEGER DEFAULT 0,
	total_tokens INTEGER DEFAULT 0,
	cost_usd REAL DEFAULT 0,
	attributes TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_observation_spans_trace_sequence ON observation_spans(trace_id, sequence, started_at);
CREATE INDEX IF NOT EXISTS idx_observation_spans_trace_parent ON observation_spans(trace_id, parent_span_id);
CREATE INDEX IF NOT EXISTS idx_observation_spans_kind_started ON observation_spans(kind, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_observation_spans_tool_call ON observation_spans(json_extract(tool_json, '$.call_id')) WHERE tool_json IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_observation_spans_model_request ON observation_spans(json_extract(model_json, '$.request_id')) WHERE model_json IS NOT NULL;

CREATE TABLE IF NOT EXISTS observation_events (
	event_id TEXT PRIMARY KEY,
	dedupe_key TEXT,
	trace_id TEXT NOT NULL,
	span_id TEXT NOT NULL,
	parent_span_id TEXT,
	sequence INTEGER DEFAULT 0,
	timestamp TEXT NOT NULL,
	kind TEXT NOT NULL,
	name TEXT,
	lifecycle TEXT,
	source TEXT,
	quality TEXT,
	status TEXT,
	payload_id TEXT,
	envelope_json TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_observation_events_dedupe ON observation_events(dedupe_key) WHERE dedupe_key IS NOT NULL AND dedupe_key <> '';
CREATE INDEX IF NOT EXISTS idx_observation_events_trace_sequence ON observation_events(trace_id, sequence, timestamp);
CREATE INDEX IF NOT EXISTS idx_observation_events_span_time ON observation_events(span_id, timestamp);
CREATE INDEX IF NOT EXISTS idx_observation_events_source_time ON observation_events(source, timestamp DESC);

CREATE TABLE IF NOT EXISTS observation_data_keys (
	key_id TEXT PRIMARY KEY,
	wrap_nonce BLOB NOT NULL,
	wrapped_key BLOB NOT NULL,
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS observation_payloads (
	payload_id TEXT PRIMARY KEY,
	key_id TEXT NOT NULL,
	content_type TEXT,
	compression TEXT NOT NULL,
	encryption TEXT NOT NULL,
	nonce BLOB NOT NULL,
	ciphertext BLOB NOT NULL,
	sha256 TEXT NOT NULL,
	original_bytes INTEGER DEFAULT 0,
	stored_bytes INTEGER DEFAULT 0,
	redacted INTEGER DEFAULT 0,
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_observation_payloads_expiry ON observation_payloads(expires_at);
CREATE INDEX IF NOT EXISTS idx_observation_payloads_key ON observation_payloads(key_id);

CREATE TABLE IF NOT EXISTS observation_payload_chunks (
	payload_id TEXT NOT NULL,
	chunk_index INTEGER NOT NULL,
	nonce BLOB NOT NULL,
	ciphertext BLOB NOT NULL,
	original_bytes INTEGER DEFAULT 0,
	stored_bytes INTEGER DEFAULT 0,
	PRIMARY KEY(payload_id, chunk_index)
);
CREATE INDEX IF NOT EXISTS idx_observation_payload_chunks_payload ON observation_payload_chunks(payload_id, chunk_index);

CREATE TABLE IF NOT EXISTS observation_daily_usage (
	day TEXT NOT NULL,
	agent_id TEXT NOT NULL DEFAULT '',
	runtime_id TEXT NOT NULL DEFAULT '',
	model TEXT NOT NULL DEFAULT '',
	source TEXT NOT NULL DEFAULT '',
	input_tokens INTEGER DEFAULT 0,
	output_tokens INTEGER DEFAULT 0,
	cache_read_tokens INTEGER DEFAULT 0,
	cache_write_tokens INTEGER DEFAULT 0,
	cost_usd REAL DEFAULT 0,
	requests INTEGER DEFAULT 0,
	updated_at TEXT NOT NULL,
	PRIMARY KEY(day,agent_id,runtime_id,model,source)
);
CREATE INDEX IF NOT EXISTS idx_observation_daily_usage_day ON observation_daily_usage(day DESC);

CREATE TABLE IF NOT EXISTS observation_ingest_cursors (
	source TEXT NOT NULL,
	resource TEXT NOT NULL,
	cursor TEXT,
	message_id TEXT,
	file_identity TEXT,
	byte_offset INTEGER DEFAULT 0,
	observed_at TEXT,
	updated_at TEXT NOT NULL,
	PRIMARY KEY(source, resource)
);
CREATE INDEX IF NOT EXISTS idx_observation_ingest_cursors_updated ON observation_ingest_cursors(updated_at);

CREATE TABLE IF NOT EXISTS observation_export_outbox (
	id TEXT PRIMARY KEY,
	exporter TEXT NOT NULL,
	event_id TEXT NOT NULL,
	trace_id TEXT NOT NULL,
	envelope_json TEXT NOT NULL,
	include_content INTEGER DEFAULT 0,
	status TEXT NOT NULL DEFAULT 'pending',
	attempts INTEGER DEFAULT 0,
	next_attempt_at TEXT,
	last_error TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE(exporter, event_id)
);
CREATE INDEX IF NOT EXISTS idx_observation_outbox_ready ON observation_export_outbox(exporter, status, next_attempt_at, created_at);

CREATE TABLE IF NOT EXISTS observation_insights (
	id TEXT PRIMARY KEY,
	rule_id TEXT NOT NULL,
	agent_id TEXT,
	trace_id TEXT,
	severity TEXT,
	status TEXT NOT NULL DEFAULT 'open',
	title TEXT NOT NULL,
	summary TEXT,
	suggestion TEXT,
	sample_size INTEGER DEFAULT 0,
	confidence REAL DEFAULT 0,
	estimated_token_savings INTEGER DEFAULT 0,
	estimated_cost_savings_usd REAL DEFAULT 0,
	related_trace_ids TEXT,
	only_suggestion INTEGER DEFAULT 1,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_observation_insights_status_created ON observation_insights(status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_observation_insights_agent_created ON observation_insights(agent_id, created_at DESC);

CREATE TABLE IF NOT EXISTS observation_integration_ownership (
	install_id TEXT NOT NULL,
	host TEXT NOT NULL,
	scope TEXT NOT NULL,
	resource_key TEXT NOT NULL,
	version TEXT,
	sha256 TEXT,
	handler_fingerprint TEXT,
	target_path TEXT,
	before_hash TEXT,
	after_hash TEXT,
	metadata TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY(install_id, resource_key)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_observation_ownership_resource ON observation_integration_ownership(host, scope, resource_key);

CREATE TABLE IF NOT EXISTS observation_resource_leases (
	resource_key TEXT PRIMARY KEY,
	owner_id TEXT NOT NULL,
	install_id TEXT,
	lease_token TEXT NOT NULL,
	acquired_at TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	metadata TEXT
);
CREATE INDEX IF NOT EXISTS idx_observation_resource_leases_expiry ON observation_resource_leases(expires_at);
`

func (s *Store) migrateObservations() error {
	_, err := s.db.Exec(observationSchema)
	return err
}

// ObservationTrace is the durable summary returned by trace list/get APIs.
type ObservationTrace struct {
	TraceID        string                 `json:"trace_id"`
	RootSpanID     string                 `json:"root_span_id,omitempty"`
	Name           string                 `json:"name,omitempty"`
	StartedAt      time.Time              `json:"started_at"`
	EndedAt        *time.Time             `json:"ended_at,omitempty"`
	AgentID        string                 `json:"agent_id,omitempty"`
	AgentName      string                 `json:"agent_name,omitempty"`
	RuntimeID      string                 `json:"runtime_id,omitempty"`
	ConversationID string                 `json:"conversation_id,omitempty"`
	SessionID      string                 `json:"session_id,omitempty"`
	TurnID         string                 `json:"turn_id,omitempty"`
	Source         string                 `json:"source,omitempty"`
	Provenance     []string               `json:"provenance,omitempty"`
	Quality        string                 `json:"quality,omitempty"`
	Status         string                 `json:"status,omitempty"`
	Error          *core.ObservationError `json:"error,omitempty"`
	Model          *core.ObservationModel `json:"model,omitempty"`
	Usage          core.ObservationUsage  `json:"usage"`
	SpanCount      int64                  `json:"span_count"`
	EventCount     int64                  `json:"event_count"`
	Attributes     map[string]any         `json:"attributes,omitempty"`
	CreatedAt      time.Time              `json:"created_at"`
	UpdatedAt      time.Time              `json:"updated_at"`
}

type ObservationSpan struct {
	SpanID         string                 `json:"span_id"`
	TraceID        string                 `json:"trace_id"`
	ParentSpanID   string                 `json:"parent_span_id,omitempty"`
	Kind           string                 `json:"kind"`
	Name           string                 `json:"name,omitempty"`
	Sequence       int64                  `json:"sequence,omitempty"`
	StartedAt      time.Time              `json:"started_at"`
	EndedAt        *time.Time             `json:"ended_at,omitempty"`
	DurationMillis int64                  `json:"duration_ms,omitempty"`
	AgentID        string                 `json:"agent_id,omitempty"`
	RuntimeID      string                 `json:"runtime_id,omitempty"`
	ConversationID string                 `json:"conversation_id,omitempty"`
	SessionID      string                 `json:"session_id,omitempty"`
	TurnID         string                 `json:"turn_id,omitempty"`
	Source         string                 `json:"source,omitempty"`
	Provenance     []string               `json:"provenance,omitempty"`
	Quality        string                 `json:"quality,omitempty"`
	Status         string                 `json:"status,omitempty"`
	Error          *core.ObservationError `json:"error,omitempty"`
	Model          *core.ObservationModel `json:"model,omitempty"`
	Tool           *core.ObservationTool  `json:"tool,omitempty"`
	PayloadID      string                 `json:"payload_id,omitempty"`
	Usage          core.ObservationUsage  `json:"usage"`
	Attributes     map[string]any         `json:"attributes,omitempty"`
	CreatedAt      time.Time              `json:"created_at"`
	UpdatedAt      time.Time              `json:"updated_at"`
}

type ObservationTraceFilter struct {
	AgentID        string
	RuntimeID      string
	ConversationID string
	SessionID      string
	Status         string
	Source         string
	Since          time.Time
	Until          time.Time
	Limit          int
	Offset         int
}

// RecordObservation idempotently records one envelope and refreshes its span
// and trace summaries. EventID and DedupeKey are both honored for replay.
func (s *Store) RecordObservation(ctx context.Context, envelope core.ObservationEnvelope) error {
	envelope.Content = nil
	envelope.Normalize()
	if err := envelope.Validate(); err != nil {
		return err
	}
	envelopeJSON, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal observation envelope: %w", err)
	}
	provenanceJSON := marshalObservationJSON(envelope.Provenance)
	errorJSON := marshalObservationJSON(envelope.Error)
	modelJSON := marshalObservationJSON(envelope.Model)
	toolJSON := marshalObservationJSON(envelope.Tool)
	attributesJSON := marshalObservationJSON(envelope.Attributes)
	payloadID := ""
	if envelope.PayloadRef != nil {
		payloadID = envelope.PayloadRef.ID
	}
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
	duration := int64(0)
	if envelope.Model != nil {
		duration = envelope.Model.DurationMillis
	}
	if envelope.Tool != nil && envelope.Tool.DurationMillis > duration {
		duration = envelope.Tool.DurationMillis
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO observation_events
		(event_id,dedupe_key,trace_id,span_id,parent_span_id,sequence,timestamp,kind,name,lifecycle,source,quality,status,payload_id,envelope_json,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		envelope.EventID, nullObservationString(envelope.DedupeKey), envelope.TraceID, envelope.SpanID,
		envelope.ParentSpanID, envelope.Sequence, timestamp, envelope.Kind, envelope.Name, envelope.Lifecycle,
		envelope.Source, envelope.Quality, envelope.Status, payloadID, string(envelopeJSON), now)
	if err != nil {
		return fmt.Errorf("insert observation event: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted == 0 {
		return nil
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO observation_traces
		(trace_id,root_span_id,name,started_at,ended_at,agent_id,agent_name,runtime_id,conversation_id,session_id,turn_id,
		 source,provenance,quality,status,error_json,model_json,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,
		 reasoning_tokens,tool_tokens,total_tokens,cost_usd,attributes,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(trace_id) DO UPDATE SET
		 root_span_id=CASE WHEN observation_traces.root_span_id='' AND excluded.root_span_id<>'' THEN excluded.root_span_id ELSE observation_traces.root_span_id END,
		 name=CASE WHEN observation_traces.name='' THEN excluded.name ELSE observation_traces.name END,
		 started_at=MIN(observation_traces.started_at,excluded.started_at),
		 ended_at=CASE WHEN excluded.ended_at<>'' THEN MAX(observation_traces.ended_at,excluded.ended_at) ELSE observation_traces.ended_at END,
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
		 input_tokens=MAX(observation_traces.input_tokens,excluded.input_tokens),
		 output_tokens=MAX(observation_traces.output_tokens,excluded.output_tokens),
		 cache_read_tokens=MAX(observation_traces.cache_read_tokens,excluded.cache_read_tokens),
		 cache_write_tokens=MAX(observation_traces.cache_write_tokens,excluded.cache_write_tokens),
		 reasoning_tokens=MAX(observation_traces.reasoning_tokens,excluded.reasoning_tokens),
		 tool_tokens=MAX(observation_traces.tool_tokens,excluded.tool_tokens),
		 total_tokens=MAX(observation_traces.total_tokens,excluded.total_tokens),
		 cost_usd=MAX(observation_traces.cost_usd,excluded.cost_usd),
		 attributes=CASE WHEN excluded.attributes<>'null' THEN excluded.attributes ELSE observation_traces.attributes END,
		 updated_at=excluded.updated_at`,
		envelope.TraceID, rootSpanID, observationName(envelope), timestamp, endedAt,
		envelope.AgentID, envelope.AgentName, envelope.RuntimeID, envelope.ConversationID, envelope.SessionID, envelope.TurnID,
		envelope.Source, provenanceJSON, envelope.Quality, envelope.Status, errorJSON, modelJSON,
		usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens, usage.ReasoningTokens,
		usage.ToolTokens, usage.TotalTokens, usage.CostUSD, attributesJSON, now, now)
	if err != nil {
		return fmt.Errorf("upsert observation trace: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
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
		 sequence=CASE WHEN observation_spans.sequence=0 THEN excluded.sequence ELSE MIN(observation_spans.sequence,excluded.sequence) END,
		 started_at=MIN(observation_spans.started_at,excluded.started_at),
		 ended_at=CASE WHEN excluded.ended_at<>'' THEN MAX(observation_spans.ended_at,excluded.ended_at) ELSE observation_spans.ended_at END,
		 duration_ms=MAX(observation_spans.duration_ms,excluded.duration_ms),
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
		 input_tokens=MAX(observation_spans.input_tokens,excluded.input_tokens),
		 output_tokens=MAX(observation_spans.output_tokens,excluded.output_tokens),
		 cache_read_tokens=MAX(observation_spans.cache_read_tokens,excluded.cache_read_tokens),
		 cache_write_tokens=MAX(observation_spans.cache_write_tokens,excluded.cache_write_tokens),
		 reasoning_tokens=MAX(observation_spans.reasoning_tokens,excluded.reasoning_tokens),
		 tool_tokens=MAX(observation_spans.tool_tokens,excluded.tool_tokens),
		 total_tokens=MAX(observation_spans.total_tokens,excluded.total_tokens),
		 cost_usd=MAX(observation_spans.cost_usd,excluded.cost_usd),
		 attributes=CASE WHEN excluded.attributes<>'null' THEN excluded.attributes ELSE observation_spans.attributes END,
		 updated_at=excluded.updated_at`,
		envelope.SpanID, envelope.TraceID, envelope.ParentSpanID, envelope.Kind, observationName(envelope), envelope.Sequence,
		timestamp, endedAt, duration, envelope.AgentID, envelope.RuntimeID, envelope.ConversationID, envelope.SessionID, envelope.TurnID,
		envelope.Source, provenanceJSON, envelope.Quality, envelope.Status, errorJSON, modelJSON, toolJSON, payloadID,
		usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens, usage.ReasoningTokens,
		usage.ToolTokens, usage.TotalTokens, usage.CostUSD, attributesJSON, now, now)
	if err != nil {
		return fmt.Errorf("upsert observation span: %w", err)
	}

	traceUsage, hasModelUsage, err := observationTraceUsageTx(ctx, tx, envelope.TraceID)
	if err != nil {
		return fmt.Errorf("materialize observation trace usage: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE observation_traces SET
		 span_count=(SELECT COUNT(*) FROM observation_spans WHERE trace_id=?),
		 event_count=(SELECT COUNT(*) FROM observation_events WHERE trace_id=?),
		 input_tokens=CASE WHEN ? THEN ? ELSE input_tokens END,
		 output_tokens=CASE WHEN ? THEN ? ELSE output_tokens END,
		 cache_read_tokens=CASE WHEN ? THEN ? ELSE cache_read_tokens END,
		 cache_write_tokens=CASE WHEN ? THEN ? ELSE cache_write_tokens END,
		 reasoning_tokens=CASE WHEN ? THEN ? ELSE reasoning_tokens END,
		 tool_tokens=CASE WHEN ? THEN ? ELSE tool_tokens END,
		 total_tokens=CASE WHEN ? THEN ? ELSE total_tokens END,
		 cost_usd=CASE WHEN ? THEN ? ELSE cost_usd END,
		 updated_at=?
		WHERE trace_id=?`,
		envelope.TraceID, envelope.TraceID,
		hasModelUsage, traceUsage.InputTokens, hasModelUsage, traceUsage.OutputTokens,
		hasModelUsage, traceUsage.CacheReadTokens, hasModelUsage, traceUsage.CacheWriteTokens,
		hasModelUsage, traceUsage.ReasoningTokens, hasModelUsage, traceUsage.ToolTokens,
		hasModelUsage, traceUsage.TotalTokens, hasModelUsage, traceUsage.CostUSD,
		now, envelope.TraceID)
	if err != nil {
		return fmt.Errorf("refresh observation trace: %w", err)
	}
	return tx.Commit()
}

// observationTraceUsageTx applies the same request/attempt source selection as
// the long-term Usage materializer while the event transaction is still open.
// This keeps Trace cards and details from summing internal, OTel and Proxy
// copies of the same model request.
func observationTraceUsageTx(ctx context.Context, tx *sql.Tx, traceID string) (core.ObservationUsage, bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT span_id,runtime_id,source,model_json,input_tokens,output_tokens,
		cache_read_tokens,cache_write_tokens,reasoning_tokens,tool_tokens,total_tokens,cost_usd
		FROM observation_spans WHERE trace_id=? AND kind='model.request'`, traceID)
	if err != nil {
		return core.ObservationUsage{}, false, err
	}
	defer rows.Close()
	type candidate struct {
		rank  int
		usage core.ObservationUsage
	}
	groups := map[string][]candidate{}
	for rows.Next() {
		var spanID, runtimeID, source, modelJSON string
		var usage core.ObservationUsage
		if err := rows.Scan(&spanID, &runtimeID, &source, &modelJSON,
			&usage.InputTokens, &usage.OutputTokens, &usage.CacheReadTokens, &usage.CacheWriteTokens,
			&usage.ReasoningTokens, &usage.ToolTokens, &usage.TotalTokens, &usage.CostUSD); err != nil {
			return core.ObservationUsage{}, false, err
		}
		model := core.ObservationModel{}
		_ = json.Unmarshal([]byte(modelJSON), &model)
		key := observationUsageGroupKey(traceID, spanID, observationUsageRuntimeSource(runtimeID, source), strings.TrimSpace(model.RequestID), model.Attempt)
		groups[key] = append(groups[key], candidate{rank: observationUsageSourceRank(source), usage: usage})
	}
	if err := rows.Err(); err != nil {
		return core.ObservationUsage{}, false, err
	}
	if len(groups) == 0 {
		return core.ObservationUsage{}, false, nil
	}
	var total core.ObservationUsage
	for _, candidates := range groups {
		selected := candidates[0]
		for _, item := range candidates[1:] {
			if item.rank < selected.rank || (item.rank == selected.rank && observationEnvelopeUsageCompleteness(item.usage) > observationEnvelopeUsageCompleteness(selected.usage)) {
				selected = item
			}
		}
		total.InputTokens += selected.usage.InputTokens
		total.OutputTokens += selected.usage.OutputTokens
		total.CacheReadTokens += selected.usage.CacheReadTokens
		total.CacheWriteTokens += selected.usage.CacheWriteTokens
		total.ReasoningTokens += selected.usage.ReasoningTokens
		total.ToolTokens += selected.usage.ToolTokens
		total.TotalTokens += selected.usage.TotalTokens
		total.CostUSD += selected.usage.CostUSD
	}
	return total, true, nil
}

func observationEnvelopeUsageCompleteness(usage core.ObservationUsage) int64 {
	if usage.TotalTokens > 0 {
		return usage.TotalTokens
	}
	return usage.InputTokens + usage.OutputTokens + usage.CacheReadTokens + usage.CacheWriteTokens + usage.ReasoningTokens + usage.ToolTokens
}

func (s *Store) GetObservationTrace(ctx context.Context, traceID string) (*ObservationTrace, error) {
	rows, err := s.queryObservationTraces(ctx, ` WHERE trace_id=?`, []any{traceID})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

func (s *Store) ListObservationTraces(ctx context.Context, filter ObservationTraceFilter) ([]ObservationTrace, error) {
	var clauses []string
	var args []any
	for column, value := range map[string]string{
		"agent_id": filter.AgentID, "runtime_id": filter.RuntimeID, "conversation_id": filter.ConversationID,
		"session_id": filter.SessionID, "status": filter.Status, "source": filter.Source,
	} {
		if value != "" {
			clauses = append(clauses, column+"=?")
			args = append(args, value)
		}
	}
	if !filter.Since.IsZero() {
		clauses = append(clauses, "started_at>=?")
		args = append(args, observationTime(filter.Since))
	}
	if !filter.Until.IsZero() {
		clauses = append(clauses, "started_at<=?")
		args = append(args, observationTime(filter.Until))
	}
	suffix := ""
	if len(clauses) > 0 {
		suffix = " WHERE " + strings.Join(clauses, " AND ")
	}
	suffix += " ORDER BY started_at DESC"
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	suffix += " LIMIT ? OFFSET ?"
	args = append(args, limit, max(filter.Offset, 0))
	return s.queryObservationTraces(ctx, suffix, args)
}

func (s *Store) queryObservationTraces(ctx context.Context, suffix string, args []any) ([]ObservationTrace, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT trace_id,root_span_id,name,started_at,ended_at,agent_id,agent_name,runtime_id,
		conversation_id,session_id,turn_id,source,provenance,quality,status,error_json,model_json,
		input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,reasoning_tokens,tool_tokens,total_tokens,cost_usd,
		span_count,event_count,attributes,created_at,updated_at FROM observation_traces`+suffix, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ObservationTrace
	for rows.Next() {
		var item ObservationTrace
		var started, ended, created, updated string
		var provenance, errorJSON, modelJSON, attributes string
		if err := rows.Scan(&item.TraceID, &item.RootSpanID, &item.Name, &started, &ended, &item.AgentID, &item.AgentName,
			&item.RuntimeID, &item.ConversationID, &item.SessionID, &item.TurnID, &item.Source, &provenance, &item.Quality,
			&item.Status, &errorJSON, &modelJSON, &item.Usage.InputTokens, &item.Usage.OutputTokens,
			&item.Usage.CacheReadTokens, &item.Usage.CacheWriteTokens, &item.Usage.ReasoningTokens, &item.Usage.ToolTokens,
			&item.Usage.TotalTokens, &item.Usage.CostUSD, &item.SpanCount, &item.EventCount, &attributes, &created, &updated); err != nil {
			return nil, err
		}
		item.StartedAt = parseObservationTime(started)
		item.EndedAt = parseOptionalObservationTime(ended)
		item.CreatedAt = parseObservationTime(created)
		item.UpdatedAt = parseObservationTime(updated)
		unmarshalObservationJSON(provenance, &item.Provenance)
		unmarshalObservationPointer(errorJSON, &item.Error)
		unmarshalObservationPointer(modelJSON, &item.Model)
		unmarshalObservationJSON(attributes, &item.Attributes)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListObservationSpans(ctx context.Context, traceID string) ([]ObservationSpan, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT span_id,trace_id,parent_span_id,kind,name,sequence,started_at,ended_at,duration_ms,
		agent_id,runtime_id,conversation_id,session_id,turn_id,source,provenance,quality,status,error_json,model_json,tool_json,payload_id,
		input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,reasoning_tokens,tool_tokens,total_tokens,cost_usd,attributes,created_at,updated_at
		FROM observation_spans WHERE trace_id=? ORDER BY sequence,started_at,span_id`, traceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ObservationSpan
	for rows.Next() {
		var item ObservationSpan
		var started, ended, provenance, errorJSON, modelJSON, toolJSON, attributes, created, updated string
		if err := rows.Scan(&item.SpanID, &item.TraceID, &item.ParentSpanID, &item.Kind, &item.Name, &item.Sequence,
			&started, &ended, &item.DurationMillis, &item.AgentID, &item.RuntimeID, &item.ConversationID, &item.SessionID,
			&item.TurnID, &item.Source, &provenance, &item.Quality, &item.Status, &errorJSON, &modelJSON, &toolJSON,
			&item.PayloadID, &item.Usage.InputTokens, &item.Usage.OutputTokens, &item.Usage.CacheReadTokens,
			&item.Usage.CacheWriteTokens, &item.Usage.ReasoningTokens, &item.Usage.ToolTokens, &item.Usage.TotalTokens,
			&item.Usage.CostUSD, &attributes, &created, &updated); err != nil {
			return nil, err
		}
		item.StartedAt = parseObservationTime(started)
		item.EndedAt = parseOptionalObservationTime(ended)
		item.CreatedAt = parseObservationTime(created)
		item.UpdatedAt = parseObservationTime(updated)
		unmarshalObservationJSON(provenance, &item.Provenance)
		unmarshalObservationPointer(errorJSON, &item.Error)
		unmarshalObservationPointer(modelJSON, &item.Model)
		unmarshalObservationPointer(toolJSON, &item.Tool)
		unmarshalObservationJSON(attributes, &item.Attributes)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListObservationEvents(ctx context.Context, traceID string, afterSequence int64, limit int) ([]core.ObservationEnvelope, error) {
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT envelope_json FROM observation_events
		WHERE trace_id=? AND sequence>=? ORDER BY sequence,timestamp,event_id LIMIT ?`, traceID, afterSequence, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.ObservationEnvelope
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var envelope core.ObservationEnvelope
		if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
			return nil, fmt.Errorf("decode observation event: %w", err)
		}
		out = append(out, envelope)
	}
	return out, rows.Err()
}

// ObservationIngestCursor supports offset/message-ID incremental transcript
// readers without modifying source files.
type ObservationIngestCursor struct {
	Source       string
	Resource     string
	Cursor       string
	MessageID    string
	FileIdentity string
	ByteOffset   int64
	ObservedAt   time.Time
	UpdatedAt    time.Time
}

func (s *Store) UpsertObservationIngestCursor(ctx context.Context, cursor ObservationIngestCursor) error {
	now := cursor.UpdatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO observation_ingest_cursors
		(source,resource,cursor,message_id,file_identity,byte_offset,observed_at,updated_at) VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(source,resource) DO UPDATE SET cursor=excluded.cursor,message_id=excluded.message_id,
		file_identity=excluded.file_identity,byte_offset=excluded.byte_offset,observed_at=excluded.observed_at,updated_at=excluded.updated_at`,
		cursor.Source, cursor.Resource, cursor.Cursor, cursor.MessageID, cursor.FileIdentity, cursor.ByteOffset,
		observationTime(cursor.ObservedAt), observationTime(now))
	return err
}

func (s *Store) GetObservationIngestCursor(ctx context.Context, source, resource string) (*ObservationIngestCursor, error) {
	var item ObservationIngestCursor
	var observed, updated string
	err := s.db.QueryRowContext(ctx, `SELECT source,resource,cursor,message_id,file_identity,byte_offset,observed_at,updated_at
		FROM observation_ingest_cursors WHERE source=? AND resource=?`, source, resource).Scan(
		&item.Source, &item.Resource, &item.Cursor, &item.MessageID, &item.FileIdentity, &item.ByteOffset, &observed, &updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	item.ObservedAt = parseObservationTime(observed)
	item.UpdatedAt = parseObservationTime(updated)
	return &item, nil
}

func observationName(envelope core.ObservationEnvelope) string {
	if envelope.Name != "" {
		return envelope.Name
	}
	return envelope.Kind
}

func observationUsage(usage *core.ObservationUsage) core.ObservationUsage {
	if usage == nil {
		return core.ObservationUsage{}
	}
	return *usage
}

func observationTerminalStatus(status string) bool {
	return status == core.ObservationStatusOK || status == core.ObservationStatusError || status == core.ObservationStatusCancelled
}

func observationTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parseObservationTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}

func parseOptionalObservationTime(value string) *time.Time {
	if value == "" {
		return nil
	}
	parsed := parseObservationTime(value)
	return &parsed
}

func marshalObservationJSON(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func unmarshalObservationJSON(raw string, target any) {
	if raw == "" || raw == "null" {
		return
	}
	_ = json.Unmarshal([]byte(raw), target)
}

func unmarshalObservationPointer[T any](raw string, target **T) {
	if raw == "" || raw == "null" {
		return
	}
	value := new(T)
	if json.Unmarshal([]byte(raw), value) == nil {
		*target = value
	}
}

func nullObservationString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
