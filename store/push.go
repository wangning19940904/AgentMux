package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// Push notifications: the hub's VAPID key pair (one row) and the browsers
// that subscribed to it.

const pushSchema = `
CREATE TABLE IF NOT EXISTS push_vapid_keys (
	id INTEGER PRIMARY KEY,
	public_key TEXT NOT NULL,
	private_key TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS push_subscriptions (
	id TEXT PRIMARY KEY,
	subscriber TEXT NOT NULL,
	endpoint TEXT NOT NULL,
	p256dh TEXT NOT NULL,
	auth TEXT NOT NULL,
	kinds TEXT NOT NULL,
	label TEXT,
	lang TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	last_success_at TEXT,
	last_error_at TEXT,
	last_error TEXT
);
CREATE INDEX IF NOT EXISTS idx_push_subscriptions_subscriber ON push_subscriptions(subscriber);
`

// PushVAPIDKeys returns the hub's key pair, or nil before one was made.
func (s *Store) PushVAPIDKeys(ctx context.Context) (*core.PushVAPIDKeys, error) {
	var keys core.PushVAPIDKeys
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT public_key,private_key,created_at FROM push_vapid_keys WHERE id=1`).
		Scan(&keys.PublicKey, &keys.PrivateKey, &created)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	keys.CreatedAt = parseControlTime(created)
	return &keys, nil
}

// EnsurePushVAPIDKeys stores keys unless a pair exists, and returns the pair
// that is kept. Two processes starting at once agree on one pair.
func (s *Store) EnsurePushVAPIDKeys(ctx context.Context, keys core.PushVAPIDKeys) (*core.PushVAPIDKeys, error) {
	if keys.CreatedAt.IsZero() {
		keys.CreatedAt = time.Now().UTC()
	}
	if _, err := s.writer.ExecContext(ctx, `INSERT INTO push_vapid_keys(id,public_key,private_key,created_at)
		VALUES (1,?,?,?) ON CONFLICT(id) DO NOTHING`,
		keys.PublicKey, keys.PrivateKey, formatControlTime(keys.CreatedAt)); err != nil {
		return nil, err
	}
	return s.PushVAPIDKeys(ctx)
}

// UpsertPushSubscription saves a subscription by ID. A browser that subscribes
// again (new keys, other kinds, another subscriber) replaces its old record.
func (s *Store) UpsertPushSubscription(ctx context.Context, sub core.PushSubscription) error {
	kinds, err := json.Marshal(sub.Kinds)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if sub.CreatedAt.IsZero() {
		sub.CreatedAt = now
	}
	if sub.UpdatedAt.IsZero() {
		sub.UpdatedAt = now
	}
	_, err = s.writer.ExecContext(ctx, `INSERT INTO push_subscriptions
		(id,subscriber,endpoint,p256dh,auth,kinds,label,lang,created_at,updated_at,last_success_at,last_error_at,last_error)
		VALUES (?,?,?,?,?,?,?,?,?,?,'','','')
		ON CONFLICT(id) DO UPDATE SET subscriber=excluded.subscriber,endpoint=excluded.endpoint,
			p256dh=excluded.p256dh,auth=excluded.auth,kinds=excluded.kinds,label=excluded.label,
			lang=excluded.lang,updated_at=excluded.updated_at,last_error_at='',last_error=''`,
		sub.ID, sub.Subscriber, sub.Endpoint, sub.P256dh, sub.Auth, string(kinds), sub.Label, sub.Lang,
		formatControlTime(sub.CreatedAt), formatControlTime(sub.UpdatedAt))
	return err
}

const pushSubscriptionColumns = `id,subscriber,endpoint,p256dh,auth,kinds,label,lang,created_at,updated_at,last_success_at,last_error_at,last_error`

// ListPushSubscriptions returns one subscriber's subscriptions, or every
// subscription when subscriber is empty, oldest first.
func (s *Store) ListPushSubscriptions(ctx context.Context, subscriber string) ([]core.PushSubscription, error) {
	query := `SELECT ` + pushSubscriptionColumns + ` FROM push_subscriptions`
	args := []any{}
	if subscriber != "" {
		query += ` WHERE subscriber=?`
		args = append(args, subscriber)
	}
	rows, err := s.db.QueryContext(ctx, query+` ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.PushSubscription{}
	for rows.Next() {
		sub, err := scanPushSubscription(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sub)
	}
	return out, rows.Err()
}

// GetPushSubscription returns one subscription, or nil.
func (s *Store) GetPushSubscription(ctx context.Context, id string) (*core.PushSubscription, error) {
	sub, err := scanPushSubscription(s.db.QueryRowContext(ctx, `SELECT `+pushSubscriptionColumns+` FROM push_subscriptions WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return sub, err
}

// DeletePushSubscription removes a subscription. A non-empty subscriber must
// own it; the push service passes "" to drop one its push service says is gone.
func (s *Store) DeletePushSubscription(ctx context.Context, id, subscriber string) (bool, error) {
	query, args := `DELETE FROM push_subscriptions WHERE id=?`, []any{id}
	if subscriber != "" {
		query += ` AND subscriber=?`
		args = append(args, subscriber)
	}
	result, err := s.writer.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

// RecordPushDelivery notes the outcome of the latest send to a subscription.
func (s *Store) RecordPushDelivery(ctx context.Context, id string, at time.Time, deliveryErr string) error {
	if deliveryErr == "" {
		_, err := s.writer.ExecContext(ctx, `UPDATE push_subscriptions SET last_success_at=?,last_error='' WHERE id=?`,
			formatControlTime(at), id)
		return err
	}
	if len(deliveryErr) > 300 {
		deliveryErr = deliveryErr[:300]
	}
	_, err := s.writer.ExecContext(ctx, `UPDATE push_subscriptions SET last_error_at=?,last_error=? WHERE id=?`,
		formatControlTime(at), deliveryErr, id)
	return err
}

func scanPushSubscription(sc scanner) (*core.PushSubscription, error) {
	var sub core.PushSubscription
	var kinds string
	var label, lang, created, updated, success, errorAt, lastError sql.NullString
	if err := sc.Scan(&sub.ID, &sub.Subscriber, &sub.Endpoint, &sub.P256dh, &sub.Auth, &kinds, &label, &lang,
		&created, &updated, &success, &errorAt, &lastError); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(kinds), &sub.Kinds)
	if sub.Kinds == nil {
		sub.Kinds = []string{}
	}
	sub.Label = label.String
	sub.Lang = lang.String
	sub.CreatedAt = parseControlTime(created.String)
	sub.UpdatedAt = parseControlTime(updated.String)
	sub.LastSuccessAt = parseControlTime(success.String)
	sub.LastErrorAt = parseControlTime(errorAt.String)
	sub.LastError = lastError.String
	return &sub, nil
}
