package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"time"

	"github.com/agentnexus/agentnexus/core"
)

// compile-time check: Store implements core.ConversationStore.
var _ core.ConversationStore = (*Store)(nil)

// GetOrCreateConversation returns the active conversation for (scope, chatID),
// creating one from seed when none exists.
func (s *Store) GetOrCreateConversation(ctx context.Context, seed core.Conversation) (*core.Conversation, bool, error) {
	existing, err := s.getActiveConversation(ctx, seed.Scope, seed.ChatID)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		return existing, false, nil
	}

	now := time.Now().UTC()
	conv := seed
	if conv.ID == "" {
		conv.ID = "conv-" + convRandHex(8)
	}
	conv.CreatedAt = now
	conv.UpdatedAt = now
	conv.LastActiveAt = now
	conv.EndedAt = time.Time{}

	_, err = s.db.ExecContext(ctx, `INSERT INTO conversations
		(id,scope,chat_id,chat_type,agent_id,work_dir,native_session_id,title,
		 message_count,created_at,updated_at,last_active_at,ended_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,'')`,
		conv.ID, conv.Scope, conv.ChatID, conv.ChatType, conv.AgentID, conv.WorkDir,
		conv.NativeSessionID, conv.Title, conv.MessageCount,
		conv.CreatedAt.Format(time.RFC3339Nano), conv.UpdatedAt.Format(time.RFC3339Nano),
		conv.LastActiveAt.Format(time.RFC3339Nano))
	if err != nil {
		// A concurrent insert may have won the race against the unique index;
		// fall back to reading the now-existing active row.
		if existing, gerr := s.getActiveConversation(ctx, seed.Scope, seed.ChatID); gerr == nil && existing != nil {
			return existing, false, nil
		}
		return nil, false, err
	}
	return &conv, true, nil
}

// UpdateConversationSession persists the native session id and work dir.
func (s *Store) UpdateConversationSession(ctx context.Context, id, nativeSessionID, workDir string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE conversations
		SET native_session_id=?, work_dir=?, updated_at=? WHERE id=?`,
		nativeSessionID, workDir, time.Now().UTC().Format(time.RFC3339Nano), id)
	return err
}

// TouchConversation bumps message count and last-active timestamp.
func (s *Store) TouchConversation(ctx context.Context, id string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `UPDATE conversations
		SET message_count=message_count+1, last_active_at=?, updated_at=? WHERE id=?`,
		now, now, id)
	return err
}

// EndConversation soft-deletes a conversation.
func (s *Store) EndConversation(ctx context.Context, id string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `UPDATE conversations
		SET ended_at=?, updated_at=? WHERE id=? AND (ended_at IS NULL OR ended_at='')`,
		now, now, id)
	return err
}

// ListConversations returns conversations most-recently-active first.
func (s *Store) ListConversations(ctx context.Context, scope string, includeEnded bool) ([]core.Conversation, error) {
	q := `SELECT id,scope,chat_id,chat_type,agent_id,work_dir,native_session_id,title,
		message_count,created_at,updated_at,last_active_at,ended_at FROM conversations`
	var conds []string
	var args []any
	if scope != "" {
		conds = append(conds, "scope=?")
		args = append(args, scope)
	}
	if !includeEnded {
		conds = append(conds, "(ended_at IS NULL OR ended_at='')")
	}
	for i, c := range conds {
		if i == 0 {
			q += " WHERE " + c
		} else {
			q += " AND " + c
		}
	}
	q += " ORDER BY last_active_at DESC, updated_at DESC"
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Conversation
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *Store) getActiveConversation(ctx context.Context, scope, chatID string) (*core.Conversation, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,scope,chat_id,chat_type,agent_id,work_dir,
		native_session_id,title,message_count,created_at,updated_at,last_active_at,ended_at
		FROM conversations WHERE scope=? AND chat_id=? AND (ended_at IS NULL OR ended_at='')
		ORDER BY last_active_at DESC LIMIT 1`, scope, chatID)
	c, err := scanConversation(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return c, err
}

func scanConversation(sc scanner) (*core.Conversation, error) {
	var c core.Conversation
	var chatType, agentID, workDir, nativeSessionID, title sql.NullString
	var created, updated, lastActive, ended sql.NullString
	if err := sc.Scan(&c.ID, &c.Scope, &c.ChatID, &chatType, &agentID, &workDir,
		&nativeSessionID, &title, &c.MessageCount, &created, &updated, &lastActive, &ended); err != nil {
		return nil, err
	}
	c.ChatType = chatType.String
	c.AgentID = agentID.String
	c.WorkDir = workDir.String
	c.NativeSessionID = nativeSessionID.String
	c.Title = title.String
	c.CreatedAt, _ = time.Parse(time.RFC3339Nano, created.String)
	c.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated.String)
	if lastActive.String != "" {
		c.LastActiveAt, _ = time.Parse(time.RFC3339Nano, lastActive.String)
	}
	if ended.String != "" {
		c.EndedAt, _ = time.Parse(time.RFC3339Nano, ended.String)
	}
	return &c, nil
}

func convRandHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
