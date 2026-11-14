package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// Devices are the nodes paired with this hub. A device's token and a pairing
// code are both stored as SHA-256 hashes, so a copy of the database cannot
// connect as a device or pair a new one.

const deviceSchema = `
CREATE TABLE IF NOT EXISTS devices (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	hostname TEXT,
	os TEXT,
	arch TEXT,
	version TEXT,
	token_hash TEXT NOT NULL,
	token_prefix TEXT,
	created_at TEXT NOT NULL,
	last_seen_at TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_devices_token ON devices(token_hash);
CREATE TABLE IF NOT EXISTS device_pairing_codes (
	id TEXT PRIMARY KEY,
	code_hash TEXT NOT NULL,
	name TEXT,
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	used_at TEXT,
	device_id TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_device_pairing_codes_hash ON device_pairing_codes(code_hash);
`

const (
	deviceTokenPrefix = "amxd_"
	deviceColumns     = `id,name,hostname,os,arch,version,token_prefix,created_at,last_seen_at,owner_account_id`
	// DevicePairingCodeTTL is how long a pairing code can be used.
	DevicePairingCodeTTL = 30 * time.Minute
	pairingCodeLength    = 8
	// The alphabet leaves out the characters people confuse when retyping.
	pairingCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
)

func newPairingCode() string {
	buf := make([]byte, pairingCodeLength)
	if _, err := rand.Read(buf); err != nil {
		panic("store: crypto/rand unavailable: " + err.Error())
	}
	for index, value := range buf {
		// 32 symbols divide 256 evenly, so the code is uniform.
		buf[index] = pairingCodeAlphabet[int(value)%len(pairingCodeAlphabet)]
	}
	return string(buf)
}

// normalizePairingCode accepts a code however it was retyped.
func normalizePairingCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	return strings.NewReplacer("-", "", " ", "").Replace(code)
}

// CreateDevicePairingCode mints a one-time code. The returned value carries
// the plaintext in Code; it cannot be recovered later.
func (s *Store) CreateDevicePairingCode(ctx context.Context, name string) (*core.DevicePairingCode, error) {
	return s.CreateOwnedDevicePairingCode(ctx, name, "")
}

// CreateOwnedDevicePairingCode mints a one-time code whose device will be
// owned by ownerAccountID; empty leaves the device unassigned.
func (s *Store) CreateOwnedDevicePairingCode(ctx context.Context, name, ownerAccountID string) (*core.DevicePairingCode, error) {
	now := time.Now().UTC()
	// Codes nobody used are of no further interest once they expire.
	if _, err := s.writer.ExecContext(ctx, `DELETE FROM device_pairing_codes WHERE created_at<?`,
		now.Add(-24*time.Hour).Format(time.RFC3339Nano)); err != nil {
		return nil, err
	}
	code := &core.DevicePairingCode{
		ID: newRecordID("pair"), Name: strings.TrimSpace(name), Code: newPairingCode(),
		CreatedAt: now, ExpiresAt: now.Add(DevicePairingCodeTTL),
	}
	_, err := s.writer.ExecContext(ctx, `INSERT INTO device_pairing_codes (id,code_hash,name,created_at,expires_at,owner_account_id) VALUES (?,?,?,?,?,?)`,
		code.ID, HashSecret(code.Code), code.Name, now.Format(time.RFC3339Nano), code.ExpiresAt.Format(time.RFC3339Nano), nullableString(ownerAccountID))
	if err != nil {
		return nil, err
	}
	return code, nil
}

// RedeemDevicePairingCode pairs one device with a valid, unused code and
// returns it with its token. A wrong, used or expired code returns
// (nil, "", nil), so a caller cannot tell those apart from the outside.
func (s *Store) RedeemDevicePairingCode(ctx context.Context, code string, info core.DeviceInfo) (*core.Device, string, error) {
	code = normalizePairingCode(code)
	if code == "" {
		return nil, "", nil
	}
	var id, expires string
	var name, used, owner sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id,name,expires_at,used_at,owner_account_id FROM device_pairing_codes WHERE code_hash=?`, HashSecret(code)).
		Scan(&id, &name, &expires, &used, &owner)
	if err == sql.ErrNoRows {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	now := time.Now().UTC()
	expiresAt, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil || used.String != "" || !now.Before(expiresAt) {
		return nil, "", nil
	}
	token := generateSecret(deviceTokenPrefix)
	device := &core.Device{
		ID: newRecordID("dev"), Name: firstNonEmpty(strings.TrimSpace(name.String), info.Name, info.Hostname, "device"),
		Hostname: info.Hostname, OS: info.OS, Arch: info.Arch, Version: info.Version,
		TokenPrefix: secretDisplayPrefix(token), CreatedAt: now, OwnerAccountID: owner.String,
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = tx.Rollback() }()
	// The unused check is repeated in the write, so two nodes racing with one
	// code pair exactly one device.
	claimed, err := tx.ExecContext(ctx, `UPDATE device_pairing_codes SET used_at=?,device_id=? WHERE id=? AND used_at IS NULL`,
		now.Format(time.RFC3339Nano), device.ID, id)
	if err != nil {
		return nil, "", err
	}
	if count, err := claimed.RowsAffected(); err != nil || count != 1 {
		return nil, "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO devices (id,name,hostname,os,arch,version,token_hash,token_prefix,created_at,owner_account_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		device.ID, device.Name, device.Hostname, device.OS, device.Arch, device.Version,
		HashSecret(token), device.TokenPrefix, now.Format(time.RFC3339Nano), nullableString(device.OwnerAccountID)); err != nil {
		return nil, "", err
	}
	if err := tx.Commit(); err != nil {
		return nil, "", err
	}
	return device, token, nil
}

// AuthenticateDevice resolves a device token, or returns (nil, nil).
func (s *Store) AuthenticateDevice(ctx context.Context, token string) (*core.Device, error) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, deviceTokenPrefix) {
		return nil, nil
	}
	device, err := scanDevice(s.db.QueryRowContext(ctx, `SELECT `+deviceColumns+` FROM devices WHERE token_hash=?`, HashSecret(token)))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &device, nil
}

// ListDevices returns the paired devices, oldest first.
func (s *Store) ListDevices(ctx context.Context) ([]core.Device, error) {
	return s.listDevices(ctx, `SELECT `+deviceColumns+` FROM devices ORDER BY created_at, id`)
}

// ListDevicesForAccount returns the devices one account owns, oldest first.
func (s *Store) ListDevicesForAccount(ctx context.Context, accountID string) ([]core.Device, error) {
	return s.listDevices(ctx, `SELECT `+deviceColumns+` FROM devices WHERE owner_account_id=? ORDER BY created_at, id`, accountID)
}

func (s *Store) listDevices(ctx context.Context, query string, args ...any) ([]core.Device, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Device
	for rows.Next() {
		device, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, device)
	}
	return out, rows.Err()
}

// GetDevice returns one device, or (nil, nil) when it is not paired.
func (s *Store) GetDevice(ctx context.Context, id string) (*core.Device, error) {
	device, err := scanDevice(s.db.QueryRowContext(ctx, `SELECT `+deviceColumns+` FROM devices WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &device, nil
}

// SetDeviceOwner hands a device to an account; empty returns it to
// administrators only.
func (s *Store) SetDeviceOwner(ctx context.Context, id, ownerAccountID string) error {
	_, err := s.writer.ExecContext(ctx, `UPDATE devices SET owner_account_id=? WHERE id=?`, nullableString(ownerAccountID), id)
	return err
}

// RenameDevice changes the hub's label for a device.
func (s *Store) RenameDevice(ctx context.Context, id, name string) error {
	_, err := s.writer.ExecContext(ctx, `UPDATE devices SET name=? WHERE id=?`, strings.TrimSpace(name), id)
	return err
}

// TouchDevice records that a device was connected at seen. A zero info leaves
// what the device said about itself unchanged.
func (s *Store) TouchDevice(ctx context.Context, id string, info core.DeviceInfo, seen time.Time) error {
	at := seen.UTC().Format(time.RFC3339Nano)
	if info == (core.DeviceInfo{}) {
		_, err := s.writer.ExecContext(ctx, `UPDATE devices SET last_seen_at=? WHERE id=?`, at, id)
		return err
	}
	_, err := s.writer.ExecContext(ctx, `UPDATE devices SET last_seen_at=?,hostname=?,os=?,arch=?,version=? WHERE id=?`,
		at, info.Hostname, info.OS, info.Arch, info.Version, id)
	return err
}

// DeleteDevice unpairs a device. Its token stops working at once, and the
// hub's copy of what it reported goes with it.
func (s *Store) DeleteDevice(ctx context.Context, id string) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, query := range []string{
		`DELETE FROM device_resources WHERE device_id=?`,
		`DELETE FROM device_collections WHERE device_id=?`,
		`DELETE FROM devices WHERE id=?`,
	} {
		if _, err := tx.ExecContext(ctx, query, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func scanDevice(sc scanner) (core.Device, error) {
	var device core.Device
	var hostname, os, arch, version, prefix, lastSeen, owner sql.NullString
	var created string
	if err := sc.Scan(&device.ID, &device.Name, &hostname, &os, &arch, &version, &prefix, &created, &lastSeen, &owner); err != nil {
		return core.Device{}, err
	}
	device.Hostname, device.OS, device.Arch, device.Version, device.TokenPrefix = hostname.String, os.String, arch.String, version.String, prefix.String
	device.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	device.LastSeenAt = nullableTime(lastSeen)
	device.OwnerAccountID = owner.String
	return device, nil
}
