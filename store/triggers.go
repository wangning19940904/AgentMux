package store

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	"github.com/agentnexus/agentnexus/core"
)

const triggerColumns = `id,name,kind,agent_id,channel_id,chat_id,cron_expr,prompt,
	event,action_type,action_target,token,session_mode,enabled,last_run,last_status,
	last_error,created_at,updated_at`

// ListTriggers returns all triggers, enabled first then by name.
func (s *Store) ListTriggers(ctx context.Context) ([]core.Trigger, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+triggerColumns+` FROM triggers ORDER BY enabled DESC, kind, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Trigger
	for rows.Next() {
		tr, err := scanTrigger(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, tr)
	}
	return out, rows.Err()
}

// GetTrigger returns one trigger or (nil,nil) if absent.
func (s *Store) GetTrigger(ctx context.Context, id string) (*core.Trigger, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+triggerColumns+` FROM triggers WHERE id=?`, id)
	tr, err := scanTrigger(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &tr, err
}

// UpsertTrigger inserts or updates a trigger definition. Run bookkeeping
// (last_run/last_status/last_error) is preserved on update.
func (s *Store) UpsertTrigger(ctx context.Context, tr *core.Trigger) error {
	enabled := 0
	if tr.Enabled {
		enabled = 1
	}
	lastRun := ""
	if !tr.LastRun.IsZero() {
		lastRun = tr.LastRun.Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO triggers
		(id,name,kind,agent_id,channel_id,chat_id,cron_expr,prompt,event,action_type,
		 action_target,token,session_mode,enabled,last_run,last_status,last_error,
		 created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name,kind=excluded.kind,
		agent_id=excluded.agent_id,channel_id=excluded.channel_id,chat_id=excluded.chat_id,
		cron_expr=excluded.cron_expr,prompt=excluded.prompt,event=excluded.event,
		action_type=excluded.action_type,action_target=excluded.action_target,
		token=excluded.token,session_mode=excluded.session_mode,enabled=excluded.enabled,
		updated_at=excluded.updated_at`,
		tr.ID, tr.Name, tr.Kind, tr.AgentID, tr.ChannelID, tr.ChatID, tr.CronExpr,
		tr.Prompt, tr.Event, tr.ActionType, tr.ActionTarget, tr.Token, tr.SessionMode,
		enabled, lastRun, tr.LastStatus, tr.LastError,
		tr.CreatedAt.Format(time.RFC3339Nano), tr.UpdatedAt.Format(time.RFC3339Nano))
	return err
}

// UpdateTriggerRun records the outcome of one trigger execution.
func (s *Store) UpdateTriggerRun(ctx context.Context, id string, lastRun time.Time, status, errMsg string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE triggers SET last_run=?, last_status=?, last_error=? WHERE id=?`,
		lastRun.Format(time.RFC3339Nano), status, errMsg, id)
	return err
}

// DeleteTrigger removes a trigger.
func (s *Store) DeleteTrigger(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM triggers WHERE id=?`, id)
	return err
}

func scanTrigger(sc scanner) (core.Trigger, error) {
	var tr core.Trigger
	var agentID, channelID, chatID, cronExpr, prompt, event sql.NullString
	var actionType, actionTarget, token, sessionMode sql.NullString
	var lastRun, lastStatus, lastError, created, updated sql.NullString
	var enabled int
	if err := sc.Scan(&tr.ID, &tr.Name, &tr.Kind, &agentID, &channelID, &chatID,
		&cronExpr, &prompt, &event, &actionType, &actionTarget, &token, &sessionMode,
		&enabled, &lastRun, &lastStatus, &lastError, &created, &updated); err != nil {
		return tr, err
	}
	tr.AgentID = agentID.String
	tr.ChannelID = channelID.String
	tr.ChatID = chatID.String
	tr.CronExpr = cronExpr.String
	tr.Prompt = prompt.String
	tr.Event = event.String
	tr.ActionType = actionType.String
	tr.ActionTarget = actionTarget.String
	tr.Token = token.String
	tr.SessionMode = sessionMode.String
	tr.Enabled = enabled != 0
	tr.LastStatus = lastStatus.String
	tr.LastError = lastError.String
	if lastRun.String != "" {
		tr.LastRun, _ = time.Parse(time.RFC3339Nano, lastRun.String)
	}
	tr.CreatedAt, _ = time.Parse(time.RFC3339Nano, created.String)
	tr.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated.String)
	return tr, nil
}

const bindingsMigratedKey = "channels_triggers_migrated"

// migrateAgentBindings promotes legacy agent_instances.channel_bindings and
// .schedules JSON blobs into the first-class channels/triggers tables. Runs
// once, guarded by a settings marker; the legacy columns are left untouched
// for read-only compatibility.
func (s *Store) migrateAgentBindings() error {
	ctx := context.Background()
	if _, done, err := s.GetSetting(ctx, bindingsMigratedKey); err != nil {
		return err
	} else if done {
		return nil
	}
	instances, err := s.ListAgentInstances(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, inst := range instances {
		firstChannelID, firstChatID := "", ""
		for i, binding := range inst.ChannelBindings {
			id := inst.ID + "-ch-" + strconv.Itoa(i)
			if firstChannelID == "" {
				firstChannelID = id
				firstChatID = binding.ChatID
			}
			name := binding.Name
			if name == "" {
				name = binding.Type
			}
			cfg := map[string]string{}
			for k, v := range binding.Config {
				cfg[k] = v
			}
			if binding.ChatID != "" {
				cfg["chat_id"] = binding.ChatID
			}
			ch := &core.Channel{
				ID:      id,
				Name:    name,
				Type:    binding.Type,
				AgentID: inst.ID,
				Config:  cfg,
				// Channels never ran before this feature existed; require an
				// explicit enable from the console so migration cannot start
				// connections with stale credentials.
				Enabled:   false,
				CreatedAt: now,
				UpdatedAt: now,
			}
			if err := s.UpsertChannel(ctx, ch); err != nil {
				return err
			}
		}
		for i, schedule := range inst.Schedules {
			name := schedule.Name
			if name == "" {
				name = "schedule " + strconv.Itoa(i+1)
			}
			tr := &core.Trigger{
				ID:          inst.ID + "-tr-" + strconv.Itoa(i),
				Name:        name,
				Kind:        core.TriggerCron,
				AgentID:     inst.ID,
				ChannelID:   firstChannelID,
				ChatID:      firstChatID,
				CronExpr:    schedule.Cron,
				Prompt:      schedule.Prompt,
				SessionMode: core.SessionModeReuse,
				Enabled:     schedule.Enabled,
				CreatedAt:   now,
				UpdatedAt:   now,
			}
			if err := s.UpsertTrigger(ctx, tr); err != nil {
				return err
			}
		}
	}
	return s.SetSetting(ctx, bindingsMigratedKey, now.Format(time.RFC3339))
}
