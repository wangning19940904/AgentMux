package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// LegacyObservationImportResult reports how many durable legacy rows were
// scanned and how many gained their terminal observation event during this
// run. Re-running the import is safe: stable event and dedupe IDs make already
// imported rows no-ops.
type LegacyObservationImportResult struct {
	UsageScanned  int `json:"usage_scanned"`
	UsageImported int `json:"usage_imported"`
}

const (
	legacyObservationImportBatchSize = 512
	legacyUsageImportCursorKey       = "observation:legacy_usage_rowid"
)

// ImportLegacyObservations backfills the pre-observability usage_records
// table into ObservationEnvelope v1. It deliberately leaves the
// source rows untouched so the old Usage and Gateway APIs remain compatible.
//
// Legacy records do not contain a complete agent/turn/span causal chain. They
// are therefore marked quality=legacy and coverage=partial. Existing trace,
// request, session, turn, runtime and conversation identifiers are retained
// whenever available; missing IDs are deterministically derived from the
// source row's durable identity.
func (s *Store) ImportLegacyObservations(ctx context.Context) (LegacyObservationImportResult, error) {
	var result LegacyObservationImportResult
	if s.IsPostgres() {
		return result, nil
	}
	correlations, err := s.loadLegacyCorrelations(ctx)
	if err != nil {
		return result, fmt.Errorf("load legacy observation correlations: %w", err)
	}

	usageCursor, err := s.legacyObservationImportCursor(ctx, legacyUsageImportCursorKey)
	if err != nil {
		return result, err
	}
	for {
		usageRows, err := s.loadLegacyUsageRowsAfter(ctx, usageCursor, legacyObservationImportBatchSize)
		if err != nil {
			return result, fmt.Errorf("load legacy usage rows: %w", err)
		}
		if len(usageRows) == 0 {
			break
		}
		for _, row := range usageRows {
			result.UsageScanned++
			if row.TraceID != "" {
				exists, err := s.observationTraceExists(ctx, row.TraceID)
				if err != nil {
					return result, err
				}
				if exists {
					continue
				}
			}
			terminalEventID := legacyEventID("usage.end", row.identity())
			alreadyImported, err := s.observationEventExists(ctx, terminalEventID)
			if err != nil {
				return result, fmt.Errorf("check legacy usage %q: %w", row.identity(), err)
			}
			if alreadyImported {
				continue
			}
			envelope := row.envelope(correlations)
			spanID, err := s.resolveLegacyModelSpanID(ctx, envelope.TraceID, row.RequestID, 0, envelope.SpanID)
			if err != nil {
				return result, fmt.Errorf("correlate legacy usage %q: %w", row.identity(), err)
			}
			envelope.SpanID = spanID
			if err := s.RecordObservation(ctx, envelope); err != nil {
				return result, fmt.Errorf("record legacy usage %q: %w", row.identity(), err)
			}
			result.UsageImported++
			// Persist expensive work immediately. Already-imported rows are cheap
			// and checkpointed once per batch below, but a restart must not replay
			// a newly materialized observation from the beginning of the batch.
			if err := s.saveLegacyObservationImportCursor(ctx, legacyUsageImportCursorKey, row.RowID); err != nil {
				return result, err
			}
		}
		usageCursor = usageRows[len(usageRows)-1].RowID
		if err := s.saveLegacyObservationImportCursor(ctx, legacyUsageImportCursorKey, usageCursor); err != nil {
			return result, err
		}
	}

	return result, nil
}

func (s *Store) legacyObservationImportCursor(ctx context.Context, key string) (int64, error) {
	value, ok, err := s.GetSetting(ctx, key)
	if err != nil || !ok {
		return 0, err
	}
	cursor, err := strconv.ParseInt(value, 10, 64)
	if err != nil || cursor < 0 {
		return 0, nil
	}
	return cursor, nil
}

func (s *Store) saveLegacyObservationImportCursor(ctx context.Context, key string, cursor int64) error {
	if err := s.SetSetting(ctx, key, strconv.FormatInt(cursor, 10)); err != nil {
		return fmt.Errorf("save legacy observation cursor %q: %w", key, err)
	}
	return nil
}

type legacyUsageRow struct {
	RowID            int64
	Source           string
	SessionID        string
	ConversationID   string
	TraceID          string
	TurnID           string
	RequestID        string
	RuntimeID        string
	Project          string
	Model            string
	TimestampRaw     string
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	Tool             string
	CostUSD          float64
	Host             string
}

func (s *Store) loadLegacyUsageRowsAfter(ctx context.Context, after int64, limit int) ([]legacyUsageRow, error) {
	query := `SELECT rowid,
		COALESCE(source,''),COALESCE(session_id,''),COALESCE(conversation_id,''),COALESCE(trace_id,''),
		COALESCE(turn_id,''),COALESCE(request_id,''),COALESCE(runtime_id,''),COALESCE(project,''),
		COALESCE(model,''),COALESCE(timestamp,''),COALESCE(input_tokens,0),COALESCE(output_tokens,0),
		COALESCE(cache_read_tokens,0),COALESCE(cache_write_tokens,0),COALESCE(tool,''),
		COALESCE(cost_usd,0),COALESCE(host,'')
		FROM usage_records WHERE rowid>? ORDER BY rowid`
	args := []any{after}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []legacyUsageRow
	for rows.Next() {
		var row legacyUsageRow
		if err := rows.Scan(&row.RowID, &row.Source, &row.SessionID, &row.ConversationID, &row.TraceID,
			&row.TurnID, &row.RequestID, &row.RuntimeID, &row.Project, &row.Model, &row.TimestampRaw,
			&row.InputTokens, &row.OutputTokens, &row.CacheReadTokens, &row.CacheWriteTokens,
			&row.Tool, &row.CostUSD, &row.Host); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (r legacyUsageRow) identity() string {
	// These are the legacy table's primary-key columns. Keeping the identity
	// independent from newly-added nullable fields prevents an upgrade from
	// assigning a second event ID to the same historical row.
	return strings.Join([]string{r.Source, r.SessionID, r.TimestampRaw, r.Host}, "\x00")
}

func (r legacyUsageRow) envelope(c legacyCorrelationIndex) core.ObservationEnvelope {
	identity := r.identity()
	correlation := c.resolve(r.ConversationID, r.SessionID, r.Project)
	runtimeID := firstNonEmpty(r.RuntimeID, correlation.RuntimeID, legacyRuntimeID(r.Source))
	traceID := strings.TrimSpace(r.TraceID)
	if traceID == "" {
		traceID = legacyTraceID("usage", runtimeID, r.SessionID, r.TurnID, r.RequestID, identity)
	}
	spanID := legacyModelSpanFallback(traceID, r.RequestID, 0, "usage\x00"+identity)
	timestamp := parseLegacyObservationTime(r.TimestampRaw)
	attributes := map[string]any{
		"backfilled":      true,
		"coverage":        core.ObservationQualityPartial,
		"legacy_table":    "usage_records",
		"original_source": r.Source,
	}
	setLegacyAttribute(attributes, "project", r.Project)
	setLegacyAttribute(attributes, "host", r.Host)
	setLegacyAttribute(attributes, "legacy_tool", r.Tool)
	if !parseLegacyObservationTimeOK(r.TimestampRaw) {
		setLegacyAttribute(attributes, "legacy_timestamp", r.TimestampRaw)
	}
	return core.ObservationEnvelope{
		Version:        core.ObservationEnvelopeVersion,
		EventID:        legacyEventID("usage.end", identity),
		DedupeKey:      "legacy_usage:" + legacyStableHex("usage.row", identity, 16),
		Time:           timestamp,
		TraceID:        traceID,
		SpanID:         spanID,
		Kind:           "model.request",
		Name:           "Legacy usage request",
		Lifecycle:      core.ObservationLifecycleEnd,
		AgentID:        correlation.AgentID,
		AgentName:      correlation.AgentName,
		RuntimeID:      runtimeID,
		ConversationID: firstNonEmpty(r.ConversationID, correlation.ConversationID),
		SessionID:      r.SessionID,
		TurnID:         r.TurnID,
		Source:         "legacy_usage",
		Provenance:     []string{"usage_records", r.Source},
		Quality:        core.ObservationQualityLegacy,
		Status:         core.ObservationStatusOK,
		Model: &core.ObservationModel{
			Provider:  r.Source,
			Requested: r.Model,
			Resolved:  r.Model,
			RequestID: r.RequestID,
		},
		Usage: &core.ObservationUsage{
			InputTokens:      r.InputTokens,
			OutputTokens:     r.OutputTokens,
			CacheReadTokens:  r.CacheReadTokens,
			CacheWriteTokens: r.CacheWriteTokens,
			TotalTokens:      r.InputTokens + r.OutputTokens + r.CacheReadTokens + r.CacheWriteTokens,
			CostUSD:          r.CostUSD,
			Cumulative:       true,
		},
		Attributes: attributes,
	}
}

func (s *Store) observationEventExists(ctx context.Context, eventID string) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM observation_events WHERE event_id=?)`, eventID).Scan(&exists)
	return exists != 0, err
}

func (s *Store) observationTraceExists(ctx context.Context, traceID string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM observation_traces WHERE trace_id=? LIMIT 1`, traceID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// resolveLegacyModelSpanID lets a legacy source enrich an already-recorded
// request rather than creating a second token-bearing model span. Attempt 0
// and 1 both denote the first request in older producers.
func (s *Store) resolveLegacyModelSpanID(ctx context.Context, traceID, requestID string, attempt int, fallback string) (string, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return fallback, nil
	}
	var spanID string
	query := `SELECT span_id FROM observation_spans
		WHERE trace_id=? AND json_extract(model_json,'$.request_id')=?`
	args := []any{traceID, requestID}
	if attempt <= 1 {
		query += ` AND COALESCE(CAST(json_extract(model_json,'$.attempt') AS INTEGER),0) IN (0,1)`
	} else {
		query += ` AND COALESCE(CAST(json_extract(model_json,'$.attempt') AS INTEGER),0)=?`
		args = append(args, attempt)
	}
	query += ` ORDER BY CASE quality WHEN 'complete' THEN 0 WHEN 'partial' THEN 1 ELSE 2 END,updated_at DESC LIMIT 1`
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&spanID)
	if err == sql.ErrNoRows {
		return fallback, nil
	}
	return spanID, err
}

func legacyModelSpanFallback(traceID, requestID string, attempt int, rowIdentity string) string {
	if strings.TrimSpace(requestID) != "" {
		if attempt <= 1 {
			attempt = 0
		}
		return legacyStableHex("model.request", strings.Join([]string{traceID, requestID, strconv.Itoa(attempt)}, "\x00"), 8)
	}
	return legacyStableHex("model.request.row", traceID+"\x00"+rowIdentity, 8)
}

func legacyTraceID(source, runtimeID, sessionID, turnID, requestID, rowIdentity string) string {
	if strings.TrimSpace(requestID) != "" {
		return legacyStableHex("trace.request", strings.Join([]string{runtimeID, sessionID, requestID}, "\x00"), 16)
	}
	if strings.TrimSpace(turnID) != "" {
		return legacyStableHex("trace.turn", strings.Join([]string{runtimeID, sessionID, turnID}, "\x00"), 16)
	}
	return legacyStableHex("trace.row", source+"\x00"+rowIdentity, 16)
}

func legacyEventID(namespace, identity string) string {
	return "obs_" + legacyStableHex(namespace, identity, 16)
}

func legacyStableHex(namespace, identity string, size int) string {
	digest := sha256.Sum256([]byte("agentmux.observation.legacy.v1\x00" + namespace + "\x00" + identity))
	if size <= 0 || size > len(digest) {
		size = len(digest)
	}
	return hex.EncodeToString(digest[:size])
}

func parseLegacyObservationTime(raw string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05"} {
		if value, err := time.Parse(layout, strings.TrimSpace(raw)); err == nil {
			return value.UTC()
		}
	}
	// A stable epoch is safer than Normalize's current time for malformed
	// historical rows and makes import results reproducible across machines.
	return time.Unix(0, 0).UTC()
}

func parseLegacyObservationTimeOK(raw string) bool {
	return !parseLegacyObservationTime(raw).Equal(time.Unix(0, 0).UTC()) || strings.TrimSpace(raw) == time.Unix(0, 0).UTC().Format(time.RFC3339Nano)
}

type legacyCorrelation struct {
	ConversationID string
	AgentID        string
	AgentName      string
	RuntimeID      string
}

type legacyCorrelationIndex struct {
	byConversation map[string]legacyCorrelation
	bySession      map[string]legacyCorrelation
	byWorkDir      map[string]legacyCorrelation
}

func (s *Store) loadLegacyCorrelations(ctx context.Context) (legacyCorrelationIndex, error) {
	index := legacyCorrelationIndex{
		byConversation: make(map[string]legacyCorrelation),
		bySession:      make(map[string]legacyCorrelation),
		byWorkDir:      make(map[string]legacyCorrelation),
	}
	type agentInfo struct{ Name, RuntimeID, WorkDir string }
	agents := make(map[string]agentInfo)
	agentRows, err := s.db.QueryContext(ctx, `SELECT COALESCE(id,''),COALESCE(name,''),COALESCE(runtime_id,''),COALESCE(work_dir,'') FROM agent_instances`)
	if err != nil {
		return index, err
	}
	for agentRows.Next() {
		var id string
		var info agentInfo
		if err := agentRows.Scan(&id, &info.Name, &info.RuntimeID, &info.WorkDir); err != nil {
			agentRows.Close()
			return index, err
		}
		agents[id] = info
		if key := legacyWorkDirKey(info.WorkDir); key != "" {
			correlation := legacyCorrelation{AgentID: id, AgentName: info.Name, RuntimeID: info.RuntimeID}
			if _, exists := index.byWorkDir[key]; exists {
				// Multiple agents can intentionally share a workspace; do not guess.
				index.byWorkDir[key] = legacyCorrelation{}
			} else {
				index.byWorkDir[key] = correlation
			}
		}
	}
	if err := agentRows.Err(); err != nil {
		agentRows.Close()
		return index, err
	}
	agentRows.Close()

	conversationRows, err := s.db.QueryContext(ctx, `SELECT COALESCE(id,''),COALESCE(native_session_id,''),COALESCE(agent_id,''),COALESCE(work_dir,'')
		FROM conversations ORDER BY CASE WHEN ended_at IS NULL OR ended_at='' THEN 0 ELSE 1 END,updated_at DESC`)
	if err != nil {
		return index, err
	}
	defer conversationRows.Close()
	for conversationRows.Next() {
		var conversationID, sessionID, agentID, workDir string
		if err := conversationRows.Scan(&conversationID, &sessionID, &agentID, &workDir); err != nil {
			return index, err
		}
		info := agents[agentID]
		correlation := legacyCorrelation{
			ConversationID: conversationID,
			AgentID:        agentID,
			AgentName:      info.Name,
			RuntimeID:      info.RuntimeID,
		}
		index.byConversation[conversationID] = correlation
		if sessionID != "" {
			if _, exists := index.bySession[sessionID]; !exists {
				index.bySession[sessionID] = correlation
			}
		}
		if key := legacyWorkDirKey(workDir); key != "" {
			if _, exists := index.byWorkDir[key]; !exists {
				index.byWorkDir[key] = correlation
			}
		}
	}
	return index, conversationRows.Err()
}

func (i legacyCorrelationIndex) resolve(conversationID, sessionID, workDir string) legacyCorrelation {
	if conversationID != "" {
		if value, ok := i.byConversation[conversationID]; ok {
			return value
		}
	}
	if sessionID != "" {
		if value, ok := i.bySession[sessionID]; ok {
			return value
		}
	}
	if key := legacyWorkDirKey(workDir); key != "" {
		return i.byWorkDir[key]
	}
	return legacyCorrelation{ConversationID: conversationID}
}

func legacyWorkDirKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return filepath.Clean(value)
}

func legacyRuntimeID(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "claude", "claudecode", "claudecode-cli", "claude-code-cli":
		return "claude"
	case "codex", "codex-cli", "codex-app", "codex-desktop", "codex-app-server":
		return "codex"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func setLegacyAttribute(attributes map[string]any, key, value string) {
	if strings.TrimSpace(value) != "" {
		attributes[key] = value
	}
}
