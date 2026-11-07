package store

import (
	"context"
	"encoding/json"

	"github.com/wangning19940904/AgentMux/core"
)

// GetFeishuUserGrant returns the channel's user authorization, or nil.
func (s *Store) GetFeishuUserGrant(ctx context.Context, channelID string) (*core.FeishuUserGrant, error) {
	return getSuperAgentRow[core.FeishuUserGrant](ctx, s, `SELECT data FROM feishu_user_grants WHERE channel_id=?`, channelID)
}

// PutFeishuUserGrant replaces the channel's user authorization.
func (s *Store) PutFeishuUserGrant(ctx context.Context, grant core.FeishuUserGrant) error {
	data, err := json.Marshal(grant)
	if err != nil {
		return err
	}
	_, err = s.writer.ExecContext(ctx, `INSERT INTO feishu_user_grants (channel_id,data,updated_at) VALUES (?,?,?)
		ON CONFLICT(channel_id) DO UPDATE SET data=excluded.data,updated_at=excluded.updated_at`,
		grant.ChannelID, string(data), formatControlTime(grant.UpdatedAt))
	return err
}

func (s *Store) DeleteFeishuUserGrant(ctx context.Context, channelID string) error {
	_, err := s.writer.ExecContext(ctx, `DELETE FROM feishu_user_grants WHERE channel_id=?`, channelID)
	return err
}
