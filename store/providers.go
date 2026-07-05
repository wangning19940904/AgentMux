package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/agentnexus/agentnexus/core"
)

func joinTools(t []string) string { return strings.Join(t, ",") }
func splitTools(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// ListProviders returns all providers ordered by name.
func (s *Store) ListProviders(ctx context.Context) ([]*core.Provider, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,preset,category,base_url,api_key_env,
		model,tools,extra,settings_config,meta,enabled,created_at,updated_at FROM providers ORDER BY name`)
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
		model,tools,extra,settings_config,meta,enabled,created_at,updated_at FROM providers WHERE id=?`, id)
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
	var preset, category, apiKeyEnv, model, tools, extra, settingsConfig, meta, created, updated sql.NullString
	var enabled int
	if err := sc.Scan(&p.ID, &p.Name, &preset, &category, &p.BaseURL, &apiKeyEnv,
		&model, &tools, &extra, &settingsConfig, &meta, &enabled, &created, &updated); err != nil {
		return nil, err
	}
	p.Preset = preset.String
	p.Category = category.String
	p.APIKeyEnv = apiKeyEnv.String
	p.Model = model.String
	p.Tools = splitTools(tools.String)
	p.Enabled = enabled != 0
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
	_, err := s.db.ExecContext(ctx, `INSERT INTO providers
		(id,name,preset,category,base_url,api_key_env,model,tools,extra,settings_config,meta,enabled,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name,preset=excluded.preset,
		category=excluded.category,base_url=excluded.base_url,api_key_env=excluded.api_key_env,model=excluded.model,
		tools=excluded.tools,extra=excluded.extra,settings_config=excluded.settings_config,meta=excluded.meta,enabled=excluded.enabled,
		updated_at=excluded.updated_at`,
		p.ID, p.Name, p.Preset, p.Category, p.BaseURL, p.APIKeyEnv, p.Model, joinTools(p.Tools),
		string(extra), string(settingsConfig), string(meta), enabled, p.CreatedAt.Format(time.RFC3339Nano), p.UpdatedAt.Format(time.RFC3339Nano))
	return err
}

// DeleteProvider removes a provider by id.
func (s *Store) DeleteProvider(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
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
	tx, err := s.db.BeginTx(ctx, nil)
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

// ClearActiveProvider removes the active route for one tool and recomputes
// provider enabled flags from the remaining active routes.
func (s *Store) ClearActiveProvider(ctx context.Context, tool string) error {
	tx, err := s.db.BeginTx(ctx, nil)
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

// ActiveProviderRoutes returns every tool binding from active_provider, joined
// to the provider row when it still exists.
func (s *Store) ActiveProviderRoutes(ctx context.Context) ([]core.ProviderRoute, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ap.tool,ap.provider_id,
		COALESCE(p.name,''),COALESCE(p.base_url,''),COALESCE(p.api_key_env,''),
		COALESCE(p.model,''),COALESCE(p.meta,''),CASE WHEN p.id IS NULL THEN 0 ELSE 1 END
		FROM active_provider ap
		LEFT JOIN providers p ON p.id=ap.provider_id
		ORDER BY ap.tool`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.ProviderRoute
	for rows.Next() {
		var route core.ProviderRoute
		var metaRaw string
		var configured int
		if err := rows.Scan(&route.Tool, &route.ProviderID, &route.ProviderName, &route.BaseURL,
			&route.APIKeyEnv, &route.Model, &metaRaw, &configured); err != nil {
			return nil, err
		}
		route.Configured = configured != 0
		if metaRaw != "" {
			var meta core.ProviderMeta
			_ = json.Unmarshal([]byte(metaRaw), &meta)
			route.APIFormat = meta.APIFormat
		}
		out = append(out, route)
	}
	return out, rows.Err()
}
