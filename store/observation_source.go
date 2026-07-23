package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/agentnexus/agentnexus/core"
)

// ObservationTranscriptPayloadCandidate is an encrypted transcript payload
// that may be replaced by a verified local-file reference.
type ObservationTranscriptPayloadCandidate struct {
	RowID         int64
	Envelope      core.ObservationEnvelope
	PayloadID     string
	ContentType   string
	ContentSHA256 string
	OriginalBytes int64
	StoredBytes   int64
	Redacted      bool
	ExpiresAt     time.Time
}

type ObservationPayloadSourceReplacement struct {
	RowID     int64
	EventID   string
	PayloadID string
	Envelope  core.ObservationEnvelope
	Ref       core.ObservationPayloadRef
}

// ListObservationTranscriptPayloadCandidates pages by SQLite rowid so a large
// migration never retains a long-lived read snapshot or repeatedly scans the
// multi-gigabyte payload tables.
func (s *Store) ListObservationTranscriptPayloadCandidates(ctx context.Context, afterRowID int64, limit int) ([]ObservationTranscriptPayloadCandidate, error) {
	if limit <= 0 || limit > 1000 {
		limit = observationCleanupBatchSize
	}
	rows, err := s.db.QueryContext(ctx, `SELECT e.rowid,e.envelope_json,e.payload_id,
		p.content_type,p.sha256,p.original_bytes,p.stored_bytes,p.redacted,p.expires_at
		FROM observation_events e
		JOIN observation_payloads p ON p.payload_id=e.payload_id
		WHERE e.rowid>? AND e.source='transcript' AND e.payload_id<>''
		ORDER BY e.rowid LIMIT ?`, afterRowID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ObservationTranscriptPayloadCandidate
	for rows.Next() {
		var item ObservationTranscriptPayloadCandidate
		var envelopeJSON, expiresAt string
		if err := rows.Scan(&item.RowID, &envelopeJSON, &item.PayloadID, &item.ContentType, &item.ContentSHA256,
			&item.OriginalBytes, &item.StoredBytes, &item.Redacted, &expiresAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(envelopeJSON), &item.Envelope); err != nil {
			return nil, fmt.Errorf("decode transcript observation event row %d: %w", item.RowID, err)
		}
		item.ExpiresAt = parseObservationTime(expiresAt)
		result = append(result, item)
	}
	return result, rows.Err()
}

// ReplaceObservationPayloadsWithSources atomically promotes verified source
// references before deleting their duplicated encrypted SQLite bodies.
func (s *Store) ReplaceObservationPayloadsWithSources(ctx context.Context, replacements []ObservationPayloadSourceReplacement) (int, error) {
	if len(replacements) == 0 {
		return 0, nil
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	replaced := 0
	payloadIDs := make([]string, 0, len(replacements))
	for _, replacement := range replacements {
		envelope := replacement.Envelope
		envelope.Content = nil
		envelope.PayloadRef = &replacement.Ref
		encoded, err := json.Marshal(envelope)
		if err != nil {
			return replaced, err
		}
		result, err := tx.ExecContext(ctx, `UPDATE observation_events SET envelope_json=?
			WHERE rowid=? AND event_id=? AND payload_id=?`, string(encoded), replacement.RowID, replacement.EventID, replacement.PayloadID)
		if err != nil {
			return replaced, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return replaced, err
		}
		if changed != 1 {
			return replaced, fmt.Errorf("transcript payload source replacement changed %d rows for %s", changed, replacement.EventID)
		}
		payloadIDs = append(payloadIDs, replacement.PayloadID)
		replaced++
	}
	// A payload is normally owned by one event. Delete a batch in two indexed
	// statements, while retaining any encrypted body that still has a legacy
	// second owner without its own verified source reference.
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(payloadIDs)), ",")
	legacyOwners := `SELECT payload_id FROM observation_events WHERE payload_id IN (` + placeholders + `)
		AND payload_id<>'' AND COALESCE(json_extract(envelope_json,'$.payload_ref.storage'),'')<>?`
	deleteArgs := make([]any, 0, len(payloadIDs)*2+1)
	for _, payloadID := range payloadIDs {
		deleteArgs = append(deleteArgs, payloadID)
	}
	for _, payloadID := range payloadIDs {
		deleteArgs = append(deleteArgs, payloadID)
	}
	deleteArgs = append(deleteArgs, core.ObservationPayloadStorageTranscriptFile)
	if _, err := tx.ExecContext(ctx, `DELETE FROM observation_payload_chunks WHERE payload_id IN (`+placeholders+`)
		AND payload_id NOT IN (`+legacyOwners+`)`, deleteArgs...); err != nil {
		return replaced, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM observation_payloads WHERE payload_id IN (`+placeholders+`)
		AND payload_id NOT IN (`+legacyOwners+`)`, deleteArgs...); err != nil {
		return replaced, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return replaced, nil
}
