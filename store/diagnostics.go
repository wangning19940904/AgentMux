package store

import (
	"context"

	"github.com/wangning19940904/AgentMux/core"
)

// ListRecentChannelTasks returns the newest channel tasks (turns), newest
// first, at most limit of them. The prompt is left empty unless withPrompt is
// set: a diagnostics bundle summarizes turns without their message content.
func (s *Store) ListRecentChannelTasks(ctx context.Context, limit int, withPrompt bool) ([]core.ChannelTask, error) {
	if limit <= 0 {
		limit = 100
	}
	prompt := `''`
	if withPrompt {
		prompt = `prompt`
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,channel_id,conversation_id,conversation_key,chat_id,user_id,controller_id,
		message_id,chat_type,root_id,thread_id,
		native_thread_id,turn_id,status,error,delivery_key,delivery_status,delivery_attempts,delivery_error,delivered_at,feedback_nonce,source_message_id,control_json,`+prompt+`,created_at,started_at,finished_at,updated_at
		FROM channel_tasks ORDER BY created_at DESC,id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.ChannelTask
	for rows.Next() {
		task, err := scanChannelTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *task)
	}
	return out, rows.Err()
}
