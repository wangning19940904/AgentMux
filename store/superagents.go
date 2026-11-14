package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func (s *Store) ListSuperAgents(ctx context.Context) ([]core.SuperAgent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM super_agents ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, err
	}
	return scanSuperAgentRows[core.SuperAgent](rows)
}

func (s *Store) GetSuperAgent(ctx context.Context, id string) (*core.SuperAgent, error) {
	return getSuperAgentRow[core.SuperAgent](ctx, s, `SELECT data FROM super_agents WHERE id=?`, id)
}

func (s *Store) UpsertSuperAgent(ctx context.Context, agent core.SuperAgent) error {
	data, err := json.Marshal(agent)
	if err != nil {
		return err
	}
	_, err = s.writer.ExecContext(ctx, `INSERT INTO super_agents (id,data,created_at,updated_at) VALUES (?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET data=excluded.data,updated_at=excluded.updated_at`,
		agent.ID, string(data), formatControlTime(agent.CreatedAt), formatControlTime(agent.UpdatedAt))
	return err
}

// DeleteSuperAgent removes the configuration and its audit trail together, so
// a later Super Agent cannot inherit Tickets or read positions.
func (s *Store) DeleteSuperAgent(ctx context.Context, id string) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, query := range []string{
		`DELETE FROM super_agent_tickets WHERE super_agent_id=?`,
		`DELETE FROM super_agent_triggers WHERE super_agent_id=?`,
		`DELETE FROM super_agent_timers WHERE super_agent_id=?`,
		`DELETE FROM super_agent_source_state WHERE super_agent_id=?`,
		`DELETE FROM super_agents WHERE id=?`,
	} {
		if _, err := tx.ExecContext(ctx, query, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// InsertSuperAgentTrigger stores one event once and reports whether it was new.
func (s *Store) InsertSuperAgentTrigger(ctx context.Context, trigger core.SuperAgentTrigger) (bool, error) {
	data, err := json.Marshal(trigger)
	if err != nil {
		return false, err
	}
	result, err := s.writer.ExecContext(ctx, `INSERT INTO super_agent_triggers
		(id,super_agent_id,source_id,dedupe_key,thread_key,disposition,data,received_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?) ON CONFLICT(super_agent_id,source_id,dedupe_key) DO NOTHING`,
		trigger.ID, trigger.SuperAgentID, trigger.SourceID, trigger.DedupeKey, trigger.ThreadKey, trigger.Disposition,
		string(data), formatControlTime(trigger.ReceivedAt), formatControlTime(trigger.UpdatedAt))
	if err != nil {
		return false, err
	}
	inserted, err := result.RowsAffected()
	return inserted > 0, err
}

func (s *Store) UpdateSuperAgentTrigger(ctx context.Context, trigger core.SuperAgentTrigger) error {
	data, err := json.Marshal(trigger)
	if err != nil {
		return err
	}
	_, err = s.writer.ExecContext(ctx, `UPDATE super_agent_triggers SET disposition=?,data=?,updated_at=? WHERE id=?`,
		trigger.Disposition, string(data), formatControlTime(trigger.UpdatedAt), trigger.ID)
	return err
}

func (s *Store) ListPendingSuperAgentTriggers(ctx context.Context, limit int) ([]core.SuperAgentTrigger, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM super_agent_triggers WHERE disposition=?
		ORDER BY received_at, id LIMIT ?`, core.SuperAgentDispositionPending, limit)
	if err != nil {
		return nil, err
	}
	return scanSuperAgentRows[core.SuperAgentTrigger](rows)
}

func (s *Store) ListSuperAgentTriggers(ctx context.Context, superAgentID string, limit int) ([]core.SuperAgentTrigger, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM super_agent_triggers WHERE super_agent_id=?
		ORDER BY received_at DESC, id LIMIT ?`, superAgentID, limit)
	if err != nil {
		return nil, err
	}
	return scanSuperAgentRows[core.SuperAgentTrigger](rows)
}

func (s *Store) UpsertSuperAgentTicket(ctx context.Context, ticket core.SuperAgentTicket) error {
	data, err := json.Marshal(ticket)
	if err != nil {
		return err
	}
	_, err = s.writer.ExecContext(ctx, `INSERT INTO super_agent_tickets
		(id,super_agent_id,thread_key,status,data,created_at,updated_at) VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET status=excluded.status,data=excluded.data,updated_at=excluded.updated_at`,
		ticket.ID, ticket.SuperAgentID, ticket.ThreadKey, ticket.Status, string(data),
		formatControlTime(ticket.CreatedAt), formatControlTime(ticket.UpdatedAt))
	return err
}

func (s *Store) GetSuperAgentTicket(ctx context.Context, id string) (*core.SuperAgentTicket, error) {
	return getSuperAgentRow[core.SuperAgentTicket](ctx, s, `SELECT data FROM super_agent_tickets WHERE id=?`, id)
}

func (s *Store) ListSuperAgentTickets(ctx context.Context, superAgentID string, limit int) ([]core.SuperAgentTicket, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM super_agent_tickets WHERE super_agent_id=?
		ORDER BY created_at DESC, id LIMIT ?`, superAgentID, limit)
	if err != nil {
		return nil, err
	}
	return scanSuperAgentRows[core.SuperAgentTicket](rows)
}

func (s *Store) ListSuperAgentThreadTickets(ctx context.Context, superAgentID, threadKey string) ([]core.SuperAgentTicket, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM super_agent_tickets WHERE super_agent_id=? AND thread_key=?
		ORDER BY created_at, id`, superAgentID, threadKey)
	if err != nil {
		return nil, err
	}
	return scanSuperAgentRows[core.SuperAgentTicket](rows)
}

func (s *Store) ListSuperAgentTicketsByStatus(ctx context.Context, statuses ...string) ([]core.SuperAgentTicket, error) {
	var out []core.SuperAgentTicket
	for _, status := range statuses {
		rows, err := s.db.QueryContext(ctx, `SELECT data FROM super_agent_tickets WHERE status=? ORDER BY created_at, id`, status)
		if err != nil {
			return nil, err
		}
		items, err := scanSuperAgentRows[core.SuperAgentTicket](rows)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	return out, nil
}

// formatTimerTime writes a timer's times as whole seconds in UTC. Every value
// then has the same width, so the text columns sort and compare as times.
func formatTimerTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}

func (s *Store) UpsertSuperAgentTimer(ctx context.Context, timer core.SuperAgentTimer) error {
	data, err := json.Marshal(timer)
	if err != nil {
		return err
	}
	_, err = s.writer.ExecContext(ctx, `INSERT INTO super_agent_timers
		(id,super_agent_id,thread_key,status,due_at,data,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET status=excluded.status,due_at=excluded.due_at,data=excluded.data,updated_at=excluded.updated_at`,
		timer.ID, timer.SuperAgentID, timer.ThreadKey, timer.Status, formatTimerTime(timer.DueAt), string(data),
		formatTimerTime(timer.CreatedAt), formatTimerTime(timer.UpdatedAt))
	return err
}

func (s *Store) GetSuperAgentTimer(ctx context.Context, id string) (*core.SuperAgentTimer, error) {
	return getSuperAgentRow[core.SuperAgentTimer](ctx, s, `SELECT data FROM super_agent_timers WHERE id=?`, id)
}

// ListDueSuperAgentTimers returns the active timers due at or before now,
// the longest overdue first.
func (s *Store) ListDueSuperAgentTimers(ctx context.Context, now time.Time, limit int) ([]core.SuperAgentTimer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM super_agent_timers WHERE status=? AND due_at<=?
		ORDER BY due_at, id LIMIT ?`, core.SuperAgentTimerActive, formatTimerTime(now), limit)
	if err != nil {
		return nil, err
	}
	return scanSuperAgentRows[core.SuperAgentTimer](rows)
}

// ListActiveSuperAgentTimers returns an agent's timers that have yet to fire,
// the soonest first.
func (s *Store) ListActiveSuperAgentTimers(ctx context.Context, superAgentID string) ([]core.SuperAgentTimer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM super_agent_timers WHERE super_agent_id=? AND status=?
		ORDER BY due_at, id`, superAgentID, core.SuperAgentTimerActive)
	if err != nil {
		return nil, err
	}
	return scanSuperAgentRows[core.SuperAgentTimer](rows)
}

// ListFinishedSuperAgentTimers returns an agent's timers that fired or were
// cancelled since a time, the latest first.
func (s *Store) ListFinishedSuperAgentTimers(ctx context.Context, superAgentID string, since time.Time, limit int) ([]core.SuperAgentTimer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM super_agent_timers WHERE super_agent_id=? AND status<>? AND updated_at>=?
		ORDER BY updated_at DESC, id LIMIT ?`, superAgentID, core.SuperAgentTimerActive, formatTimerTime(since), limit)
	if err != nil {
		return nil, err
	}
	return scanSuperAgentRows[core.SuperAgentTimer](rows)
}

// CountSuperAgentTimersFired counts an agent's timers that fired since a time.
func (s *Store) CountSuperAgentTimersFired(ctx context.Context, superAgentID string, since time.Time) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM super_agent_timers WHERE super_agent_id=? AND status=? AND updated_at>=?`,
		superAgentID, core.SuperAgentTimerDone, formatTimerTime(since)).Scan(&count)
	return count, err
}

// GetSuperAgentSourceState returns a Source's read position, such as an IMAP
// UID, which is what keeps a restart from replaying events.
func (s *Store) GetSuperAgentSourceState(ctx context.Context, sourceID string) (string, error) {
	var state string
	err := s.db.QueryRowContext(ctx, `SELECT state FROM super_agent_source_state WHERE source_id=?`, sourceID).Scan(&state)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return state, err
}

func (s *Store) SetSuperAgentSourceState(ctx context.Context, superAgentID, sourceID, state string) error {
	_, err := s.writer.ExecContext(ctx, `INSERT INTO super_agent_source_state (source_id,super_agent_id,state) VALUES (?,?,?)
		ON CONFLICT(source_id) DO UPDATE SET state=excluded.state`, sourceID, superAgentID, state)
	return err
}

func getSuperAgentRow[T any](ctx context.Context, s *Store, query, id string) (*T, error) {
	var data string
	if err := s.db.QueryRowContext(ctx, query, id).Scan(&data); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	var item T
	if err := json.Unmarshal([]byte(data), &item); err != nil {
		return nil, err
	}
	return &item, nil
}

func scanSuperAgentRows[T any](rows *sql.Rows) ([]T, error) {
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var item T
		if err := json.Unmarshal([]byte(data), &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
