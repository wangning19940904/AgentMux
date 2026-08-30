package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

const channelColumns = `id,name,type,agent_id,config,enabled,owner_tenant_id,visibility,
	created_at,updated_at`

// ListChannels returns all console-managed channels, enabled first. This is
// the admin and runtime view: the engine needs every channel to run it. Use
// ListChannelsForTenant for the scoped API view.
func (s *Store) ListChannels(ctx context.Context) ([]core.Channel, error) {
	return s.queryChannels(ctx,
		`SELECT `+channelColumns+` FROM channels ORDER BY enabled DESC, name`)
}

// ListChannelsForTenant returns the channels one tenant may see.
func (s *Store) ListChannelsForTenant(ctx context.Context, tenantID string) ([]core.Channel, error) {
	return s.queryChannels(ctx,
		`SELECT `+channelColumns+` FROM channels WHERE `+
			visibleToTenant(core.ResourceTypeChannel)+` ORDER BY enabled DESC, name`,
		visibleToTenantArgs(tenantID)...)
}

func (s *Store) queryChannels(ctx context.Context, query string, args ...any) ([]core.Channel, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
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
	row := s.db.QueryRowContext(ctx,
		`SELECT `+channelColumns+` FROM channels WHERE id=?`, id)
	ch, err := scanChannel(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &ch, err
}

// UpsertChannel inserts or updates a channel.
func (s *Store) UpsertChannel(ctx context.Context, ch *core.Channel) error {
	return upsertChannel(ctx, s.writer, ch)
}

func upsertChannel(ctx context.Context, executor statementExecutor, ch *core.Channel) error {
	cfg, _ := json.Marshal(ch.Config)
	enabled := 0
	if ch.Enabled {
		enabled = 1
	}
	_, err := executor.ExecContext(ctx, `INSERT INTO channels
		(id,name,type,agent_id,config,enabled,owner_tenant_id,visibility,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name,type=excluded.type,
		agent_id=excluded.agent_id,config=excluded.config,enabled=excluded.enabled,
		owner_tenant_id=excluded.owner_tenant_id,visibility=excluded.visibility,
		updated_at=excluded.updated_at`,
		ch.ID, ch.Name, ch.Type, ch.AgentID, string(cfg), enabled,
		nullableOwner(ch.OwnerTenantID), ch.Visibility,
		ch.CreatedAt.Format(time.RFC3339Nano), ch.UpdatedAt.Format(time.RFC3339Nano))
	return err
}

// DeleteChannel removes a channel.
func (s *Store) DeleteChannel(ctx context.Context, id string) error {
	_, err := s.writer.ExecContext(ctx, `DELETE FROM channels WHERE id=?`, id)
	return err
}

// SetChannelOwner assigns (or with an empty tenantID clears) ownership.
func (s *Store) SetChannelOwner(ctx context.Context, id, tenantID string) error {
	_, err := s.writer.ExecContext(ctx,
		`UPDATE channels SET owner_tenant_id=?, updated_at=? WHERE id=?`,
		nullableOwner(tenantID), time.Now().UTC().Format(time.RFC3339Nano), id)
	return err
}

func scanChannel(sc scanner) (core.Channel, error) {
	var ch core.Channel
	var agentID, cfg, ownerTenantID, visibility, created, updated sql.NullString
	var enabled int
	if err := sc.Scan(&ch.ID, &ch.Name, &ch.Type, &agentID, &cfg, &enabled,
		&ownerTenantID, &visibility, &created, &updated); err != nil {
		return ch, err
	}
	ch.AgentID = agentID.String
	ch.Enabled = enabled != 0
	ch.OwnerTenantID = ownerTenantID.String
	ch.Visibility = visibility.String
	if cfg.String != "" {
		_ = json.Unmarshal([]byte(cfg.String), &ch.Config)
	}
	ch.CreatedAt, _ = time.Parse(time.RFC3339Nano, created.String)
	ch.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated.String)
	return ch, nil
}
