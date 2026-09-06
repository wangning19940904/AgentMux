package store

import "context"

// BackfillUsageRuntimes fills only unclassified transcript rows, scoped to the
// original product and host. It never changes usage amounts, identities, or
// client metadata already supplied by a live request.
func (s *Store) BackfillUsageRuntimes(ctx context.Context, source, host string, sessions map[string]string) error {
	if len(sessions) == 0 {
		return nil
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `UPDATE usage_records SET runtime_id=?
		WHERE source=? AND COALESCE(host,'')=? AND session_id=?
		AND COALESCE(request_id,'')='' AND COALESCE(runtime_id,'')=''`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for session, runtime := range sessions {
		if session == "" || runtime == "" {
			continue
		}
		if _, err := stmt.ExecContext(ctx, runtime, source, host, session); err != nil {
			return err
		}
	}
	return tx.Commit()
}
