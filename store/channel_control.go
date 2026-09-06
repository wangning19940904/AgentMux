package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

var _ core.ChannelControlStore = (*Store)(nil)
var _ core.ChannelFeedbackStore = (*Store)(nil)

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
		 native_thread_id,turn_id,status,error,delivery_key,delivery_status,delivery_attempts,delivery_error,delivered_at,feedback_nonce,source_message_id,control_json,prompt,created_at,started_at,finished_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		task.ID, task.ChannelID, task.ConversationID, task.ConversationKey, task.ChatID,
		task.MessageID, task.ChatType, task.RootID, task.ThreadID, task.UserID, task.ControllerID,
		task.NativeThreadID, task.TurnID, task.Status,
		task.Error, task.DeliveryKey, task.DeliveryStatus, task.DeliveryAttempts, task.DeliveryError,
		formatControlTime(task.DeliveredAt), task.FeedbackNonce, task.SourceMessageID, taskControlJSON(task), task.Prompt, formatControlTime(task.CreatedAt), formatControlTime(task.StartedAt),
		formatControlTime(task.FinishedAt), formatControlTime(task.UpdatedAt))
	return err
}

func (s *Store) UpdateChannelTask(ctx context.Context, task core.ChannelTask) error {
	task.UpdatedAt = time.Now().UTC()
	_, err := s.writer.ExecContext(ctx, `UPDATE channel_tasks SET
		conversation_id=?,controller_id=?,native_thread_id=?,turn_id=?,status=?,error=?,prompt=?,
		delivery_key=?,delivery_status=?,delivery_attempts=?,delivery_error=?,delivered_at=?,feedback_nonce=?,control_json=?,
		started_at=?,finished_at=?,updated_at=? WHERE id=?`,
		task.ConversationID, task.ControllerID, task.NativeThreadID, task.TurnID, task.Status, task.Error,
		task.Prompt, task.DeliveryKey, task.DeliveryStatus, task.DeliveryAttempts, task.DeliveryError,
		formatControlTime(task.DeliveredAt), task.FeedbackNonce, taskControlJSON(task), formatControlTime(task.StartedAt), formatControlTime(task.FinishedAt),
		formatControlTime(task.UpdatedAt), task.ID)
	return err
}

func (s *Store) GetChannelTask(ctx context.Context, id string) (*core.ChannelTask, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,channel_id,conversation_id,conversation_key,chat_id,user_id,controller_id,
		message_id,chat_type,root_id,thread_id,native_thread_id,turn_id,status,error,delivery_key,delivery_status,
		delivery_attempts,delivery_error,delivered_at,feedback_nonce,source_message_id,control_json,prompt,created_at,started_at,finished_at,updated_at
		FROM channel_tasks WHERE id=?`, id)
	task, err := scanChannelTask(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return task, err
}

func (s *Store) ListChannelTasks(ctx context.Context, channelID, conversationID string, activeOnly bool) ([]core.ChannelTask, error) {
	q := `SELECT id,channel_id,conversation_id,conversation_key,chat_id,user_id,controller_id,
		message_id,chat_type,root_id,thread_id,
		native_thread_id,turn_id,status,error,delivery_key,delivery_status,delivery_attempts,delivery_error,delivered_at,feedback_nonce,source_message_id,control_json,prompt,created_at,started_at,finished_at,updated_at
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

// ListLatestChannelTasks returns only the newest task for each conversation.
// The session list needs status metadata, not every historical prompt and
// delivery record, so keeping this query compact avoids work proportional to
// the full task history on every console refresh.
func (s *Store) ListLatestChannelTasks(ctx context.Context) ([]core.ChannelTask, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,channel_id,conversation_id,conversation_key,status,created_at
		FROM (
			SELECT id,channel_id,conversation_id,conversation_key,status,created_at,
				ROW_NUMBER() OVER (
					PARTITION BY channel_id, CASE
						WHEN conversation_id IS NOT NULL AND conversation_id<>'' THEN 'id:' || conversation_id
						ELSE 'key:' || conversation_key
					END
					ORDER BY created_at DESC,id DESC
				) AS task_rank
			FROM channel_tasks
		) latest
		WHERE task_rank=1
		ORDER BY created_at DESC,id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.ChannelTask
	for rows.Next() {
		var task core.ChannelTask
		var conversationID, createdAt sql.NullString
		var status string
		if err := rows.Scan(&task.ID, &task.ChannelID, &conversationID, &task.ConversationKey, &status, &createdAt); err != nil {
			return nil, err
		}
		task.ConversationID = conversationID.String
		task.Status = core.ChannelTaskStatus(status)
		task.CreatedAt = parseControlTime(createdAt.String)
		out = append(out, task)
	}
	return out, rows.Err()
}

// RecoverChannelTasks returns queued tasks and atomically marks tasks that had
// already started as interrupted. Started prompts are never replayed.
func (s *Store) RecoverChannelTasks(ctx context.Context, channelID string) ([]core.ChannelTask, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.writer.ExecContext(ctx, `UPDATE channel_tasks SET status='steer_unknown',error='追加结果待确认：服务在提交期间重启',prompt='',updated_at=? WHERE channel_id=? AND status='steering'`, now, channelID); err != nil {
		return nil, err
	}
	if _, err := s.writer.ExecContext(ctx, `UPDATE channel_interactions
		SET status='expired', resolved_at=?, resolved_by='system-restart'
		WHERE channel_id=? AND status='pending' AND task_id IN (
			SELECT id FROM channel_tasks WHERE channel_id=? AND status IN ('running','waiting_input')
		)`, now, channelID, channelID); err != nil {
		return nil, err
	}
	if _, err := s.writer.ExecContext(ctx, `UPDATE channel_tasks
		SET status='interrupted', error='AgentMux restarted while task was active',
		    finished_at=?, updated_at=?, prompt=''
		WHERE channel_id=? AND status IN ('running','waiting_input')`, now, now, channelID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,channel_id,conversation_id,conversation_key,
		chat_id,user_id,controller_id,message_id,chat_type,root_id,thread_id,native_thread_id,turn_id,status,error,
		delivery_key,delivery_status,delivery_attempts,delivery_error,delivered_at,feedback_nonce,source_message_id,control_json,prompt,
		created_at,started_at,finished_at,updated_at FROM channel_tasks
		WHERE channel_id=? AND status='queued' ORDER BY created_at ASC,id ASC`, channelID)
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
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, rows.Err()
}

func scanChannelTask(sc scanner) (*core.ChannelTask, error) {
	var task core.ChannelTask
	var conversationID, chatID, userID, controllerID, messageID, chatType, rootID, threadID, nativeThreadID, turnID sql.NullString
	var status string
	var errText, deliveryKey, deliveryStatus, deliveryError, deliveredAt, feedbackNonce, sourceMessageID, controlJSON, prompt, createdAt, startedAt, finishedAt, updatedAt sql.NullString
	var deliveryAttempts int
	if err := sc.Scan(&task.ID, &task.ChannelID, &conversationID, &task.ConversationKey,
		&chatID, &userID, &controllerID, &messageID, &chatType, &rootID, &threadID,
		&nativeThreadID, &turnID, &status, &errText, &deliveryKey, &deliveryStatus, &deliveryAttempts, &deliveryError, &deliveredAt, &feedbackNonce, &sourceMessageID, &controlJSON,
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
	task.DeliveryKey = deliveryKey.String
	task.DeliveryStatus = deliveryStatus.String
	task.DeliveryAttempts = deliveryAttempts
	task.DeliveryError = deliveryError.String
	task.DeliveredAt = parseControlTime(deliveredAt.String)
	task.FeedbackNonce = feedbackNonce.String
	task.SourceMessageID = sourceMessageID.String
	decodeTaskControlJSON(&task, controlJSON.String)
	task.Prompt = prompt.String
	task.CreatedAt = parseControlTime(createdAt.String)
	task.StartedAt = parseControlTime(startedAt.String)
	task.FinishedAt = parseControlTime(finishedAt.String)
	task.UpdatedAt = parseControlTime(updatedAt.String)
	return &task, nil
}

func (s *Store) SubmitChannelFeedback(ctx context.Context, feedback core.ChannelFeedback, nonce string) (bool, error) {
	now := time.Now().UTC()
	if feedback.ID == "" {
		feedback.ID = core.NewChannelControlID("feedback")
	}
	if feedback.CreatedAt.IsZero() {
		feedback.CreatedAt = now
	}
	feedback.UpdatedAt = now
	result, err := s.writer.ExecContext(ctx, `INSERT INTO channel_feedback
		(id,task_id,channel_id,conversation_id,user_id,semantic,reason,comment,created_at,updated_at)
		SELECT ?,t.id,t.channel_id,t.conversation_id,?,?,?,?,?,?
		FROM channel_tasks t
		WHERE t.id=? AND t.feedback_nonce=? AND t.user_id=?
		  AND t.status='succeeded' AND t.delivery_status='sent'
		ON CONFLICT(task_id,user_id) DO UPDATE SET
			semantic=excluded.semantic,reason=excluded.reason,comment=excluded.comment,updated_at=excluded.updated_at`,
		feedback.ID, feedback.UserID, feedback.Semantic, feedback.Reason, feedback.Comment,
		formatControlTime(feedback.CreatedAt), formatControlTime(feedback.UpdatedAt),
		feedback.TaskID, nonce, feedback.UserID)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

func (s *Store) ListChannelFeedback(ctx context.Context, channelID, taskID string, limit int) ([]core.ChannelFeedback, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	query := `SELECT id,task_id,channel_id,conversation_id,user_id,semantic,reason,comment,created_at,updated_at
		FROM channel_feedback WHERE 1=1`
	args := []any{}
	if channelID != "" {
		query += ` AND channel_id=?`
		args = append(args, channelID)
	}
	if taskID != "" {
		query += ` AND task_id=?`
		args = append(args, taskID)
	}
	query += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.ChannelFeedback
	for rows.Next() {
		var feedback core.ChannelFeedback
		var conversationID, reason, comment, createdAt, updatedAt sql.NullString
		if err := rows.Scan(&feedback.ID, &feedback.TaskID, &feedback.ChannelID, &conversationID,
			&feedback.UserID, &feedback.Semantic, &reason, &comment, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		feedback.ConversationID = conversationID.String
		feedback.Reason = reason.String
		feedback.Comment = comment.String
		feedback.CreatedAt = parseControlTime(createdAt.String)
		feedback.UpdatedAt = parseControlTime(updatedAt.String)
		out = append(out, feedback)
	}
	return out, rows.Err()
}

func (s *Store) UpdateChannelFeedbackDetail(ctx context.Context, id, reason, comment string) (bool, error) {
	result, err := s.writer.ExecContext(ctx, `UPDATE channel_feedback
		SET reason=?,comment=?,updated_at=? WHERE id=?`, reason, comment, formatControlTime(time.Now().UTC()), id)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
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

func taskControlJSON(task core.ChannelTask) string {
	b, _ := json.Marshal(map[string]string{"card_id": task.ControlCardID, "nonce": task.ControlNonce, "target_task_id": task.TargetTaskID, "chat_mode": task.ChatMode, "reply_in_thread": fmt.Sprint(task.ReplyInThread)})
	return string(b)
}
func decodeTaskControlJSON(task *core.ChannelTask, raw string) {
	var fields map[string]string
	if json.Unmarshal([]byte(raw), &fields) != nil {
		return
	}
	task.ChatMode = fields["chat_mode"]
	task.ReplyInThread = fields["reply_in_thread"] == "true"
	task.ControlCardID, task.ControlNonce, task.TargetTaskID = fields["card_id"], fields["nonce"], fields["target_task_id"]
}

func (s *Store) HasChannelSourceTask(ctx context.Context, channelID, messageID string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM channel_tasks WHERE channel_id=? AND (source_message_id=? OR message_id=?))`, channelID, messageID, messageID).Scan(&exists)
	return exists, err
}
