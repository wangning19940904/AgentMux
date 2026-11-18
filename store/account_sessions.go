package store

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"time"
)

// AccountSessionInfo is one live account session as an administrator or its
// owner sees it: never the token, only what tells the session apart.
type AccountSessionInfo struct {
	ID         string
	AccountID  string
	Method     string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt *time.Time
	UserAgent  string
	RemoteAddr string
	// Current is the session the listing request itself used.
	Current bool
}

// ListAccountSessions returns the unexpired sessions, newest activity first.
// An empty accountID lists every account's. currentToken, when set, marks
// the session it belongs to.
func (s *Store) ListAccountSessions(ctx context.Context, accountID, currentToken string) ([]AccountSessionInfo, error) {
	query := `SELECT id,account_id,method,created_at,expires_at,last_seen_at,user_agent,remote_addr,token_hash
		FROM account_sessions WHERE expires_at>=?`
	args := []any{time.Now().UTC().Format(time.RFC3339Nano)}
	if accountID != "" {
		query += ` AND account_id=?`
		args = append(args, accountID)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	currentHash := ""
	if token := strings.TrimSpace(currentToken); token != "" {
		currentHash = HashSecret(token)
	}
	var out []AccountSessionInfo
	for rows.Next() {
		var info AccountSessionInfo
		var created, expires string
		var lastSeen, userAgent, remoteAddr sql.NullString
		var tokenHash string
		if err := rows.Scan(&info.ID, &info.AccountID, &info.Method, &created, &expires, &lastSeen,
			&userAgent, &remoteAddr, &tokenHash); err != nil {
			return nil, err
		}
		info.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		info.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expires)
		info.LastSeenAt = nullableTime(lastSeen)
		info.UserAgent, info.RemoteAddr = userAgent.String, remoteAddr.String
		info.Current = currentHash != "" && tokenHash == currentHash
		out = append(out, info)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.SortStableFunc(out, func(a, b AccountSessionInfo) int { return b.LastActive().Compare(a.LastActive()) })
	return out, nil
}

// LastActive is when the session was last used, or when it began.
func (info AccountSessionInfo) LastActive() time.Time {
	if info.LastSeenAt != nil {
		return *info.LastSeenAt
	}
	return info.CreatedAt
}

// DeleteAccountSessionByID ends one session by its record ID. A non-empty
// accountID limits it to that account's sessions. It reports whether a
// session was ended.
func (s *Store) DeleteAccountSessionByID(ctx context.Context, id, accountID string) (bool, error) {
	query, args := `DELETE FROM account_sessions WHERE id=?`, []any{id}
	if accountID != "" {
		query += ` AND account_id=?`
		args = append(args, accountID)
	}
	result, err := s.writer.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}
