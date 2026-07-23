package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/agentnexus/agentnexus/core"
)

// ListChannels returns all console-managed channels, enabled first.
func (s *Store) ListChannels(ctx context.Context) ([]core.Channel, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,type,agent_id,config,enabled,
		created_at,updated_at FROM channels ORDER BY enabled DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Channel
	for rows.Next() {
		ch, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	return out, rows.Err()
}

// GetChannel returns one channel or (nil,nil) if absent.
func (s *Store) GetChannel(ctx context.Context, id string) (*core.Channel, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,name,type,agent_id,config,enabled,
		created_at,updated_at FROM channels WHERE id=?`, id)
	ch, err := scanChannel(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &ch, err
}

// UpsertChannel inserts or updates a channel.
func (s *Store) UpsertChannel(ctx context.Context, ch *core.Channel) error {
	cfg, _ := json.Marshal(ch.Config)
	enabled := 0
	if ch.Enabled {
		enabled = 1
	}
	_, err := s.writer.ExecContext(ctx, `INSERT INTO channels
		(id,name,type,agent_id,config,enabled,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name,type=excluded.type,
		agent_id=excluded.agent_id,config=excluded.config,enabled=excluded.enabled,
		updated_at=excluded.updated_at`,
		ch.ID, ch.Name, ch.Type, ch.AgentID, string(cfg), enabled,
		ch.CreatedAt.Format(time.RFC3339Nano), ch.UpdatedAt.Format(time.RFC3339Nano))
	return err
}

// DeleteChannel removes a channel.
func (s *Store) DeleteChannel(ctx context.Context, id string) error {
	_, err := s.writer.ExecContext(ctx, `DELETE FROM channels WHERE id=?`, id)
	return err
}

func scanChannel(sc scanner) (core.Channel, error) {
	var ch core.Channel
	var agentID, cfg, created, updated sql.NullString
	var enabled int
	if err := sc.Scan(&ch.ID, &ch.Name, &ch.Type, &agentID, &cfg, &enabled,
		&created, &updated); err != nil {
		return ch, err
	}
	ch.AgentID = agentID.String
	ch.Enabled = enabled != 0
	if cfg.String != "" {
		_ = json.Unmarshal([]byte(cfg.String), &ch.Config)
	}
	ch.CreatedAt, _ = time.Parse(time.RFC3339Nano, created.String)
	ch.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated.String)
	return ch, nil
}
