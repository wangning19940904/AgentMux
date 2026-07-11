package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/agentnexus/agentnexus/core"
)

type ObservationExportItem struct {
	ID             string                   `json:"id"`
	Exporter       string                   `json:"exporter"`
	EventID        string                   `json:"event_id"`
	TraceID        string                   `json:"trace_id"`
	Envelope       core.ObservationEnvelope `json:"envelope"`
	IncludeContent bool                     `json:"include_content"`
	Status         string                   `json:"status"`
	Attempts       int                      `json:"attempts"`
	NextAttemptAt  *time.Time               `json:"next_attempt_at,omitempty"`
	LastError      string                   `json:"last_error,omitempty"`
	CreatedAt      time.Time                `json:"created_at"`
	UpdatedAt      time.Time                `json:"updated_at"`
}

// EnqueueObservationExport is idempotent per exporter/event pair. Envelope
// JSON contains only metadata and an encrypted payload reference.
func (s *Store) EnqueueObservationExport(ctx context.Context, exporter string, envelope core.ObservationEnvelope, includeContent bool) error {
	exporter = strings.TrimSpace(exporter)
	if exporter == "" {
		return errors.New("observation exporter name is required")
	}
	envelope.Content = nil
	envelope.Normalize()
	if err := envelope.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(exporter + "\x00" + envelope.EventID))
	id := "export_" + hex.EncodeToString(digest[:16])
	now := observationTime(time.Now().UTC())
	_, err = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO observation_export_outbox
		(id,exporter,event_id,trace_id,envelope_json,include_content,status,attempts,next_attempt_at,last_error,created_at,updated_at)
		VALUES(?,?,?,?,?,?,'pending',0,'','',?,?)`, id, exporter, envelope.EventID, envelope.TraceID, string(raw), includeContent, now, now)
	return err
}

// TrimObservationExportQueue bounds one exporter's pending/retry queue without
// affecting any other exporter. Oldest overflow entries are retained as
// discarded audit rows until normal retention cleanup removes them.
func (s *Store) TrimObservationExportQueue(ctx context.Context, exporter string, maximum int) error {
	if maximum <= 0 {
		maximum = 10000
	}
	_, err := s.db.ExecContext(ctx, `UPDATE observation_export_outbox SET status='discarded',
		last_error='export queue capacity exceeded',updated_at=? WHERE id IN (
			SELECT id FROM observation_export_outbox WHERE exporter=? AND status IN ('pending','retry')
			ORDER BY created_at DESC,id DESC LIMIT -1 OFFSET ?
		)`, observationTime(time.Now().UTC()), exporter, maximum)
	return err
}

func (s *Store) ListPendingObservationExports(ctx context.Context, exporter string, now time.Time, limit int) ([]ObservationExportItem, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,exporter,event_id,trace_id,envelope_json,include_content,status,attempts,
		next_attempt_at,last_error,created_at,updated_at FROM observation_export_outbox
		WHERE exporter=? AND status IN ('pending','retry') AND (next_attempt_at='' OR next_attempt_at<=?)
		ORDER BY created_at,id LIMIT ?`, exporter, observationTime(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ObservationExportItem
	for rows.Next() {
		var item ObservationExportItem
		var raw, nextAttempt, created, updated string
		if err := rows.Scan(&item.ID, &item.Exporter, &item.EventID, &item.TraceID, &raw, &item.IncludeContent,
			&item.Status, &item.Attempts, &nextAttempt, &item.LastError, &created, &updated); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &item.Envelope); err != nil {
			return nil, fmt.Errorf("decode outbox envelope %s: %w", item.ID, err)
		}
		item.NextAttemptAt = parseOptionalObservationTime(nextAttempt)
		item.CreatedAt = parseObservationTime(created)
		item.UpdatedAt = parseObservationTime(updated)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) CompleteObservationExport(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE observation_export_outbox SET status='sent',last_error='',updated_at=? WHERE id=?`, observationTime(time.Now().UTC()), id)
	if err != nil {
		return err
	}
	return requireObservationRow(result, "observation export item")
}

func (s *Store) RetryObservationExport(ctx context.Context, id string, failure error, nextAttempt time.Time) error {
	message := ""
	if failure != nil {
		message = failure.Error()
	}
	result, err := s.db.ExecContext(ctx, `UPDATE observation_export_outbox
		SET status='retry',attempts=attempts+1,next_attempt_at=?,last_error=?,updated_at=? WHERE id=?`,
		observationTime(nextAttempt), message, observationTime(time.Now().UTC()), id)
	if err != nil {
		return err
	}
	return requireObservationRow(result, "observation export item")
}

type ObservationInsight struct {
	ID                      string    `json:"id"`
	RuleID                  string    `json:"rule_id"`
	AgentID                 string    `json:"agent_id,omitempty"`
	TraceID                 string    `json:"trace_id,omitempty"`
	Severity                string    `json:"severity,omitempty"`
	Status                  string    `json:"status"`
	Title                   string    `json:"title"`
	Summary                 string    `json:"summary,omitempty"`
	Suggestion              string    `json:"suggestion,omitempty"`
	SampleSize              int64     `json:"sample_size"`
	Confidence              float64   `json:"confidence"`
	EstimatedTokenSavings   int64     `json:"estimated_token_savings"`
	EstimatedCostSavingsUSD float64   `json:"estimated_cost_savings_usd"`
	RelatedTraceIDs         []string  `json:"related_trace_ids,omitempty"`
	OnlySuggestion          bool      `json:"only_suggestion"`
	CreatedAt               time.Time `json:"created_at"`
	UpdatedAt               time.Time `json:"updated_at"`
}

type ObservationInsightFilter struct {
	AgentID string
	Status  string
	RuleID  string
	Limit   int
}

func (s *Store) UpsertObservationInsight(ctx context.Context, insight ObservationInsight) error {
	if insight.ID == "" || insight.RuleID == "" || insight.Title == "" {
		return errors.New("observation insight id, rule_id, and title are required")
	}
	now := time.Now().UTC()
	if insight.CreatedAt.IsZero() {
		insight.CreatedAt = now
	}
	if insight.UpdatedAt.IsZero() {
		insight.UpdatedAt = now
	}
	if insight.Status == "" {
		insight.Status = "open"
	}
	// Insights are always advisory in v1. Ignore a caller-provided false zero
	// value rather than allowing an optimization to become self-applying.
	insight.OnlySuggestion = true
	related := marshalObservationJSON(insight.RelatedTraceIDs)
	_, err := s.db.ExecContext(ctx, `INSERT INTO observation_insights
		(id,rule_id,agent_id,trace_id,severity,status,title,summary,suggestion,sample_size,confidence,estimated_token_savings,
		estimated_cost_savings_usd,related_trace_ids,only_suggestion,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET rule_id=excluded.rule_id,agent_id=excluded.agent_id,trace_id=excluded.trace_id,
		severity=excluded.severity,status=excluded.status,title=excluded.title,summary=excluded.summary,suggestion=excluded.suggestion,
		sample_size=excluded.sample_size,confidence=excluded.confidence,estimated_token_savings=excluded.estimated_token_savings,
		estimated_cost_savings_usd=excluded.estimated_cost_savings_usd,related_trace_ids=excluded.related_trace_ids,
		only_suggestion=1,updated_at=excluded.updated_at`,
		insight.ID, insight.RuleID, insight.AgentID, insight.TraceID, insight.Severity, insight.Status, insight.Title,
		insight.Summary, insight.Suggestion, insight.SampleSize, insight.Confidence, insight.EstimatedTokenSavings,
		insight.EstimatedCostSavingsUSD, related, true, observationTime(insight.CreatedAt), observationTime(insight.UpdatedAt))
	return err
}

// ResolveObservationInsightsExcept closes advisory findings that are no longer
// produced by the current evaluation window. A later recurrence reopens the
// stable ID through UpsertObservationInsight.
func (s *Store) ResolveObservationInsightsExcept(ctx context.Context, activeIDs []string) error {
	query := `UPDATE observation_insights SET status='resolved',updated_at=? WHERE status='open'`
	args := []any{observationTime(time.Now().UTC())}
	if len(activeIDs) > 0 {
		placeholders := make([]string, len(activeIDs))
		for index, id := range activeIDs {
			placeholders[index] = "?"
			args = append(args, id)
		}
		query += " AND id NOT IN (" + strings.Join(placeholders, ",") + ")"
	}
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}

func (s *Store) ListObservationInsights(ctx context.Context, filter ObservationInsightFilter) ([]ObservationInsight, error) {
	var clauses []string
	var args []any
	if filter.AgentID != "" {
		clauses = append(clauses, "agent_id=?")
		args = append(args, filter.AgentID)
	}
	if filter.Status != "" {
		clauses = append(clauses, "status=?")
		args = append(args, filter.Status)
	}
	if filter.RuleID != "" {
		clauses = append(clauses, "rule_id=?")
		args = append(args, filter.RuleID)
	}
	query := `SELECT id,rule_id,agent_id,trace_id,severity,status,title,summary,suggestion,sample_size,confidence,
		estimated_token_savings,estimated_cost_savings_usd,related_trace_ids,only_suggestion,created_at,updated_at FROM observation_insights`
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY created_at DESC,id LIMIT ?"
	if filter.Limit <= 0 || filter.Limit > 1000 {
		filter.Limit = 100
	}
	args = append(args, filter.Limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ObservationInsight
	for rows.Next() {
		var insight ObservationInsight
		var related, created, updated string
		if err := rows.Scan(&insight.ID, &insight.RuleID, &insight.AgentID, &insight.TraceID, &insight.Severity, &insight.Status,
			&insight.Title, &insight.Summary, &insight.Suggestion, &insight.SampleSize, &insight.Confidence,
			&insight.EstimatedTokenSavings, &insight.EstimatedCostSavingsUSD, &related, &insight.OnlySuggestion, &created, &updated); err != nil {
			return nil, err
		}
		unmarshalObservationJSON(related, &insight.RelatedTraceIDs)
		insight.CreatedAt = parseObservationTime(created)
		insight.UpdatedAt = parseObservationTime(updated)
		out = append(out, insight)
	}
	return out, rows.Err()
}

type ObservationIntegrationOwnership struct {
	InstallID          string         `json:"install_id"`
	Host               string         `json:"host"`
	Scope              string         `json:"scope"`
	ResourceKey        string         `json:"resource_key"`
	Version            string         `json:"version,omitempty"`
	SHA256             string         `json:"sha256,omitempty"`
	HandlerFingerprint string         `json:"handler_fingerprint,omitempty"`
	TargetPath         string         `json:"target_path,omitempty"`
	BeforeHash         string         `json:"before_hash,omitempty"`
	AfterHash          string         `json:"after_hash,omitempty"`
	Metadata           map[string]any `json:"metadata,omitempty"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
}

var ErrObservationResourceOwned = errors.New("observation integration resource is owned by another install")

// ClaimObservationIntegrationOwnership inserts or refreshes only the same
// install's record. It refuses to replace an existing third-party/other install
// owner and returns claimed=false with no mutation.
func (s *Store) ClaimObservationIntegrationOwnership(ctx context.Context, ownership ObservationIntegrationOwnership) (bool, error) {
	if ownership.InstallID == "" || ownership.Host == "" || ownership.Scope == "" || ownership.ResourceKey == "" {
		return false, errors.New("install_id, host, scope, and resource_key are required")
	}
	var existingInstallID string
	err := s.db.QueryRowContext(ctx, `SELECT install_id FROM observation_integration_ownership
		WHERE host=? AND scope=? AND resource_key=?`, ownership.Host, ownership.Scope, ownership.ResourceKey).Scan(&existingInstallID)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if err == nil && existingInstallID != ownership.InstallID {
		return false, ErrObservationResourceOwned
	}
	now := time.Now().UTC()
	if ownership.CreatedAt.IsZero() {
		ownership.CreatedAt = now
	}
	if ownership.UpdatedAt.IsZero() {
		ownership.UpdatedAt = now
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO observation_integration_ownership
		(install_id,host,scope,resource_key,version,sha256,handler_fingerprint,target_path,before_hash,after_hash,metadata,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(install_id,resource_key) DO UPDATE SET version=excluded.version,
		sha256=excluded.sha256,handler_fingerprint=excluded.handler_fingerprint,target_path=excluded.target_path,
		before_hash=excluded.before_hash,after_hash=excluded.after_hash,metadata=excluded.metadata,updated_at=excluded.updated_at`,
		ownership.InstallID, ownership.Host, ownership.Scope, ownership.ResourceKey, ownership.Version, ownership.SHA256,
		ownership.HandlerFingerprint, ownership.TargetPath, ownership.BeforeHash, ownership.AfterHash,
		marshalObservationJSON(ownership.Metadata), observationTime(ownership.CreatedAt), observationTime(ownership.UpdatedAt))
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint") {
		return false, ErrObservationResourceOwned
	}
	return err == nil, err
}

func (s *Store) ListObservationIntegrationOwnership(ctx context.Context, host, scope string) ([]ObservationIntegrationOwnership, error) {
	query := `SELECT install_id,host,scope,resource_key,version,sha256,handler_fingerprint,target_path,before_hash,after_hash,
		metadata,created_at,updated_at FROM observation_integration_ownership WHERE 1=1`
	var args []any
	if host != "" {
		query += " AND host=?"
		args = append(args, host)
	}
	if scope != "" {
		query += " AND scope=?"
		args = append(args, scope)
	}
	query += " ORDER BY host,scope,resource_key"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ObservationIntegrationOwnership
	for rows.Next() {
		var item ObservationIntegrationOwnership
		var metadata, created, updated string
		if err := rows.Scan(&item.InstallID, &item.Host, &item.Scope, &item.ResourceKey, &item.Version, &item.SHA256,
			&item.HandlerFingerprint, &item.TargetPath, &item.BeforeHash, &item.AfterHash, &metadata, &created, &updated); err != nil {
			return nil, err
		}
		unmarshalObservationJSON(metadata, &item.Metadata)
		item.CreatedAt = parseObservationTime(created)
		item.UpdatedAt = parseObservationTime(updated)
		out = append(out, item)
	}
	return out, rows.Err()
}

// DeleteObservationIntegrationOwnership deletes only when the install ID and
// expected handler fingerprint still match. Drift is preserved and reported.
func (s *Store) DeleteObservationIntegrationOwnership(ctx context.Context, installID, resourceKey, expectedFingerprint string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM observation_integration_ownership
		WHERE install_id=? AND resource_key=? AND handler_fingerprint=?`, installID, resourceKey, expectedFingerprint)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

type ObservationResourceLease struct {
	ResourceKey string         `json:"resource_key"`
	OwnerID     string         `json:"owner_id"`
	InstallID   string         `json:"install_id,omitempty"`
	LeaseToken  string         `json:"lease_token"`
	AcquiredAt  time.Time      `json:"acquired_at"`
	ExpiresAt   time.Time      `json:"expires_at"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// AcquireObservationResourceLease atomically acquires an absent/expired lease.
// It never stops or steals from a live unknown owner.
func (s *Store) AcquireObservationResourceLease(ctx context.Context, resourceKey, ownerID, installID string, ttl time.Duration, metadata map[string]any) (*ObservationResourceLease, bool, error) {
	if resourceKey == "" || ownerID == "" {
		return nil, false, errors.New("resource_key and owner_id are required")
	}
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	now := time.Now().UTC()
	lease := &ObservationResourceLease{ResourceKey: resourceKey, OwnerID: ownerID, InstallID: installID,
		LeaseToken: randomObservationLeaseToken(), AcquiredAt: now, ExpiresAt: now.Add(ttl), Metadata: metadata}
	result, err := s.db.ExecContext(ctx, `INSERT INTO observation_resource_leases
		(resource_key,owner_id,install_id,lease_token,acquired_at,expires_at,metadata) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(resource_key) DO UPDATE SET owner_id=excluded.owner_id,install_id=excluded.install_id,
		lease_token=excluded.lease_token,acquired_at=excluded.acquired_at,expires_at=excluded.expires_at,metadata=excluded.metadata
		WHERE observation_resource_leases.expires_at<=?`, resourceKey, ownerID, installID, lease.LeaseToken,
		observationTime(now), observationTime(lease.ExpiresAt), marshalObservationJSON(metadata), observationTime(now))
	if err != nil {
		return nil, false, err
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		return nil, false, err
	}
	return lease, true, nil
}

func (s *Store) ReleaseObservationResourceLease(ctx context.Context, resourceKey, leaseToken string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM observation_resource_leases WHERE resource_key=? AND lease_token=?`, resourceKey, leaseToken)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *Store) RenewObservationResourceLease(ctx context.Context, resourceKey, leaseToken string, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE observation_resource_leases SET expires_at=?
		WHERE resource_key=? AND lease_token=? AND expires_at>?`, observationTime(now.Add(ttl)), resourceKey, leaseToken, observationTime(now))
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func randomObservationLeaseToken() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return fmt.Sprintf("lease-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value)
}

func requireObservationRow(result sql.Result, name string) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("%s not found", name)
	}
	return nil
}
