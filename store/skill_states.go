package store

import (
	"context"
	"time"
)

func (s *Store) ListSkillStates(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name,enabled FROM skill_states`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		var enabled int
		if err := rows.Scan(&name, &enabled); err != nil {
			return nil, err
		}
		out[name] = enabled != 0
	}
	return out, rows.Err()
}

func (s *Store) SetSkillEnabled(ctx context.Context, name string, enabled bool) error {
	value := 0
	if enabled {
		value = 1
	}
	_, err := s.writer.ExecContext(ctx, `INSERT INTO skill_states(name,enabled,updated_at)
		VALUES(?,?,?) ON CONFLICT(name) DO UPDATE SET enabled=excluded.enabled,updated_at=excluded.updated_at`,
		name, value, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) DeleteSkillState(ctx context.Context, name string) error {
	_, err := s.writer.ExecContext(ctx, `DELETE FROM skill_states WHERE name=?`, name)
	return err
}
