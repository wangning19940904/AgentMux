package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// Tenant credentials are stored as SHA-256 hashes: the plaintext is shown once
// at creation, so a database copy cannot be replayed against the API.

const (
	tenantTokenPrefix     = "amxt_"
	tenantSecretBytes     = 32
	tenantColumns         = `id,name,kind,status,note,created_at,updated_at`
	tenantTokenColumns    = `id,tenant_id,name,token_hash,prefix,created_at,last_used_at,expires_at,revoked_at`
	resourceGrantColumns  = `tenant_id,resource_type,resource_id,level,created_at,updated_at`
	tenantDisplayedPrefix = 12
	tenantLastUsedWindow  = 5 * time.Minute
)

// HashSecret is the one-way transform applied to tenant tokens before they
// touch the database.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func generateSecret(prefix string) string {
	buf := make([]byte, tenantSecretBytes)
	if _, err := rand.Read(buf); err != nil {
		panic("store: crypto/rand unavailable: " + err.Error())
	}
	return prefix + hex.EncodeToString(buf)
}

func secretDisplayPrefix(secret string) string {
	if len(secret) <= tenantDisplayedPrefix {
		return secret
	}
	return secret[:tenantDisplayedPrefix]
}

func nullableTime(value sql.NullString) *time.Time {
	if !value.Valid || value.String == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil
	}
	return &parsed
}

func formatTimePtr(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return value.Format(time.RFC3339Nano)
}

// ---------------------------------------------------------------- tenants

// ListTenants returns every registered tenant, newest first.
func (s *Store) ListTenants(ctx context.Context) ([]core.Tenant, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+tenantColumns+` FROM tenants ORDER BY created_at DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Tenant
	for rows.Next() {
		tenant, err := scanTenant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, tenant)
	}
	return out, rows.Err()
}

// GetTenant returns one tenant or (nil,nil) if absent.
func (s *Store) GetTenant(ctx context.Context, id string) (*core.Tenant, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+tenantColumns+` FROM tenants WHERE id=?`, id)
	tenant, err := scanTenant(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &tenant, nil
}

// GetTenantByName resolves a tenant by its unique name.
func (s *Store) GetTenantByName(ctx context.Context, name string) (*core.Tenant, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+tenantColumns+` FROM tenants WHERE name=?`, name)
	tenant, err := scanTenant(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &tenant, nil
}

// UpsertTenant inserts or updates a tenant.
func (s *Store) UpsertTenant(ctx context.Context, tenant *core.Tenant) error {
	_, err := s.writer.ExecContext(ctx, `INSERT INTO tenants
		(id,name,kind,status,note,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name,kind=excluded.kind,
		status=excluded.status,note=excluded.note,updated_at=excluded.updated_at`,
		tenant.ID, tenant.Name, tenant.Kind, tenant.Status, tenant.Note,
		tenant.CreatedAt.Format(time.RFC3339Nano), tenant.UpdatedAt.Format(time.RFC3339Nano))
	return err
}

// DeleteTenant removes a tenant along with its credentials and grants. The
// resources it owned are left in place and become unassigned, which keeps them
// visible to the admin instead of silently disappearing.
func (s *Store) DeleteTenant(ctx context.Context, id string) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		`DELETE FROM tenant_tokens WHERE tenant_id=?`,
		`DELETE FROM resource_grants WHERE tenant_id=?`,
		`DELETE FROM tenants WHERE id=?`,
	} {
		if _, err := tx.ExecContext(ctx, statement, id); err != nil {
			return err
		}
	}
	for _, statement := range []string{
		`UPDATE agent_instances SET owner_tenant_id=NULL WHERE owner_tenant_id=?`,
		`UPDATE channels SET owner_tenant_id=NULL WHERE owner_tenant_id=?`,
		`UPDATE triggers SET owner_tenant_id=NULL WHERE owner_tenant_id=?`,
		`UPDATE orchestrations SET owner_tenant_id=NULL WHERE owner_tenant_id=?`,
	} {
		if _, err := tx.ExecContext(ctx, statement, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func scanTenant(sc scanner) (core.Tenant, error) {
	var tenant core.Tenant
	var kind, note, created, updated sql.NullString
	if err := sc.Scan(&tenant.ID, &tenant.Name, &kind, &tenant.Status, &note, &created, &updated); err != nil {
		return tenant, err
	}
	tenant.Kind = kind.String
	tenant.Note = note.String
	tenant.CreatedAt, _ = time.Parse(time.RFC3339Nano, created.String)
	tenant.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated.String)
	return tenant, nil
}

// ---------------------------------------------------------- tenant tokens

// CreateTenantToken mints a credential for a tenant. The returned token
// carries the plaintext secret in Secret; it cannot be recovered later.
func (s *Store) CreateTenantToken(ctx context.Context, tenantID, name string, expiresAt *time.Time) (*core.TenantToken, error) {
	secret := generateSecret(tenantTokenPrefix)
	now := time.Now().UTC()
	token := &core.TenantToken{
		ID:        newTenancyID("tok"),
		TenantID:  tenantID,
		Name:      name,
		Prefix:    secretDisplayPrefix(secret),
		CreatedAt: now,
		ExpiresAt: expiresAt,
		Secret:    secret,
	}
	_, err := s.writer.ExecContext(ctx, `INSERT INTO tenant_tokens
		(id,tenant_id,name,token_hash,prefix,created_at,last_used_at,expires_at,revoked_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		token.ID, token.TenantID, token.Name, HashSecret(secret), token.Prefix,
		now.Format(time.RFC3339Nano), nil, formatTimePtr(expiresAt), nil)
	if err != nil {
		return nil, err
	}
	return token, nil
}

// ListTenantTokens returns a tenant's credentials without their secrets.
func (s *Store) ListTenantTokens(ctx context.Context, tenantID string) ([]core.TenantToken, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+tenantTokenColumns+` FROM tenant_tokens WHERE tenant_id=? ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.TenantToken
	for rows.Next() {
		token, err := scanTenantToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, token)
	}
	return out, rows.Err()
}

// RevokeTenantToken marks a credential unusable from now on.
func (s *Store) RevokeTenantToken(ctx context.Context, id string) error {
	_, err := s.writer.ExecContext(ctx,
		`UPDATE tenant_tokens SET revoked_at=? WHERE id=? AND revoked_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339Nano), id)
	return err
}

// AuthenticateTenantToken resolves a plaintext token to its tenant. It returns
// (nil,nil) when the token is unknown, revoked, expired, or its tenant is
// disabled, so callers cannot distinguish those cases from the outside.
func (s *Store) AuthenticateTenantToken(ctx context.Context, secret string) (*core.Tenant, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" || !strings.HasPrefix(secret, tenantTokenPrefix) {
		return nil, nil
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT `+tenantTokenColumns+` FROM tenant_tokens WHERE token_hash=?`, HashSecret(secret))
	token, err := scanTenantToken(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if token.RevokedAt != nil {
		return nil, nil
	}
	if token.ExpiresAt != nil && now.After(*token.ExpiresAt) {
		return nil, nil
	}
	tenant, err := s.GetTenant(ctx, token.TenantID)
	if err != nil {
		return nil, err
	}
	if !tenant.Active() {
		return nil, nil
	}
	// Last-used tracking is advisory. Avoid turning every authenticated request
	// into a writer transaction; one durable update per five-minute window is
	// enough for operator-facing activity metadata.
	if token.LastUsedAt == nil || now.Sub(*token.LastUsedAt) >= tenantLastUsedWindow {
		cutoff := now.Add(-tenantLastUsedWindow).Format(time.RFC3339Nano)
		_, _ = s.writer.ExecContext(ctx, `UPDATE tenant_tokens SET last_used_at=?
			WHERE id=? AND (last_used_at IS NULL OR last_used_at<?)`,
			now.Format(time.RFC3339Nano), token.ID, cutoff)
	}
	return tenant, nil
}

func scanTenantToken(sc scanner) (core.TenantToken, error) {
	var token core.TenantToken
	var name, hash, prefix, created sql.NullString
	var lastUsed, expires, revoked sql.NullString
	if err := sc.Scan(&token.ID, &token.TenantID, &name, &hash, &prefix, &created,
		&lastUsed, &expires, &revoked); err != nil {
		return token, err
	}
	token.Name = name.String
	token.Prefix = prefix.String
	token.CreatedAt, _ = time.Parse(time.RFC3339Nano, created.String)
	token.LastUsedAt = nullableTime(lastUsed)
	token.ExpiresAt = nullableTime(expires)
	token.RevokedAt = nullableTime(revoked)
	return token, nil
}

// -------------------------------------------------------- resource grants

// UpsertResourceGrant grants or re-levels a tenant's access to one resource.
func (s *Store) UpsertResourceGrant(ctx context.Context, grant *core.ResourceGrant) error {
	now := time.Now().UTC()
	if grant.CreatedAt.IsZero() {
		grant.CreatedAt = now
	}
	grant.UpdatedAt = now
	grant.Level = core.NormalizeGrantLevel(grant.Level)
	_, err := s.writer.ExecContext(ctx, `INSERT INTO resource_grants
		(tenant_id,resource_type,resource_id,level,created_at,updated_at)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(tenant_id,resource_type,resource_id) DO UPDATE SET
		level=excluded.level,updated_at=excluded.updated_at`,
		grant.TenantID, grant.ResourceType, grant.ResourceID, grant.Level,
		grant.CreatedAt.Format(time.RFC3339Nano), grant.UpdatedAt.Format(time.RFC3339Nano))
	return err
}

// DeleteResourceGrant revokes one grant.
func (s *Store) DeleteResourceGrant(ctx context.Context, tenantID, resourceType, resourceID string) error {
	_, err := s.writer.ExecContext(ctx,
		`DELETE FROM resource_grants WHERE tenant_id=? AND resource_type=? AND resource_id=?`,
		tenantID, resourceType, resourceID)
	return err
}

// ListResourceGrants returns grants, optionally narrowed to one tenant.
func (s *Store) ListResourceGrants(ctx context.Context, tenantID string) ([]core.ResourceGrant, error) {
	query := `SELECT ` + resourceGrantColumns + ` FROM resource_grants`
	args := []any{}
	if tenantID != "" {
		query += ` WHERE tenant_id=?`
		args = append(args, tenantID)
	}
	query += ` ORDER BY resource_type, resource_id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.ResourceGrant
	for rows.Next() {
		var grant core.ResourceGrant
		var created, updated sql.NullString
		if err := rows.Scan(&grant.TenantID, &grant.ResourceType, &grant.ResourceID,
			&grant.Level, &created, &updated); err != nil {
			return nil, err
		}
		grant.CreatedAt, _ = time.Parse(time.RFC3339Nano, created.String)
		grant.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated.String)
		out = append(out, grant)
	}
	return out, rows.Err()
}

// GrantLevelFor returns the level a tenant holds on one resource, or "" when
// no grant exists.
func (s *Store) GrantLevelFor(ctx context.Context, tenantID, resourceType, resourceID string) (string, error) {
	var level sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT level FROM resource_grants WHERE tenant_id=? AND resource_type=? AND resource_id=?`,
		tenantID, resourceType, resourceID).Scan(&level)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return level.String, nil
}

// TenantNames maps tenant IDs to display names so list responses can label
// ownership without an extra round trip per row.
func (s *Store) TenantNames(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name FROM tenants`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

func newTenancyID(kind string) string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		panic("store: crypto/rand unavailable: " + err.Error())
	}
	return kind + "_" + hex.EncodeToString(buf)
}
