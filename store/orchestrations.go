package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
)

func (s *Store) CreateOrchestration(ctx context.Context, orchestration core.Orchestration) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO orchestrations
		(id,name,status,max_concurrency,error,owner_tenant_id,created_at,started_at,finished_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, orchestration.ID, orchestration.Name, orchestration.Status,
		orchestration.MaxConcurrency, orchestration.Error, nullableOwner(orchestration.OwnerTenantID),
		formatControlTime(orchestration.CreatedAt),
		formatControlTime(orchestration.StartedAt), formatControlTime(orchestration.FinishedAt), formatControlTime(orchestration.UpdatedAt)); err != nil {
		return err
	}
	for _, task := range orchestration.Tasks {
		dependsOn, _ := json.Marshal(task.DependsOn)
		if _, err := tx.ExecContext(ctx, `INSERT INTO orchestration_tasks
			(orchestration_id,id,agent_id,project,input,depends_on,status,output,error,invocation_id,conversation_id,created_at,started_at,finished_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, orchestration.ID, task.ID, task.AgentID, "", task.Input,
			string(dependsOn), task.Status, task.Output, task.Error, task.InvocationID, task.ConversationID,
			formatControlTime(task.CreatedAt), formatControlTime(task.StartedAt), formatControlTime(task.FinishedAt), formatControlTime(task.UpdatedAt)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) UpdateOrchestration(ctx context.Context, orchestration core.Orchestration) error {
	_, err := s.writer.ExecContext(ctx, `UPDATE orchestrations SET
		name=?,status=?,max_concurrency=?,error=?,started_at=?,finished_at=?,updated_at=? WHERE id=?`,
		orchestration.Name, orchestration.Status, orchestration.MaxConcurrency, orchestration.Error,
		formatControlTime(orchestration.StartedAt), formatControlTime(orchestration.FinishedAt),
		formatControlTime(orchestration.UpdatedAt), orchestration.ID)
	return err
}

func (s *Store) UpdateOrchestrationTask(ctx context.Context, task core.OrchestrationTask) error {
	dependsOn, _ := json.Marshal(task.DependsOn)
	_, err := s.writer.ExecContext(ctx, `UPDATE orchestration_tasks SET
		agent_id=?,project='',input=?,depends_on=?,status=?,output=?,error=?,invocation_id=?,conversation_id=?,
		started_at=?,finished_at=?,updated_at=? WHERE orchestration_id=? AND id=?`,
		task.AgentID, task.Input, string(dependsOn), task.Status, task.Output, task.Error,
		task.InvocationID, task.ConversationID, formatControlTime(task.StartedAt), formatControlTime(task.FinishedAt),
		formatControlTime(task.UpdatedAt), task.OrchestrationID, task.ID)
	return err
}

const orchestrationColumns = `id,name,status,max_concurrency,error,owner_tenant_id,created_at,started_at,finished_at,updated_at`

func (s *Store) GetOrchestration(ctx context.Context, id string) (*core.Orchestration, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+orchestrationColumns+`
		FROM orchestrations WHERE id=?`, id)
	orchestration, err := scanOrchestration(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	tasks, err := s.listOrchestrationTasks(ctx, id)
	if err != nil {
		return nil, err
	}
	orchestration.Tasks = tasks
	return orchestration, nil
}

func (s *Store) ListOrchestrations(ctx context.Context, activeOnly bool, limit int) ([]core.Orchestration, error) {
	return s.listOrchestrations(ctx, activeOnly, limit, "")
}

// ListOrchestrationsForTenant returns the orchestrations one tenant owns.
func (s *Store) ListOrchestrationsForTenant(ctx context.Context, activeOnly bool, limit int, tenantID string) ([]core.Orchestration, error) {
	return s.listOrchestrations(ctx, activeOnly, limit, tenantID)
}

func (s *Store) listOrchestrations(ctx context.Context, activeOnly bool, limit int, tenantID string) ([]core.Orchestration, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `SELECT ` + orchestrationColumns + ` FROM orchestrations`
	args := []any{}
	conditions := []string{}
	if activeOnly {
		conditions = append(conditions, `status IN ('queued','running')`)
	}
	if tenantID != "" {
		conditions = append(conditions, `owner_tenant_id=?`)
		args = append(args, tenantID)
	}
	if len(conditions) > 0 {
		query += ` WHERE ` + strings.Join(conditions, " AND ")
	}
	query += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Orchestration
	for rows.Next() {
		item, err := scanOrchestration(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, rows.Err()
}

func (s *Store) listOrchestrationTasks(ctx context.Context, id string) ([]core.OrchestrationTask, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT orchestration_id,id,agent_id,project,input,depends_on,status,output,error,
		invocation_id,conversation_id,created_at,started_at,finished_at,updated_at
		FROM orchestration_tasks WHERE orchestration_id=? ORDER BY created_at,id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.OrchestrationTask
	for rows.Next() {
		var task core.OrchestrationTask
		var agentID, project, dependsOn, output, errText, invocationID, conversationID sql.NullString
		var createdAt, startedAt, finishedAt, updatedAt string
		if err := rows.Scan(&task.OrchestrationID, &task.ID, &agentID, &project, &task.Input, &dependsOn, &task.Status,
			&output, &errText, &invocationID, &conversationID, &createdAt, &startedAt, &finishedAt, &updatedAt); err != nil {
			return nil, err
		}
		task.AgentID = agentID.String
		task.Output, task.Error = output.String, errText.String
		task.InvocationID, task.ConversationID = invocationID.String, conversationID.String
		_ = json.Unmarshal([]byte(dependsOn.String), &task.DependsOn)
		task.CreatedAt, task.StartedAt = parseControlTime(createdAt), parseControlTime(startedAt)
		task.FinishedAt, task.UpdatedAt = parseControlTime(finishedAt), parseControlTime(updatedAt)
		out = append(out, task)
	}
	return out, rows.Err()
}

func scanOrchestration(sc scanner) (*core.Orchestration, error) {
	var orchestration core.Orchestration
	var errText, ownerTenantID, createdAt, startedAt, finishedAt, updatedAt sql.NullString
	if err := sc.Scan(&orchestration.ID, &orchestration.Name, &orchestration.Status, &orchestration.MaxConcurrency,
		&errText, &ownerTenantID, &createdAt, &startedAt, &finishedAt, &updatedAt); err != nil {
		return nil, err
	}
	orchestration.Error = errText.String
	orchestration.OwnerTenantID = ownerTenantID.String
	orchestration.CreatedAt = parseControlTime(createdAt.String)
	orchestration.StartedAt = parseControlTime(startedAt.String)
	orchestration.FinishedAt = parseControlTime(finishedAt.String)
	orchestration.UpdatedAt = parseControlTime(updatedAt.String)
	return &orchestration, nil
}
