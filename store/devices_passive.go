package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// Devices the hub dials (passive link). Such a device is an ordinary row in
// devices, plus one row here saying where it listens, which key it must
// present and which credential the hub presents. The hub has to present the
// credential, so it is kept as it is, unlike a device's token; it is hub
// data and travels to a standby hub with the rest.
//
// A passive device has no token of its own to dial the hub with: its
// devices.token_hash is the hash of a random value nobody keeps, so the
// outbound connect endpoint never accepts it.

const devicePassiveLinkSchema = `
CREATE TABLE IF NOT EXISTS device_passive_links (
	device_id TEXT PRIMARY KEY,
	addrs TEXT NOT NULL,
	fingerprint TEXT NOT NULL,
	credential TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
`

// ErrMachinePairedOutbound means the machine already dials this hub as a
// device of its own; a machine is paired one way at a time.
var ErrMachinePairedOutbound = errors.New("this machine is already paired with the hub and dials it; a machine is paired one way at a time: remove that device on the hub first")

const passiveLinkColumns = `device_id,addrs,fingerprint,credential,created_at,updated_at`

// SavePassiveDevice records a device the hub dials. When a device with the
// same machine UID is already passive, its link is replaced (a new key,
// credential or address, as after the node was set up again) and it keeps its
// id; when that device dials the hub instead, ErrMachinePairedOutbound is
// returned. replaced says which happened.
func (s *Store) SavePassiveDevice(ctx context.Context, name, ownerAccountID string, info core.DeviceInfo, link core.PassiveLink) (device *core.Device, replaced bool, err error) {
	addrs, err := json.Marshal(link.Addrs)
	if err != nil {
		return nil, false, err
	}
	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if info.MachineUID != "" {
		existing, outbound, err := passiveMachineDevices(ctx, tx, info.MachineUID)
		if err != nil {
			return nil, false, err
		}
		if outbound {
			return nil, false, ErrMachinePairedOutbound
		}
		if existing != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE device_passive_links SET addrs=?,fingerprint=?,credential=?,updated_at=? WHERE device_id=?`,
				string(addrs), link.Fingerprint, link.Credential, stamp, existing); err != nil {
				return nil, false, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE devices SET hostname=?,os=?,arch=?,version=? WHERE id=?`,
				info.Hostname, info.OS, info.Arch, info.Version, existing); err != nil {
				return nil, false, err
			}
			if err := tx.Commit(); err != nil {
				return nil, false, err
			}
			device, err := s.GetDevice(ctx, existing)
			return device, true, err
		}
	}
	device = &core.Device{
		ID: newRecordID("dev"), Name: firstNonEmpty(strings.TrimSpace(name), info.Name, info.Hostname, "device"),
		Hostname: info.Hostname, OS: info.OS, Arch: info.Arch, Version: info.Version, MachineUID: info.MachineUID,
		TokenPrefix: "passive", CreatedAt: now, OwnerAccountID: ownerAccountID,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO devices (id,name,hostname,os,arch,version,token_hash,token_prefix,created_at,owner_account_id,machine_uid) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		device.ID, device.Name, device.Hostname, device.OS, device.Arch, device.Version,
		HashSecret(generateSecret("unused_")), device.TokenPrefix, stamp, nullableString(device.OwnerAccountID),
		nullableString(device.MachineUID)); err != nil {
		return nil, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO device_passive_links (`+passiveLinkColumns+`) VALUES (?,?,?,?,?,?)`,
		device.ID, string(addrs), link.Fingerprint, link.Credential, stamp, stamp); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return device, false, nil
}

// passiveMachineDevices looks at the devices that are this machine: the
// first one the hub dials, and whether any dials the hub instead.
func passiveMachineDevices(ctx context.Context, tx *dbTx, machineUID string) (passive string, outbound bool, err error) {
	rows, err := tx.QueryContext(ctx, `SELECT d.id, p.device_id FROM devices d LEFT JOIN device_passive_links p ON p.device_id = d.id
		WHERE d.machine_uid=? ORDER BY d.created_at, d.id`, machineUID)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var link sql.NullString
		if err := rows.Scan(&id, &link); err != nil {
			return "", false, err
		}
		if !link.Valid {
			outbound = true
		} else if passive == "" {
			passive = id
		}
	}
	return passive, outbound, rows.Err()
}

// PassiveLinks lists every device the hub dials.
func (s *Store) PassiveLinks(ctx context.Context) ([]core.PassiveLink, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+passiveLinkColumns+` FROM device_passive_links ORDER BY created_at, device_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.PassiveLink
	for rows.Next() {
		link, err := scanPassiveLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, link)
	}
	return out, rows.Err()
}

// PassiveLink returns how the hub dials one device, or (nil, nil) when the
// device dials the hub instead.
func (s *Store) PassiveLink(ctx context.Context, deviceID string) (*core.PassiveLink, error) {
	link, err := scanPassiveLink(s.db.QueryRowContext(ctx, `SELECT `+passiveLinkColumns+` FROM device_passive_links WHERE device_id=?`, deviceID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &link, nil
}

// PassiveDeviceForMachine returns the device the hub dials that is this
// machine, or "".
func (s *Store) PassiveDeviceForMachine(ctx context.Context, machineUID string) (string, error) {
	machineUID = core.NormalizeMachineUID(machineUID)
	if machineUID == "" {
		return "", nil
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT d.id FROM devices d JOIN device_passive_links p ON p.device_id = d.id WHERE d.machine_uid=? ORDER BY d.created_at LIMIT 1`, machineUID).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}

func scanPassiveLink(sc scanner) (core.PassiveLink, error) {
	var link core.PassiveLink
	var addrs, created, updated string
	if err := sc.Scan(&link.DeviceID, &addrs, &link.Fingerprint, &link.Credential, &created, &updated); err != nil {
		return core.PassiveLink{}, err
	}
	_ = json.Unmarshal([]byte(addrs), &link.Addrs)
	link.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	link.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return link, nil
}
