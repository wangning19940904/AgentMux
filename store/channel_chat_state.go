package store

import (
	"context"
	"database/sql"
)

func (s *Store) GetChannelChatState(ctx context.Context, channelID, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM channel_chat_state WHERE channel_id=? AND state_key=?`, channelID, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}
func (s *Store) SetChannelChatState(ctx context.Context, channelID, key, value string) error {
	_, err := s.writer.ExecContext(ctx, `INSERT INTO channel_chat_state(channel_id,state_key,value) VALUES(?,?,?) ON CONFLICT(channel_id,state_key) DO UPDATE SET value=excluded.value`, channelID, key, value)
	return err
}

func (s *Store) FindTopicConversationKey(ctx context.Context, scope, root, thread string) (string, error) {
	var key string
	err := s.db.QueryRowContext(ctx, `SELECT conversation_key FROM conversations WHERE scope=? AND conversation_key IN (?,?) AND (ended_at IS NULL OR ended_at='') ORDER BY created_at ASC LIMIT 1`, scope, root, thread).Scan(&key)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return key, err
}
