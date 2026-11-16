package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// Account handover.
//
// A hub that starts following a primary replaces its hub data with the
// primary's, accounts included. Before it does, it hands the accounts it
// holds to the primary, which keeps the ones it does not know, so a person
// who registered on either hub has one account on both afterwards. A
// returning former primary does the same with accounts made while the two
// were apart. Sessions are not handed over: people sign in again.

// HandoverAccount is one account as one hub hands it to another, password
// hash included. It travels only over a hub's own link to its primary.
type HandoverAccount struct {
	ID                string     `json:"id"`
	Login             string     `json:"login"`
	DisplayName       string     `json:"display_name,omitempty"`
	Role              string     `json:"role"`
	Status            string     `json:"status"`
	PasswordHash      string     `json:"password_hash,omitempty"`
	PasswordChangedAt string     `json:"password_changed_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	LastSignInAt      *time.Time `json:"last_sign_in_at,omitempty"`
}

// HandoverResult says what the receiving hub did with a handover.
type HandoverResult struct {
	// Added are logins the receiving hub did not know and now holds.
	Added []string `json:"added"`
	// Known are accounts it already held under the same id; its copy wins.
	Known int `json:"known"`
	// Conflicts are logins that belong to a different account there. That
	// account wins; the person signs in with it.
	Conflicts []string `json:"conflicts"`
}

// ExportAccountsForHandover returns every account with its password hash.
func (s *Store) ExportAccountsForHandover(ctx context.Context) ([]HandoverAccount, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,login,display_name,role,status,password_hash,password_changed_at,created_at,updated_at,last_sign_in_at
		FROM accounts ORDER BY created_at, login`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HandoverAccount
	for rows.Next() {
		var account HandoverAccount
		var displayName, hash, changed, created, updated, lastSignIn sql.NullString
		if err := rows.Scan(&account.ID, &account.Login, &displayName, &account.Role, &account.Status,
			&hash, &changed, &created, &updated, &lastSignIn); err != nil {
			return nil, err
		}
		account.DisplayName, account.PasswordHash, account.PasswordChangedAt = displayName.String, hash.String, changed.String
		account.CreatedAt, _ = time.Parse(time.RFC3339Nano, created.String)
		account.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated.String)
		account.LastSignInAt = nullableTime(lastSignIn)
		out = append(out, account)
	}
	return out, rows.Err()
}

// ImportHandoverAccounts keeps the handed-over accounts this hub does not
// know. An account it holds under the same id is left as it is here, and a
// login that belongs to another account here stays with that account, so a
// handover never overwrites, revives or takes over anything.
func (s *Store) ImportHandoverAccounts(ctx context.Context, accounts []HandoverAccount) (HandoverResult, error) {
	result := HandoverResult{Added: []string{}, Conflicts: []string{}}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	if s.dialect == DialectPostgres {
		if _, err := tx.ExecContext(ctx, `LOCK TABLE accounts IN SHARE ROW EXCLUSIVE MODE`); err != nil {
			return result, err
		}
	}
	for _, account := range accounts {
		login := core.NormalizeLogin(account.Login)
		if account.ID == "" || core.ValidateLogin(login) != nil {
			continue
		}
		var sameID, sameLogin int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE id=?`, account.ID).Scan(&sameID); err != nil {
			return result, err
		}
		if sameID > 0 {
			result.Known++
			continue
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE login=?`, login).Scan(&sameLogin); err != nil {
			return result, err
		}
		if sameLogin > 0 {
			result.Conflicts = append(result.Conflicts, login)
			continue
		}
		status := account.Status
		if status != core.AccountStatusDisabled && status != core.AccountStatusPending {
			status = core.AccountStatusActive
		}
		created, updated := account.CreatedAt, account.UpdatedAt
		if created.IsZero() {
			created = time.Now().UTC()
		}
		if updated.IsZero() {
			updated = created
		}
		var lastSignIn any
		if account.LastSignInAt != nil {
			lastSignIn = account.LastSignInAt.UTC().Format(time.RFC3339Nano)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO accounts (id,login,display_name,role,status,password_hash,password_changed_at,created_at,updated_at,last_sign_in_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`,
			account.ID, login, account.DisplayName, core.NormalizeAccountRole(account.Role), status,
			nullableString(account.PasswordHash), nullableString(account.PasswordChangedAt),
			created.UTC().Format(time.RFC3339Nano), updated.UTC().Format(time.RFC3339Nano), lastSignIn); err != nil {
			return result, err
		}
		result.Added = append(result.Added, login)
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}
