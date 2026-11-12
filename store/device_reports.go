package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// Device reports are the hub's copy of what each paired device's own API
// answers, kept so the hub can show a device's data while it is offline and
// answer for every device from one database. Each device still owns its
// data: this copy only ever follows what the device sends.

const deviceReportSchema = `
CREATE TABLE IF NOT EXISTS device_collections (
	device_id TEXT NOT NULL,
	collection TEXT NOT NULL,
	path TEXT NOT NULL,
	shape TEXT NOT NULL,
	item_order TEXT,
	reported_at TEXT NOT NULL,
	PRIMARY KEY(device_id, collection)
);
CREATE INDEX IF NOT EXISTS idx_device_collections_path ON device_collections(device_id, path);
CREATE TABLE IF NOT EXISTS device_resources (
	device_id TEXT NOT NULL,
	collection TEXT NOT NULL,
	id TEXT NOT NULL,
	hash TEXT NOT NULL,
	data TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY(device_id, collection, id)
);
`

// maxDeviceCollectionItems bounds what one device can make the hub keep.
const maxDeviceCollectionItems = 5000

// DeviceReportState returns the hash of each document the hub holds of one
// collection, which is what a device compares its own data against.
func (s *Store) DeviceReportState(ctx context.Context, deviceID, collection string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,hash FROM device_resources WHERE device_id=? AND collection=?`, deviceID, collection)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	state := map[string]string{}
	for rows.Next() {
		var id, hash string
		if err := rows.Scan(&id, &hash); err != nil {
			return nil, err
		}
		state[id] = hash
	}
	return state, rows.Err()
}

// ApplyDeviceReport applies one change to the hub's copy, all or nothing.
func (s *Store) ApplyDeviceReport(ctx context.Context, deviceID string, report core.DeviceReport, at time.Time) error {
	stamp := at.UTC().Format(time.RFC3339Nano)
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// A device removed while its last report was in flight leaves nothing.
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM devices WHERE id=?`, deviceID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return fmt.Errorf("device %s is not paired", deviceID)
	}
	var order any
	if report.Order != nil {
		encoded, err := json.Marshal(report.Order)
		if err != nil {
			return err
		}
		order = string(encoded)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO device_collections (device_id,collection,path,shape,item_order,reported_at) VALUES (?,?,?,?,?,?)
		ON CONFLICT(device_id,collection) DO UPDATE SET path=excluded.path,shape=excluded.shape,
			item_order=COALESCE(excluded.item_order,device_collections.item_order),reported_at=excluded.reported_at`,
		deviceID, report.Collection, report.Path, report.Shape, order, stamp); err != nil {
		return err
	}
	for _, id := range report.Deletes {
		if _, err := tx.ExecContext(ctx, `DELETE FROM device_resources WHERE device_id=? AND collection=? AND id=?`, deviceID, report.Collection, id); err != nil {
			return err
		}
	}
	for _, doc := range report.Upserts {
		// The hub hashes what it stores, so the state it later hands back is
		// about its own copy and not about what a device claimed.
		if _, err := tx.ExecContext(ctx, `INSERT INTO device_resources (device_id,collection,id,hash,data,updated_at) VALUES (?,?,?,?,?,?)
			ON CONFLICT(device_id,collection,id) DO UPDATE SET hash=excluded.hash,data=excluded.data,updated_at=excluded.updated_at`,
			deviceID, report.Collection, doc.ID, core.DeviceReportHash(doc.Data), string(doc.Data), stamp); err != nil {
			return err
		}
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM device_resources WHERE device_id=? AND collection=?`, deviceID, report.Collection).Scan(&count); err != nil {
		return err
	}
	if count > maxDeviceCollectionItems {
		return fmt.Errorf("collection %s would hold %d documents; the limit is %d", report.Collection, count, maxDeviceCollectionItems)
	}
	return tx.Commit()
}

// DeviceReportedResponse rebuilds what the device's API answered at path when
// the device last reported it. ok is false when the device never reported
// that path.
func (s *Store) DeviceReportedResponse(ctx context.Context, deviceID, path string) (payload []byte, reportedAt time.Time, ok bool, err error) {
	var collection, shape, stamp string
	var order sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT collection,shape,item_order,reported_at FROM device_collections WHERE device_id=? AND path=?
		ORDER BY reported_at DESC LIMIT 1`, deviceID, path).Scan(&collection, &shape, &order, &stamp)
	if err == sql.ErrNoRows {
		return nil, time.Time{}, false, nil
	}
	if err != nil {
		return nil, time.Time{}, false, err
	}
	reportedAt, _ = time.Parse(time.RFC3339Nano, stamp)
	rows, err := s.db.QueryContext(ctx, `SELECT id,data FROM device_resources WHERE device_id=? AND collection=? ORDER BY id`, deviceID, collection)
	if err != nil {
		return nil, time.Time{}, false, err
	}
	defer rows.Close()
	docs := map[string]string{}
	var ids []string
	for rows.Next() {
		var id, data string
		if err := rows.Scan(&id, &data); err != nil {
			return nil, time.Time{}, false, err
		}
		docs[id] = data
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, false, err
	}
	if shape == core.DeviceReportValue {
		data, found := docs["_"]
		if !found {
			return nil, time.Time{}, false, nil
		}
		return []byte(data), reportedAt, true, nil
	}
	var ordered []string
	if order.Valid {
		_ = json.Unmarshal([]byte(order.String), &ordered)
	}
	var out bytes.Buffer
	out.WriteByte('[')
	written := map[string]bool{}
	write := func(id string) {
		data, found := docs[id]
		if !found || written[id] {
			return
		}
		if len(written) > 0 {
			out.WriteByte(',')
		}
		out.WriteString(data)
		written[id] = true
	}
	for _, id := range ordered {
		write(id)
	}
	// A document outside the reported order still belongs to the list.
	for _, id := range ids {
		write(id)
	}
	out.WriteByte(']')
	return out.Bytes(), reportedAt, true, nil
}

// ListDeviceCollections summarizes what the hub holds, for every device or
// for one.
func (s *Store) ListDeviceCollections(ctx context.Context, deviceID string) ([]core.DeviceCollection, error) {
	query := `SELECT c.device_id,c.collection,c.path,c.shape,c.reported_at,
		(SELECT COUNT(*) FROM device_resources r WHERE r.device_id=c.device_id AND r.collection=c.collection)
		FROM device_collections c`
	var args []any
	if deviceID != "" {
		query += ` WHERE c.device_id=?`
		args = append(args, deviceID)
	}
	rows, err := s.db.QueryContext(ctx, query+` ORDER BY c.device_id,c.collection`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.DeviceCollection
	for rows.Next() {
		var item core.DeviceCollection
		var stamp string
		if err := rows.Scan(&item.DeviceID, &item.Collection, &item.Path, &item.Shape, &stamp, &item.Items); err != nil {
			return nil, err
		}
		item.ReportedAt, _ = time.Parse(time.RFC3339Nano, stamp)
		out = append(out, item)
	}
	return out, rows.Err()
}
