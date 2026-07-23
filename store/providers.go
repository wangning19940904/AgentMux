package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/agentnexus/agentnexus/core"
)

// ListProviders returns all providers ordered by name.
func (s *Store) ListProviders(ctx context.Context) ([]*core.Provider, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,preset,category,base_url,api_key_env,
		model,extra,settings_config,meta,enabled,in_failover_queue,sort_index,created_at,updated_at FROM providers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*core.Provider
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetProvider returns one provider or (nil,nil) if absent.
func (s *Store) GetProvider(ctx context.Context, id string) (*core.Provider, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,name,preset,category,base_url,api_key_env,
		model,extra,settings_config,meta,enabled,in_failover_queue,sort_index,created_at,updated_at FROM providers WHERE id=?`, id)
	p, err := scanProvider(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return p, err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanProvider(sc scanner) (*core.Provider, error) {
	var p core.Provider
	var preset, category, apiKeyEnv, model, extra, settingsConfig, meta, created, updated sql.NullString
	var enabled int
	var inFailoverQueue, sortIndex sql.NullInt64
	if err := sc.Scan(&p.ID, &p.Name, &preset, &category, &p.BaseURL, &apiKeyEnv,
		&model, &extra, &settingsConfig, &meta, &enabled, &inFailoverQueue, &sortIndex, &created, &updated); err != nil {
		return nil, err
	}
	p.Preset = preset.String
	p.Category = category.String
	p.APIKeyEnv = apiKeyEnv.String
	p.Model = model.String
	p.Enabled = enabled != 0
	p.InFailoverQueue = inFailoverQueue.Int64 != 0
	p.SortIndex = int(sortIndex.Int64)
	if extra.String != "" {
		_ = json.Unmarshal([]byte(extra.String), &p.Extra)
	}
	if settingsConfig.String != "" {
		_ = json.Unmarshal([]byte(settingsConfig.String), &p.SettingsConfig)
	}
	if meta.String != "" {
		_ = json.Unmarshal([]byte(meta.String), &p.Meta)
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339Nano, created.String)
	p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated.String)
	return &p, nil
}

// UpsertProvider inserts or updates a provider atomically.
func (s *Store) UpsertProvider(ctx context.Context, p *core.Provider) error {
	extra, _ := json.Marshal(p.Extra)
	settingsConfig, _ := json.Marshal(p.SettingsConfig)
	meta, _ := json.Marshal(p.Meta)
	enabled := 0
	if p.Enabled {
		enabled = 1
	}
	inQueue := 0
	if p.InFailoverQueue {
		inQueue = 1
	}
	_, err := s.writer.ExecContext(ctx, `INSERT INTO providers
		(id,name,preset,category,base_url,api_key_env,model,extra,settings_config,meta,enabled,in_failover_queue,sort_index,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name,preset=excluded.preset,
		category=excluded.category,base_url=excluded.base_url,api_key_env=excluded.api_key_env,model=excluded.model,
		extra=excluded.extra,settings_config=excluded.settings_config,meta=excluded.meta,enabled=excluded.enabled,
		in_failover_queue=excluded.in_failover_queue,sort_index=excluded.sort_index,
		updated_at=excluded.updated_at`,
		p.ID, p.Name, p.Preset, p.Category, p.BaseURL, p.APIKeyEnv, p.Model,
		string(extra), string(settingsConfig), string(meta), enabled, inQueue, p.SortIndex,
		p.CreatedAt.Format(time.RFC3339Nano), p.UpdatedAt.Format(time.RFC3339Nano))
	return err
}

// SetFailoverQueue updates a provider's failover-queue membership and order.
func (s *Store) SetFailoverQueue(ctx context.Context, id string, inQueue bool, sortIndex int) error {
	v := 0
	if inQueue {
		v = 1
	}
	_, err := s.writer.ExecContext(ctx,
		`UPDATE providers SET in_failover_queue=?, sort_index=? WHERE id=?`, v, sortIndex, id)
	return err
}

// DeleteProvider removes a provider by id.
func (s *Store) DeleteProvider(ctx context.Context, id string) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM providers WHERE id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM active_provider WHERE provider_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE providers SET enabled=0`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE providers SET enabled=1 WHERE id IN (SELECT provider_id FROM active_provider)`); err != nil {
		return err
	}
	return tx.Commit()
}

// SetActiveProvider marks provider id active for a tool (and flips enabled flags).
func (s *Store) SetActiveProvider(ctx context.Context, tool, id string) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO active_provider(tool,provider_id)
		VALUES(?,?) ON CONFLICT(tool) DO UPDATE SET provider_id=excluded.provider_id`,
		tool, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE providers SET enabled=0`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE providers SET enabled=1 WHERE id IN (SELECT provider_id FROM active_provider)`); err != nil {
		return err
	}
	return tx.Commit()
}

// SetActiveProviderRoute marks provider id active for a tool and writes the
// route-owned metadata for that binding.
func (s *Store) SetActiveProviderRoute(ctx context.Context, route core.ProviderRoute) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	meta, _ := json.Marshal(route.Meta)
	if _, err := tx.ExecContext(ctx, `INSERT INTO active_provider(tool,provider_id,meta)
		VALUES(?,?,?) ON CONFLICT(tool) DO UPDATE SET provider_id=excluded.provider_id,meta=excluded.meta`,
		route.Tool, route.ProviderID, string(meta)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE providers SET enabled=0`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE providers SET enabled=1 WHERE id IN (SELECT provider_id FROM active_provider)`); err != nil {
		return err
	}
	return tx.Commit()
}

// ClearActiveProvider removes the active route for one tool and recomputes
// provider enabled flags from the remaining active routes.
func (s *Store) ClearActiveProvider(ctx context.Context, tool string) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM active_provider WHERE tool=?`, tool); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE providers SET enabled=0`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE providers SET enabled=1 WHERE id IN (SELECT provider_id FROM active_provider)`); err != nil {
		return err
	}
	return tx.Commit()
}

// ActiveProviderID returns the active provider id for a tool.
func (s *Store) ActiveProviderID(ctx context.Context, tool string) (string, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT provider_id FROM active_provider WHERE tool=?`, tool)
	var id string
	switch err := row.Scan(&id); err {
	case nil:
		return id, true, nil
	case sql.ErrNoRows:
		return "", false, nil
	default:
		return "", false, err
	}
}

// ActiveProviderRoute returns the active route row for a tool.
func (s *Store) ActiveProviderRoute(ctx context.Context, tool string) (core.ProviderRoute, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT ap.tool,ap.provider_id,
		COALESCE(p.name,''),COALESCE(p.base_url,''),COALESCE(p.api_key_env,''),
		COALESCE(p.model,''),COALESCE(p.meta,''),COALESCE(ap.meta,''),CASE WHEN p.id IS NULL THEN 0 ELSE 1 END
		FROM active_provider ap
		LEFT JOIN providers p ON p.id=ap.provider_id
		WHERE ap.tool=?`, tool)
	route, err := scanProviderRoute(row)
	if err == sql.ErrNoRows {
		return core.ProviderRoute{}, false, nil
	}
	return route, err == nil, err
}

// ActiveProviderRoutes returns every tool binding from active_provider, joined
// to the provider row when it still exists.
func (s *Store) ActiveProviderRoutes(ctx context.Context) ([]core.ProviderRoute, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ap.tool,ap.provider_id,
		COALESCE(p.name,''),COALESCE(p.base_url,''),COALESCE(p.api_key_env,''),
		COALESCE(p.model,''),COALESCE(p.meta,''),COALESCE(ap.meta,''),CASE WHEN p.id IS NULL THEN 0 ELSE 1 END
		FROM active_provider ap
		LEFT JOIN providers p ON p.id=ap.provider_id
		ORDER BY ap.tool`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.ProviderRoute
	for rows.Next() {
		route, err := scanProviderRoute(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, route)
	}
	return out, rows.Err()
}

func scanProviderRoute(sc scanner) (core.ProviderRoute, error) {
	var route core.ProviderRoute
	var providerMetaRaw, routeMetaRaw string
	var configured int
	if err := sc.Scan(&route.Tool, &route.ProviderID, &route.ProviderName, &route.BaseURL,
		&route.APIKeyEnv, &route.Model, &providerMetaRaw, &routeMetaRaw, &configured); err != nil {
		return route, err
	}
	route.Configured = configured != 0
	var providerMeta core.ProviderMeta
	if providerMetaRaw != "" {
		_ = json.Unmarshal([]byte(providerMetaRaw), &providerMeta)
		route.APIFormat = providerMeta.APIFormat
	}
	if routeMetaRaw != "" {
		_ = json.Unmarshal([]byte(routeMetaRaw), &route.Meta)
	}
	if core.RouteMetaIsZero(route.Meta) {
		route.Meta = core.RouteMetaFromProvider(providerMeta)
	}
	return route, nil
}
