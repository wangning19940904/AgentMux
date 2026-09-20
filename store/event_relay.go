package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wangning19940904/AgentMux/core"
)

// Millisecond integers avoid dialect-dependent timestamp comparison semantics.
const eventRelaySchema = `
CREATE TABLE IF NOT EXISTS event_subscriptions (
 id TEXT PRIMARY KEY, owner_tenant_id TEXT NOT NULL DEFAULT '', subscription_key TEXT NOT NULL,
 config TEXT NOT NULL, secret TEXT NOT NULL, paused BIGINT NOT NULL DEFAULT 0,
 deleted BIGINT NOT NULL DEFAULT 0, created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL,
 lease_token TEXT NOT NULL DEFAULT '', lease_until BIGINT NOT NULL DEFAULT 0,
 UNIQUE(owner_tenant_id,subscription_key)
);
CREATE TABLE IF NOT EXISTS relay_events (
 id TEXT PRIMARY KEY, dedupe_key TEXT NOT NULL UNIQUE, source_ref TEXT NOT NULL,
 envelope TEXT NOT NULL, created_at BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS event_deliveries (
 id TEXT PRIMARY KEY, subscription_id TEXT NOT NULL REFERENCES event_subscriptions(id),
 event_id TEXT NOT NULL REFERENCES relay_events(id), status TEXT NOT NULL,
 attempts BIGINT NOT NULL DEFAULT 0, first_attempt_at BIGINT NOT NULL DEFAULT 0,
 next_attempt_at BIGINT NOT NULL, lease_token TEXT NOT NULL DEFAULT '', lease_until BIGINT NOT NULL DEFAULT 0,
 last_error TEXT NOT NULL DEFAULT '', created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL,
 UNIQUE(subscription_id,event_id)
);
CREATE INDEX IF NOT EXISTS idx_event_deliveries_ready ON event_deliveries(status,next_attempt_at);
CREATE INDEX IF NOT EXISTS idx_event_deliveries_subscription ON event_deliveries(subscription_id,created_at);
CREATE TABLE IF NOT EXISTS event_delivery_attempts (
 id TEXT PRIMARY KEY, delivery_id TEXT NOT NULL REFERENCES event_deliveries(id),
 started_at BIGINT NOT NULL, finished_at BIGINT NOT NULL DEFAULT 0,
 http_status BIGINT NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_event_attempts_delivery ON event_delivery_attempts(delivery_id,started_at);
`

const eventSubscriptionColumns = `id,owner_tenant_id,subscription_key,config,secret,paused,deleted,created_at,updated_at`

func scanEventSubscription(row scanner) (*core.EventSubscription, error) {
	var id, owner, key, cfg, secret string
	var paused, deleted, created, updated int64
	if err := row.Scan(&id, &owner, &key, &cfg, &secret, &paused, &deleted, &created, &updated); err != nil {
		return nil, err
	}
	var sub core.EventSubscription
	if err := json.Unmarshal([]byte(cfg), &sub); err != nil {
		return nil, err
	}
	sub.ID = id
	sub.OwnerTenantID = owner
	sub.Key = key
	sub.Secret = secret
	sub.Paused = paused != 0
	sub.Deleted = deleted != 0
	sub.CreatedAt = time.UnixMilli(created).UTC()
	sub.UpdatedAt = time.UnixMilli(updated).UTC()
	return &sub, nil
}

// ListEventSubscriptions uses nil for the administrator's all-tenants view.
func (s *Store) ListEventSubscriptions(ctx context.Context, owner *string) ([]core.EventSubscription, error) {
	q := `SELECT ` + eventSubscriptionColumns + ` FROM event_subscriptions WHERE deleted=0`
	args := []any{}
	if owner != nil {
		q += ` AND owner_tenant_id=?`
		args = append(args, *owner)
	}
	q += ` ORDER BY created_at,id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.EventSubscription{}
	for rows.Next() {
		sub, err := scanEventSubscription(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sub)
	}
	return out, rows.Err()
}

func (s *Store) GetEventSubscription(ctx context.Context, id string) (*core.EventSubscription, error) {
	sub, err := scanEventSubscription(s.db.QueryRowContext(ctx, `SELECT `+eventSubscriptionColumns+` FROM event_subscriptions WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return sub, err
}

// SaveEventSubscription never replaces an existing signing secret. IDs and keys
// of deleted subscriptions remain reserved so a retry cannot resurrect one.
func (s *Store) SaveEventSubscription(ctx context.Context, sub *core.EventSubscription) (bool, error) {
	proposed := "sub_" + uuid.NewString()
	now := time.Now().UTC().UnixMilli()
	if sub.ID != "" {
		existing, err := s.GetEventSubscription(ctx, sub.ID)
		if err != nil {
			return false, err
		}
		if existing == nil || existing.Deleted || existing.OwnerTenantID != sub.OwnerTenantID || existing.Key != sub.Key {
			return false, fmt.Errorf("subscription identity cannot be changed")
		}
	}
	cfg, err := json.Marshal(sub)
	if err != nil {
		return false, err
	}
	paused := 0
	if sub.Paused {
		paused = 1
	}
	var id, secret string
	var created int64
	err = s.writer.QueryRowContext(ctx, `INSERT INTO event_subscriptions
 (id,owner_tenant_id,subscription_key,config,secret,paused,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?)
 ON CONFLICT(owner_tenant_id,subscription_key) DO UPDATE SET config=excluded.config,paused=excluded.paused,updated_at=excluded.updated_at
 WHERE event_subscriptions.deleted=0
 RETURNING id,secret,created_at`, proposed, sub.OwnerTenantID, sub.Key, string(cfg), sub.Secret, paused, now, now).Scan(&id, &secret, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("subscription key belongs to a deleted subscription; use a new key")
	}
	if err != nil {
		return false, err
	}
	sub.ID = id
	sub.Secret = secret
	sub.CreatedAt = time.UnixMilli(created).UTC()
	sub.UpdatedAt = time.UnixMilli(now).UTC()
	return id == proposed, nil
}

func (s *Store) DeleteEventSubscription(ctx context.Context, id string) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	if _, err = tx.ExecContext(ctx, `UPDATE event_subscriptions SET deleted=1,secret='',updated_at=?,lease_token='',lease_until=0 WHERE id=?`, now, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE event_deliveries SET status='cancelled',lease_token='',lease_until=0,updated_at=? WHERE subscription_id=? AND status IN ('pending','retry','delivering','blocked')`, now, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RotateEventSecret(ctx context.Context, id, secret string) error {
	result, err := s.writer.ExecContext(ctx, `UPDATE event_subscriptions SET secret=?,updated_at=? WHERE id=? AND deleted=0`, secret, time.Now().UnixMilli(), id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("subscription is unavailable")
	}
	return nil
}

type eventQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func eventSourceOwner(ctx context.Context, q eventQueryer, ref string) (string, error) {
	if ref == "system" {
		return "", nil
	}
	kind, id, ok := strings.Cut(ref, ":")
	if !ok || id == "" {
		return "", fmt.Errorf("invalid event source")
	}
	table := map[string]string{"channel": "channels", "agent": "agent_instances", "trigger": "triggers"}[kind]
	if table == "" {
		return "", fmt.Errorf("invalid event source")
	}
	var owner sql.NullString
	err := q.QueryRowContext(ctx, `SELECT owner_tenant_id FROM `+table+` WHERE id=?`, id).Scan(&owner)
	return owner.String, err
}

func eventSourceAllowed(ctx context.Context, q eventQueryer, tenant, ref string) (bool, error) {
	owner, err := eventSourceOwner(ctx, q, ref)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if tenant == "" {
		return true, nil
	}
	if ref == "system" {
		return false, nil
	}
	var status string
	err = q.QueryRowContext(ctx, `SELECT status FROM tenants WHERE id=?`, tenant).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if status != core.TenantStatusActive {
		return false, nil
	}
	if owner == tenant {
		return true, nil
	}
	var level string
	err = q.QueryRowContext(ctx, `SELECT level FROM resource_grants WHERE tenant_id=? AND resource_type=? AND resource_id=?`, tenant, core.ResourceTypeEventSource, ref).Scan(&level)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return core.GrantSatisfies(level, core.GrantLevelUse), nil
}

func (s *Store) EventSourceAllowed(ctx context.Context, tenant, ref string) (bool, error) {
	return eventSourceAllowed(ctx, s.db, tenant, ref)
}

// IngestRelayEvent atomically snapshots matching subscriptions and creates their
// outbox entries. A duplicate never adds subscribers that registered later.
func (s *Store) IngestRelayEvent(ctx context.Context, event core.RelayEvent, targetID string) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	subscriptionQuery := `SELECT ` + eventSubscriptionColumns + ` FROM event_subscriptions WHERE deleted=0`
	if s.dialect == DialectPostgres {
		subscriptionQuery += ` FOR SHARE`
	}
	rows, err := tx.QueryContext(ctx, subscriptionQuery)
	if err != nil {
		return err
	}
	matches := []core.EventSubscription{}
	for rows.Next() {
		sub, e := scanEventSubscription(rows)
		if e != nil {
			rows.Close()
			return e
		}
		if (targetID == "" && sub.Matches(event)) || (targetID != "" && sub.ID == targetID) {
			matches = append(matches, *sub)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	allowed := matches[:0]
	for _, sub := range matches {
		ok, e := eventSourceAllowed(ctx, tx, sub.OwnerTenantID, event.SourceRef)
		if e != nil {
			return e
		}
		if ok {
			allowed = append(allowed, sub)
		}
	}
	if len(allowed) == 0 {
		if targetID != "" {
			return fmt.Errorf("subscription no longer available or authorized")
		}
		return tx.Commit()
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	dedupe := event.ID
	if event.Source != "agentmux" && event.SourceEventID != "" {
		b, _ := json.Marshal([]string{event.Source, event.Attributes["app_id"], event.Type, event.SourceEventID})
		dedupe = string(b)
	}
	now := time.Now().UnixMilli()
	result, err := tx.ExecContext(ctx, `INSERT INTO relay_events(id,dedupe_key,source_ref,envelope,created_at) VALUES(?,?,?,?,?) ON CONFLICT DO NOTHING`, event.ID, dedupe, event.SourceRef, string(payload), now)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return tx.Commit()
	}
	for _, sub := range allowed {
		_, err = tx.ExecContext(ctx, `INSERT INTO event_deliveries(id,subscription_id,event_id,status,next_attempt_at,created_at,updated_at) VALUES(?,?,?,'pending',?,?,?)`, "del_"+uuid.NewString(), sub.ID, event.ID, now, now, now)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

const eventDeliveryColumns = `d.id,d.subscription_id,d.event_id,d.status,d.attempts,d.first_attempt_at,d.next_attempt_at,d.lease_token,d.last_error,d.created_at,d.updated_at,e.envelope`

func scanEventDelivery(row scanner) (*core.EventDelivery, error) {
	var d core.EventDelivery
	var first, next, created, updated int64
	var body string
	if err := row.Scan(&d.ID, &d.SubscriptionID, &d.EventID, &d.Status, &d.Attempts, &first, &next, &d.LeaseToken, &d.LastError, &created, &updated, &body); err != nil {
		return nil, err
	}
	d.NextAttemptAt = time.UnixMilli(next).UTC()
	d.CreatedAt = time.UnixMilli(created).UTC()
	d.UpdatedAt = time.UnixMilli(updated).UTC()
	if first != 0 {
		v := time.UnixMilli(first).UTC()
		d.FirstAttemptAt = &v
	}
	if err := json.Unmarshal([]byte(body), &d.Event); err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *Store) GetEventDelivery(ctx context.Context, id string) (*core.EventDelivery, error) {
	d, err := scanEventDelivery(s.db.QueryRowContext(ctx, `SELECT `+eventDeliveryColumns+` FROM event_deliveries d JOIN relay_events e ON e.id=d.event_id WHERE d.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,started_at,finished_at,http_status,error FROM event_delivery_attempts WHERE delivery_id=? ORDER BY started_at DESC,id DESC LIMIT 200`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	d.History = []core.EventDeliveryAttempt{}
	for rows.Next() {
		var a core.EventDeliveryAttempt
		var start, end int64
		if err := rows.Scan(&a.ID, &start, &end, &a.HTTPStatus, &a.Error); err != nil {
			return nil, err
		}
		a.DeliveryID = id
		a.StartedAt = time.UnixMilli(start).UTC()
		if end != 0 {
			v := time.UnixMilli(end).UTC()
			a.FinishedAt = &v
		}
		d.History = append(d.History, a)
	}
	return d, rows.Err()
}

func (s *Store) ListEventDeliveries(ctx context.Context, owner *string, subID, status string, limit, offset int) ([]core.EventDelivery, error) {
	q := `SELECT ` + eventDeliveryColumns + ` FROM event_deliveries d JOIN relay_events e ON e.id=d.event_id JOIN event_subscriptions s ON s.id=d.subscription_id WHERE 1=1`
	args := []any{}
	if owner != nil {
		q += ` AND s.owner_tenant_id=?`
		args = append(args, *owner)
	}
	if subID != "" {
		q += ` AND d.subscription_id=?`
		args = append(args, subID)
	}
	if status != "" {
		q += ` AND d.status=?`
		args = append(args, status)
	}
	q += ` ORDER BY d.created_at DESC,d.id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.EventDelivery{}
	for rows.Next() {
		d, e := scanEventDelivery(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// ClaimEventDelivery locks the subscription, not just the event: two processes
// cannot dispatch different events to the same subscriber concurrently.
func (s *Store) ClaimEventDelivery(ctx context.Context, now time.Time) (*core.EventDelivery, error) {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	q := `SELECT s.id FROM event_subscriptions s WHERE s.deleted=0 AND s.paused=0 AND s.lease_until<=? AND EXISTS
 (SELECT 1 FROM event_deliveries d WHERE d.subscription_id=s.id AND d.status IN ('pending','retry','blocked','delivering') AND d.next_attempt_at<=? AND d.lease_until<=?) ORDER BY s.updated_at,s.id LIMIT 1`
	if s.dialect == DialectPostgres {
		q += ` FOR UPDATE OF s SKIP LOCKED`
	}
	var subID string
	err = tx.QueryRowContext(ctx, q, now.UnixMilli(), now.UnixMilli(), now.UnixMilli()).Scan(&subID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d, err := scanEventDelivery(tx.QueryRowContext(ctx, `SELECT `+eventDeliveryColumns+` FROM event_deliveries d JOIN relay_events e ON e.id=d.event_id WHERE d.subscription_id=? AND d.status IN ('pending','retry','blocked','delivering') AND d.next_attempt_at<=? AND d.lease_until<=? ORDER BY d.next_attempt_at,d.created_at,d.id LIMIT 1`, subID, now.UnixMilli(), now.UnixMilli()))
	if err != nil {
		return nil, err
	}
	token := uuid.NewString()
	lease := now.Add(30 * time.Second).UnixMilli()
	if _, err = tx.ExecContext(ctx, `UPDATE event_subscriptions SET lease_token=?,lease_until=?,updated_at=? WHERE id=?`, token, lease, now.UnixMilli(), subID); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE event_deliveries SET status='delivering',lease_token=?,lease_until=? WHERE id=?`, token, lease, d.ID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	d.LeaseToken = token
	return d, nil
}

func (s *Store) BeginEventAttempt(ctx context.Context, d *core.EventDelivery, now time.Time) (string, error) {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE event_deliveries SET attempts=attempts+1,first_attempt_at=CASE WHEN first_attempt_at=0 THEN ? ELSE first_attempt_at END,updated_at=? WHERE id=? AND lease_token=? AND status='delivering'`, now.UnixMilli(), now.UnixMilli(), d.ID, d.LeaseToken)
	if err != nil {
		return "", err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return "", fmt.Errorf("delivery lease lost")
	}
	id := "try_" + uuid.NewString()
	if _, err = tx.ExecContext(ctx, `INSERT INTO event_delivery_attempts(id,delivery_id,started_at) VALUES(?,?,?)`, id, d.ID, now.UnixMilli()); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	d.Attempts++
	if d.FirstAttemptAt == nil {
		d.FirstAttemptAt = &now
	}
	return id, nil
}

func (s *Store) FinishEventDelivery(ctx context.Context, d *core.EventDelivery, attemptID, status string, httpStatus int, message string, next time.Time) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	result, err := tx.ExecContext(ctx, `UPDATE event_deliveries SET status=?,last_error=?,next_attempt_at=?,lease_token='',lease_until=0,updated_at=? WHERE id=? AND lease_token=? AND status='delivering'`, status, message, next.UnixMilli(), now, d.ID, d.LeaseToken)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return fmt.Errorf("delivery lease lost")
	}
	if attemptID != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE event_delivery_attempts SET finished_at=?,http_status=?,error=? WHERE id=?`, now, httpStatus, message, attemptID); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE event_subscriptions SET lease_token='',lease_until=0 WHERE id=? AND lease_token=?`, d.SubscriptionID, d.LeaseToken); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RetryEventDelivery(ctx context.Context, id string) error {
	now := time.Now().UnixMilli()
	result, err := s.writer.ExecContext(ctx, `UPDATE event_deliveries SET status='pending',first_attempt_at=0,next_attempt_at=?,last_error='',updated_at=? WHERE id=? AND status IN ('dead','retry','blocked') AND lease_until<=?`, now, now, id, now)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return fmt.Errorf("delivery is not retryable")
	}
	return nil
}

func (s *Store) EventSubscriptionStats(ctx context.Context, sub *core.EventSubscription) error {
	rows, err := s.db.QueryContext(ctx, `SELECT status,COUNT(*),MAX(updated_at) FROM event_deliveries WHERE subscription_id=? GROUP BY status`, sub.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var status string
		var count int
		var last int64
		if err := rows.Scan(&status, &count, &last); err != nil {
			rows.Close()
			return err
		}
		switch status {
		case "pending", "retry", "blocked", "delivering":
			sub.Pending += count
		case "dead":
			sub.Dead += count
		case "sent":
			v := time.UnixMilli(last).UTC()
			sub.LastSuccessAt = &v
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	err = s.db.QueryRowContext(ctx, `SELECT last_error FROM event_deliveries WHERE subscription_id=? AND last_error<>'' ORDER BY updated_at DESC LIMIT 1`, sub.ID).Scan(&sub.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

func (s *Store) PruneEventDeliveries(ctx context.Context, now time.Time) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	where := `(status='sent' AND updated_at<?) OR (status IN ('dead','cancelled') AND updated_at<?)`
	args := []any{now.Add(-7 * 24 * time.Hour).UnixMilli(), now.Add(-30 * 24 * time.Hour).UnixMilli()}
	if _, err = tx.ExecContext(ctx, `DELETE FROM event_delivery_attempts WHERE delivery_id IN (SELECT id FROM event_deliveries WHERE `+where+`)`, args...); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM event_deliveries WHERE `+where, args...); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM relay_events WHERE NOT EXISTS(SELECT 1 FROM event_deliveries d WHERE d.event_id=relay_events.id)`); err != nil {
		return err
	}
	return tx.Commit()
}
