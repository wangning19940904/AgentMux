package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// Accounts are the people who sign in. Each account owns a tenant namespace
// created with it, so ownership and grants reuse the tenancy tables. Session
// tokens are stored as SHA-256 hashes, like tenant tokens; passwords as
// argon2id hashes (see passwords.go).

const accountSchema = `
CREATE TABLE IF NOT EXISTS accounts (
	id TEXT PRIMARY KEY,
	login TEXT NOT NULL,
	display_name TEXT,
	role TEXT NOT NULL,
	status TEXT NOT NULL,
	tenant_id TEXT NOT NULL,
	password_hash TEXT,
	password_changed_at TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	last_sign_in_at TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_login ON accounts(login);
CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_tenant ON accounts(tenant_id);
CREATE TABLE IF NOT EXISTS account_sessions (
	id TEXT PRIMARY KEY,
	account_id TEXT NOT NULL,
	token_hash TEXT NOT NULL,
	method TEXT NOT NULL,
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	last_seen_at TEXT,
	user_agent TEXT,
	remote_addr TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_account_sessions_token ON account_sessions(token_hash);
CREATE INDEX IF NOT EXISTS idx_account_sessions_account ON account_sessions(account_id);
`

// accountOwnershipMigration gives devices an owner on PostgreSQL. The legacy
// SQLite store adds the same columns with ensureColumn.
const accountOwnershipMigration = `
ALTER TABLE devices ADD COLUMN IF NOT EXISTS owner_tenant_id TEXT;
ALTER TABLE device_pairing_codes ADD COLUMN IF NOT EXISTS owner_tenant_id TEXT;
CREATE INDEX IF NOT EXISTS idx_devices_owner ON devices(owner_tenant_id);
`

const (
	// AccountSessionTokenPrefix marks a session cookie that belongs to an
	// account, as opposed to a one-time-link Console session held in memory.
	AccountSessionTokenPrefix = "amxs_"
	accountColumns            = `id,login,display_name,role,status,tenant_id,password_hash,created_at,updated_at,last_sign_in_at`
	accountSessionSeenWindow  = 5 * time.Minute
)

var (
	// ErrLoginTaken is returned when a login, or the tenant name it would
	// give the account's namespace, is already in use.
	ErrLoginTaken = errors.New("that login name is already in use by an account or tenant")
	// ErrAccountsExist is returned when the first administrator was already
	// created.
	ErrAccountsExist = errors.New("accounts already exist; an administrator must create new ones")
	// ErrLastAdmin protects against locking every administrator out.
	ErrLastAdmin = errors.New("at least one active administrator must remain")
	// ErrAccountNotFound is returned by updates to an unknown account.
	ErrAccountNotFound = errors.New("account not found")
)

// NewAccount is what creating an account needs. Password may be empty for an
// account that will only sign in through another method.
type NewAccount struct {
	Login       string
	DisplayName string
	Role        string
	Password    string
}

// AccountUpdate changes the fields that are set.
type AccountUpdate struct {
	DisplayName *string
	Role        *string
	Status      *string
}

// CountAccounts returns how many accounts exist. Zero means this machine runs
// exactly as it did before accounts.
func (s *Store) CountAccounts(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts`).Scan(&count)
	return count, err
}

// CreateAccount creates an account together with its tenant namespace.
func (s *Store) CreateAccount(ctx context.Context, input NewAccount) (*core.Account, error) {
	return s.createAccount(ctx, input, false)
}

// CreateFirstAdmin creates the first account as an administrator, and fails
// with ErrAccountsExist once any account exists.
func (s *Store) CreateFirstAdmin(ctx context.Context, input NewAccount) (*core.Account, error) {
	input.Role = core.AccountRoleAdmin
	return s.createAccount(ctx, input, true)
}

func (s *Store) createAccount(ctx context.Context, input NewAccount, first bool) (*core.Account, error) {
	login := core.NormalizeLogin(input.Login)
	if err := core.ValidateLogin(login); err != nil {
		return nil, err
	}
	passwordHash := ""
	if input.Password != "" {
		if err := core.ValidatePassword(input.Password); err != nil {
			return nil, err
		}
		hash, err := HashPassword(input.Password)
		if err != nil {
			return nil, err
		}
		passwordHash = hash
	}
	now := time.Now().UTC()
	account := &core.Account{
		ID: newTenancyID("acct"), Login: login, DisplayName: strings.TrimSpace(input.DisplayName),
		Role: core.NormalizeAccountRole(input.Role), Status: core.AccountStatusActive,
		TenantID: newTenancyID("ten"), HasPassword: passwordHash != "", CreatedAt: now, UpdatedAt: now,
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if first && s.dialect == DialectPostgres {
		// Two first-run requests must not both become the first administrator.
		if _, err := tx.ExecContext(ctx, `LOCK TABLE accounts IN SHARE ROW EXCLUSIVE MODE`); err != nil {
			return nil, err
		}
	}
	if first {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts`).Scan(&count); err != nil {
			return nil, err
		}
		if count > 0 {
			return nil, ErrAccountsExist
		}
	}
	var taken int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM accounts WHERE login=?)+(SELECT COUNT(*) FROM tenants WHERE name=?)`,
		login, login).Scan(&taken); err != nil {
		return nil, err
	}
	if taken > 0 {
		return nil, ErrLoginTaken
	}
	stamp := now.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO tenants (id,name,kind,status,note,created_at,updated_at) VALUES (?,?,?,?,?,?,?)`,
		account.TenantID, login, core.TenantKindAccount, core.TenantStatusActive, "Namespace of account "+login, stamp, stamp); err != nil {
		return nil, err
	}
	var passwordValue, passwordChanged any
	if passwordHash != "" {
		passwordValue, passwordChanged = passwordHash, stamp
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO accounts (id,login,display_name,role,status,tenant_id,password_hash,password_changed_at,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		account.ID, account.Login, account.DisplayName, account.Role, account.Status, account.TenantID,
		passwordValue, passwordChanged, stamp, stamp); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return account, nil
}

// ListAccounts returns every account, oldest first.
func (s *Store) ListAccounts(ctx context.Context) ([]core.Account, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+accountColumns+` FROM accounts ORDER BY created_at, login`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Account
	for rows.Next() {
		account, _, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, account)
	}
	return out, rows.Err()
}

// GetAccount returns one account or (nil, nil).
func (s *Store) GetAccount(ctx context.Context, id string) (*core.Account, error) {
	return s.getAccount(ctx, `id=?`, id)
}

// GetAccountByLogin returns one account by login or (nil, nil).
func (s *Store) GetAccountByLogin(ctx context.Context, login string) (*core.Account, error) {
	return s.getAccount(ctx, `login=?`, core.NormalizeLogin(login))
}

func (s *Store) getAccount(ctx context.Context, where string, arg string) (*core.Account, error) {
	account, _, err := scanAccount(s.db.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM accounts WHERE `+where, arg))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &account, nil
}

// UpdateAccount changes an account's display name, role or status. Disabling
// an account also disables its namespace and ends its sessions. The last
// active administrator can be neither demoted nor disabled.
func (s *Store) UpdateAccount(ctx context.Context, id string, update AccountUpdate) (*core.Account, error) {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if s.dialect == DialectPostgres {
		if _, err := tx.ExecContext(ctx, `LOCK TABLE accounts IN SHARE ROW EXCLUSIVE MODE`); err != nil {
			return nil, err
		}
	}
	account, _, err := scanAccount(tx.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM accounts WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return nil, ErrAccountNotFound
	}
	if err != nil {
		return nil, err
	}
	next := account
	if update.DisplayName != nil {
		next.DisplayName = strings.TrimSpace(*update.DisplayName)
	}
	if update.Role != nil {
		next.Role = core.NormalizeAccountRole(*update.Role)
	}
	if update.Status != nil {
		if *update.Status == core.AccountStatusDisabled {
			next.Status = core.AccountStatusDisabled
		} else {
			next.Status = core.AccountStatusActive
		}
	}
	wasActiveAdmin := account.IsAdmin() && account.Active()
	if wasActiveAdmin && !(next.IsAdmin() && next.Active()) {
		var others int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE role=? AND status=? AND id<>?`,
			core.AccountRoleAdmin, core.AccountStatusActive, id).Scan(&others); err != nil {
			return nil, err
		}
		if others == 0 {
			return nil, ErrLastAdmin
		}
	}
	next.UpdatedAt = time.Now().UTC()
	stamp := next.UpdatedAt.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET display_name=?,role=?,status=?,updated_at=? WHERE id=?`,
		next.DisplayName, next.Role, next.Status, stamp, id); err != nil {
		return nil, err
	}
	if next.Status != account.Status {
		tenantStatus := core.TenantStatusActive
		if next.Status == core.AccountStatusDisabled {
			tenantStatus = core.TenantStatusDisabled
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tenants SET status=?,updated_at=? WHERE id=?`, tenantStatus, stamp, account.TenantID); err != nil {
			return nil, err
		}
	}
	if next.Status == core.AccountStatusDisabled {
		if _, err := tx.ExecContext(ctx, `DELETE FROM account_sessions WHERE account_id=?`, id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &next, nil
}

// SetAccountPassword replaces an account's password and ends its sessions,
// so a reset locks out whoever knew the old one. keepSession, when not empty,
// is a session token that stays signed in: the person changing their own
// password.
func (s *Store) SetAccountPassword(ctx context.Context, id, password, keepSession string) error {
	if err := core.ValidatePassword(password); err != nil {
		return err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE accounts SET password_hash=?,password_changed_at=?,updated_at=? WHERE id=?`, hash, stamp, stamp, id)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err == nil && count == 0 {
		return ErrAccountNotFound
	}
	if keepSession != "" {
		_, err = tx.ExecContext(ctx, `DELETE FROM account_sessions WHERE account_id=? AND token_hash<>?`, id, HashSecret(keepSession))
	} else {
		_, err = tx.ExecContext(ctx, `DELETE FROM account_sessions WHERE account_id=?`, id)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// VerifyAccountPassword checks a login and password. It returns (nil, nil)
// for an unknown login, a disabled account, an account without a password
// and a wrong password alike, and takes about as long in each case.
func (s *Store) VerifyAccountPassword(ctx context.Context, login, password string) (*core.Account, error) {
	account, hash, err := scanAccount(s.db.QueryRowContext(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE login=?`, core.NormalizeLogin(login)))
	if err == sql.ErrNoRows || (err == nil && hash == "") {
		VerifyPassword(dummyPasswordHash(), password)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !VerifyPassword(hash, password) || !account.Active() {
		return nil, nil
	}
	return &account, nil
}

// AccountSession is one signed-in browser.
type AccountSession struct {
	ID        string
	AccountID string
	Method    string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// CreateAccountSession starts a session for an account and returns its
// plaintext token, which is never stored. method records how the person
// proved who they are.
func (s *Store) CreateAccountSession(ctx context.Context, accountID, method, userAgent, remoteAddr string, ttl time.Duration) (string, *AccountSession, error) {
	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	if _, err := s.writer.ExecContext(ctx, `DELETE FROM account_sessions WHERE expires_at<?`, stamp); err != nil {
		return "", nil, err
	}
	token := generateSecret(AccountSessionTokenPrefix)
	session := &AccountSession{ID: newTenancyID("sess"), AccountID: accountID, Method: method, CreatedAt: now, ExpiresAt: now.Add(ttl)}
	if len(userAgent) > 256 {
		userAgent = userAgent[:256]
	}
	if _, err := s.writer.ExecContext(ctx, `INSERT INTO account_sessions (id,account_id,token_hash,method,created_at,expires_at,last_seen_at,user_agent,remote_addr)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		session.ID, accountID, HashSecret(token), method, stamp, session.ExpiresAt.Format(time.RFC3339Nano), stamp, userAgent, remoteAddr); err != nil {
		return "", nil, err
	}
	if _, err := s.writer.ExecContext(ctx, `UPDATE accounts SET last_sign_in_at=? WHERE id=?`, stamp, accountID); err != nil {
		return "", nil, err
	}
	return token, session, nil
}

// AuthenticateAccountSession resolves a session token to its account. It
// returns (nil, nil) for an unknown or expired session and for a disabled
// account, so the account's current role and status apply to every request.
func (s *Store) AuthenticateAccountSession(ctx context.Context, token string) (*core.Account, error) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, AccountSessionTokenPrefix) {
		return nil, nil
	}
	var sessionID, expires string
	var lastSeen sql.NullString
	var accountID string
	err := s.db.QueryRowContext(ctx, `SELECT id,account_id,expires_at,last_seen_at FROM account_sessions WHERE token_hash=?`,
		HashSecret(token)).Scan(&sessionID, &accountID, &expires, &lastSeen)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	expiresAt, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil || !now.Before(expiresAt) {
		return nil, nil
	}
	account, err := s.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if !account.Active() {
		return nil, nil
	}
	if seen := nullableTime(lastSeen); seen == nil || now.Sub(*seen) >= accountSessionSeenWindow {
		_, _ = s.writer.ExecContext(ctx, `UPDATE account_sessions SET last_seen_at=? WHERE id=?`, now.Format(time.RFC3339Nano), sessionID)
	}
	return account, nil
}

// DeleteAccountSession ends one session by its token.
func (s *Store) DeleteAccountSession(ctx context.Context, token string) error {
	_, err := s.writer.ExecContext(ctx, `DELETE FROM account_sessions WHERE token_hash=?`, HashSecret(strings.TrimSpace(token)))
	return err
}

// DeleteAccountSessions ends every session of one account.
func (s *Store) DeleteAccountSessions(ctx context.Context, accountID string) error {
	_, err := s.writer.ExecContext(ctx, `DELETE FROM account_sessions WHERE account_id=?`, accountID)
	return err
}

// AccountForTenant returns the account whose namespace a tenant is, or
// (nil, nil) for a tenant that is a host application.
func (s *Store) AccountForTenant(ctx context.Context, tenantID string) (*core.Account, error) {
	return s.getAccount(ctx, `tenant_id=?`, tenantID)
}

func scanAccount(sc scanner) (core.Account, string, error) {
	var account core.Account
	var displayName, hash, created, updated, lastSignIn sql.NullString
	if err := sc.Scan(&account.ID, &account.Login, &displayName, &account.Role, &account.Status, &account.TenantID,
		&hash, &created, &updated, &lastSignIn); err != nil {
		return core.Account{}, "", err
	}
	account.DisplayName = displayName.String
	account.HasPassword = hash.String != ""
	account.CreatedAt, _ = time.Parse(time.RFC3339Nano, created.String)
	account.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated.String)
	account.LastSignInAt = nullableTime(lastSignIn)
	return account, hash.String, nil
}

// nullableString stores an empty string as NULL.
func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
