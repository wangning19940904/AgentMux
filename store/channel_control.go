package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/agentnexus/agentnexus/core"
)

var _ core.ChannelControlStore = (*Store)(nil)

func (s *Store) CreateChannelTask(ctx context.Context, task core.ChannelTask) error {
	now := time.Now().UTC()
	if task.CreatedAt.IsZero() {
		task.CreatedAt = now
	}
	if task.UpdatedAt.IsZero() {
		task.UpdatedAt = now
	}
	_, err := s.writer.ExecContext(ctx, `INSERT INTO channel_tasks
		(id,channel_id,conversation_id,conversation_key,chat_id,message_id,chat_type,root_id,thread_id,user_id,controller_id,
		 native_thread_id,turn_id,status,error,prompt,created_at,started_at,finished_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		task.ID, task.ChannelID, task.ConversationID, task.ConversationKey, task.ChatID,
		task.MessageID, task.ChatType, task.RootID, task.ThreadID, task.UserID, task.ControllerID,
		task.NativeThreadID, task.TurnID, task.Status,
		task.Error, task.Prompt, formatControlTime(task.CreatedAt), formatControlTime(task.StartedAt),
		formatControlTime(task.FinishedAt), formatControlTime(task.UpdatedAt))
	return err
}

func (s *Store) UpdateChannelTask(ctx context.Context, task core.ChannelTask) error {
	task.UpdatedAt = time.Now().UTC()
	_, err := s.writer.ExecContext(ctx, `UPDATE channel_tasks SET
		conversation_id=?,controller_id=?,native_thread_id=?,turn_id=?,status=?,error=?,prompt=?,
		started_at=?,finished_at=?,updated_at=? WHERE id=?`,
		task.ConversationID, task.ControllerID, task.NativeThreadID, task.TurnID, task.Status, task.Error,
		task.Prompt, formatControlTime(task.StartedAt), formatControlTime(task.FinishedAt),
		formatControlTime(task.UpdatedAt), task.ID)
	return err
}

func (s *Store) ListChannelTasks(ctx context.Context, channelID, conversationID string, activeOnly bool) ([]core.ChannelTask, error) {
	q := `SELECT id,channel_id,conversation_id,conversation_key,chat_id,user_id,controller_id,
		message_id,chat_type,root_id,thread_id,
		native_thread_id,turn_id,status,error,prompt,created_at,started_at,finished_at,updated_at
		FROM channel_tasks WHERE 1=1`
	args := []any{}
	if channelID != "" {
		q += ` AND channel_id=?`
		args = append(args, channelID)
	}
	if conversationID != "" {
		q += ` AND conversation_id=?`
		args = append(args, conversationID)
	}
	if activeOnly {
		q += ` AND status IN ('queued','running','waiting_input')`
	}
	q += ` ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
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

// RecoverChannelTasks returns queued tasks and atomically marks tasks that had
// already started as interrupted. Started prompts are never replayed.
func (s *Store) RecoverChannelTasks(ctx context.Context, channelID string) ([]core.ChannelTask, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.writer.ExecContext(ctx, `UPDATE channel_interactions
		SET status='expired', resolved_at=?, resolved_by='system-restart'
		WHERE channel_id=? AND status='pending' AND task_id IN (
			SELECT id FROM channel_tasks WHERE channel_id=? AND status IN ('running','waiting_input')
		)`, now, channelID, channelID); err != nil {
		return nil, err
	}
	if _, err := s.writer.ExecContext(ctx, `UPDATE channel_tasks
		SET status='interrupted', error='AgentNexus restarted while task was active',
		    finished_at=?, updated_at=?, prompt=''
		WHERE channel_id=? AND status IN ('running','waiting_input')`, now, now, channelID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,channel_id,conversation_id,conversation_key,
		chat_id,user_id,controller_id,message_id,chat_type,root_id,thread_id,native_thread_id,turn_id,status,error,prompt,
		created_at,started_at,finished_at,updated_at FROM channel_tasks
		WHERE channel_id=? AND status='queued' ORDER BY created_at ASC`, channelID)
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

func scanChannelTask(sc scanner) (*core.ChannelTask, error) {
	var task core.ChannelTask
	var conversationID, chatID, userID, controllerID, messageID, chatType, rootID, threadID, nativeThreadID, turnID sql.NullString
	var status string
	var errText, prompt, createdAt, startedAt, finishedAt, updatedAt sql.NullString
	if err := sc.Scan(&task.ID, &task.ChannelID, &conversationID, &task.ConversationKey,
		&chatID, &userID, &controllerID, &messageID, &chatType, &rootID, &threadID,
		&nativeThreadID, &turnID, &status, &errText,
		&prompt, &createdAt, &startedAt, &finishedAt, &updatedAt); err != nil {
		return nil, err
	}
	task.ConversationID = conversationID.String
	task.ChatID = chatID.String
	task.UserID = userID.String
	task.ControllerID = controllerID.String
	task.MessageID = messageID.String
	task.ChatType = chatType.String
	task.RootID = rootID.String
	task.ThreadID = threadID.String
	task.NativeThreadID = nativeThreadID.String
	task.TurnID = turnID.String
	task.Status = core.ChannelTaskStatus(status)
	task.Error = errText.String
	task.Prompt = prompt.String
	task.CreatedAt = parseControlTime(createdAt.String)
	task.StartedAt = parseControlTime(startedAt.String)
	task.FinishedAt = parseControlTime(finishedAt.String)
	task.UpdatedAt = parseControlTime(updatedAt.String)
	return &task, nil
}

func (s *Store) CreateChannelInteraction(ctx context.Context, interaction core.ChannelInteraction) error {
	request, err := json.Marshal(interaction.Request)
	if err != nil {
		return err
	}
	_, err = s.writer.ExecContext(ctx, `INSERT INTO channel_interactions
		(id,task_id,channel_id,conversation_id,conversation_key,controller_id,nonce,message_id,
		 status,request,created_at,expires_at,resolved_at,resolved_by)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		interaction.ID, interaction.TaskID, interaction.ChannelID, interaction.ConversationID,
		interaction.ConversationKey, interaction.ControllerID, interaction.Nonce,
		interaction.MessageID, interaction.Status, string(request), formatControlTime(interaction.CreatedAt),
		formatControlTime(interaction.ExpiresAt), formatControlTime(interaction.ResolvedAt),
		interaction.ResolvedBy)
	return err
}

func (s *Store) UpdateChannelInteractionMessage(ctx context.Context, id, messageID string) error {
	_, err := s.writer.ExecContext(ctx, `UPDATE channel_interactions SET message_id=? WHERE id=? AND status='pending'`,
		messageID, id)
	return err
}

func (s *Store) ResolveChannelInteraction(ctx context.Context, id, nonce, actor string, status core.ChannelInteractionStatus) (bool, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.writer.ExecContext(ctx, `UPDATE channel_interactions
		SET status=?,resolved_at=?,resolved_by=? WHERE id=? AND nonce=? AND status='pending'`,
		status, now, actor, id, nonce)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (s *Store) GetChannelInteraction(ctx context.Context, id string) (*core.ChannelInteraction, error) {
	row := s.db.QueryRowContext(ctx, channelInteractionSelect+` WHERE id=?`, id)
	interaction, err := scanChannelInteraction(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return interaction, err
}

func (s *Store) ListChannelInteractions(ctx context.Context, channelID, conversationID string, pendingOnly bool) ([]core.ChannelInteraction, error) {
	q := channelInteractionSelect + ` WHERE 1=1`
	args := []any{}
	if channelID != "" {
		q += ` AND channel_id=?`
		args = append(args, channelID)
	}
	if conversationID != "" {
		q += ` AND conversation_id=?`
		args = append(args, conversationID)
	}
	if pendingOnly {
		q += ` AND status='pending'`
	}
	q += ` ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.ChannelInteraction
	for rows.Next() {
		interaction, err := scanChannelInteraction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *interaction)
	}
	return out, rows.Err()
}

const channelInteractionSelect = `SELECT id,task_id,channel_id,conversation_id,
	conversation_key,controller_id,nonce,message_id,status,request,created_at,expires_at,
	resolved_at,resolved_by FROM channel_interactions`

func scanChannelInteraction(sc scanner) (*core.ChannelInteraction, error) {
	var interaction core.ChannelInteraction
	var conversationID, controllerID, nonce, messageID, request, createdAt, expiresAt, resolvedAt, resolvedBy sql.NullString
	var status string
	if err := sc.Scan(&interaction.ID, &interaction.TaskID, &interaction.ChannelID,
		&conversationID, &interaction.ConversationKey, &controllerID, &nonce, &messageID, &status,
		&request, &createdAt, &expiresAt, &resolvedAt, &resolvedBy); err != nil {
		return nil, err
	}
	interaction.ConversationID = conversationID.String
	interaction.ControllerID = controllerID.String
	interaction.Nonce = nonce.String
	interaction.MessageID = messageID.String
	interaction.Status = core.ChannelInteractionStatus(status)
	_ = json.Unmarshal([]byte(request.String), &interaction.Request)
	interaction.CreatedAt = parseControlTime(createdAt.String)
	interaction.ExpiresAt = parseControlTime(expiresAt.String)
	interaction.ResolvedAt = parseControlTime(resolvedAt.String)
	interaction.ResolvedBy = resolvedBy.String
	return &interaction, nil
}

func formatControlTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parseControlTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}
