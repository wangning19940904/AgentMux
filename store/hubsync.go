package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Hub replication.
//
// A standby hub keeps a copy of hub data so it can take over from the
// primary. Hub data is a fixed set of ordinary tables (HubTables); every other
// table is the machine's own data and is never copied. While this machine is
// the primary, a trigger on each hub table appends the change to hub_changes
// in the same transaction, and the standby reads that log through the hub
// service over its link. PostgreSQL's own replication is not used: it would
// need a database connection between the hubs and matching major versions.
//
// The trigger also fences: while hub_replication says this machine is a
// standby, or a primary whose lease lapsed, any write to a hub table fails
// unless it is the follower applying the primary's changes.

// HubTables are the tables that make up hub data. The mailbox tables are not
// among them: a node keeps its own outgoing mail in the same tables, so a
// standby could not write its own mail while they followed the primary.
var HubTables = []string{
	"accounts", "account_sessions",
	"devices", "device_pairing_codes", "device_collections", "device_resources",
	"push_vapid_keys", "push_subscriptions",
}

// HubRole is this machine's part in hub replication.
type HubRole string

const (
	// HubRoleNone is a hub without a standby, or no hub at all: nothing is
	// logged and nothing is fenced.
	HubRoleNone HubRole = "none"
	// HubRolePrimary holds the lease: it writes hub data and logs changes.
	HubRolePrimary HubRole = "primary"
	// HubRoleStandby follows the primary and writes hub data only by applying
	// the primary's changes.
	HubRoleStandby HubRole = "standby"
	// HubRoleFenced was the primary but its lease lapsed. It writes nothing
	// until the lease is renewed or it is told to follow.
	HubRoleFenced HubRole = "fenced"
)

// ErrHubChangesPruned means the changes after a cursor are no longer kept, so
// the standby has to load a snapshot.
var ErrHubChangesPruned = errors.New("the hub no longer keeps changes that old; a snapshot is needed")

const hubChangeLock = 0x616d7568 // "amuh"

const hubReplicationSchema = `
CREATE TABLE IF NOT EXISTS hub_replication (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	role TEXT NOT NULL DEFAULT 'none',
	epoch BIGINT NOT NULL DEFAULT 0,
	applied_seq BIGINT NOT NULL DEFAULT 0,
	pruned_through BIGINT NOT NULL DEFAULT 0,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO hub_replication (id) VALUES (1) ON CONFLICT (id) DO NOTHING;
CREATE TABLE IF NOT EXISTS hub_changes (
	seq BIGSERIAL PRIMARY KEY,
	epoch BIGINT NOT NULL,
	tbl TEXT NOT NULL,
	op TEXT NOT NULL,
	row_data JSONB NOT NULL,
	at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_hub_changes_at ON hub_changes(at);
ALTER TABLE devices ADD COLUMN IF NOT EXISTS hub_role TEXT;
CREATE OR REPLACE FUNCTION agentmux_hub_capture() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
	my_role TEXT;
	my_epoch BIGINT;
BEGIN
	IF current_setting('agentmux.hub_apply', true) = 'on' THEN
		RETURN NULL;
	END IF;
	SELECT role, epoch INTO my_role, my_epoch FROM hub_replication WHERE id = 1;
	IF my_role IS NULL OR my_role = 'none' THEN
		RETURN NULL;
	END IF;
	IF my_role <> 'primary' THEN
		RAISE EXCEPTION 'this machine is a %% hub; hub data can only be changed on the primary hub', my_role
			USING ERRCODE = 'read_only_sql_transaction';
	END IF;
	-- Writers take turns, so sequence order is commit order and a reader
	-- that has seen seq N has seen everything before it.
	PERFORM pg_advisory_xact_lock(%d);
	INSERT INTO hub_changes (epoch, tbl, op, row_data) VALUES (
		my_epoch, TG_TABLE_NAME, TG_OP,
		CASE WHEN TG_OP = 'DELETE' THEN to_jsonb(OLD) ELSE to_jsonb(NEW) END);
	RETURN NULL;
END
$$;
`

func hubTriggerSQL() string {
	var b strings.Builder
	fmt.Fprintf(&b, hubReplicationSchema, hubChangeLock)
	for _, table := range HubTables {
		fmt.Fprintf(&b, "DROP TRIGGER IF EXISTS agentmux_hub_capture ON %s;\n", table)
		fmt.Fprintf(&b, "CREATE TRIGGER agentmux_hub_capture AFTER INSERT OR UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION agentmux_hub_capture();\n", table)
	}
	return b.String()
}

// HubReplicationState is this machine's replication record.
type HubReplicationState struct {
	Role  HubRole `json:"role"`
	Epoch int64   `json:"epoch"`
	// AppliedSeq is the last primary change a standby applied.
	AppliedSeq int64 `json:"applied_seq"`
	// HeadSeq is the last change this machine logged as primary.
	HeadSeq       int64 `json:"head_seq"`
	PrunedThrough int64 `json:"pruned_through"`
	// SchemaVersion is the newest migration applied; hubs only exchange
	// changes when theirs match.
	SchemaVersion int `json:"schema_version"`
}

// HubChange is one logged write to a hub table.
type HubChange struct {
	Seq   int64           `json:"seq"`
	Epoch int64           `json:"epoch"`
	Table string          `json:"table"`
	Op    string          `json:"op"`
	Row   json.RawMessage `json:"row"`
}

// HubSnapshot is every hub table at one point of the change log.
type HubSnapshot struct {
	Seq           int64                        `json:"seq"`
	Epoch         int64                        `json:"epoch"`
	SchemaVersion int                          `json:"schema_version"`
	Tables        map[string][]json.RawMessage `json:"tables"`
}

func (s *Store) requirePostgres() error {
	if !s.IsPostgres() {
		return errors.New("hub replication needs PostgreSQL")
	}
	return nil
}

// HubReplication reads this machine's replication record.
func (s *Store) HubReplication(ctx context.Context) (HubReplicationState, error) {
	if err := s.requirePostgres(); err != nil {
		return HubReplicationState{}, err
	}
	var state HubReplicationState
	var role string
	err := s.writer.DB.QueryRowContext(ctx, `SELECT r.role, r.epoch, r.applied_seq, r.pruned_through,
		COALESCE((SELECT MAX(seq) FROM hub_changes), r.pruned_through),
		COALESCE((SELECT MAX(version) FROM schema_migrations), 0)
		FROM hub_replication r WHERE r.id = 1`).
		Scan(&role, &state.Epoch, &state.AppliedSeq, &state.PrunedThrough, &state.HeadSeq, &state.SchemaVersion)
	state.Role = HubRole(role)
	return state, err
}

// SetHubRole records a new role. Becoming primary continues the change log
// after what this machine applied as a standby, so its sequence numbers stay
// above anything a standby of the previous primary has seen. Becoming a
// standby from any other role forgets the copy's position, so the next sync
// starts from a snapshot and drops whatever this machine wrote that the new
// primary never saw.
func (s *Store) SetHubRole(ctx context.Context, role HubRole, epoch int64) error {
	if err := s.requirePostgres(); err != nil {
		return err
	}
	tx, err := s.writer.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var previous string
	var applied, head int64
	if err := tx.QueryRowContext(ctx, `SELECT role, applied_seq, COALESCE((SELECT MAX(seq) FROM hub_changes), pruned_through)
		FROM hub_replication WHERE id = 1 FOR UPDATE`).Scan(&previous, &applied, &head); err != nil {
		return err
	}
	switch role {
	case HubRolePrimary:
		if HubRole(previous) == HubRoleStandby {
			// The old log belongs to an earlier life as primary; the copy
			// now continues from what was applied.
			if _, err := tx.ExecContext(ctx, `DELETE FROM hub_changes`); err != nil {
				return err
			}
			head = applied
			if _, err := tx.ExecContext(ctx, `UPDATE hub_replication SET pruned_through = $1 WHERE id = 1`, applied); err != nil {
				return err
			}
		}
		if head > 0 {
			if _, err := tx.ExecContext(ctx, `SELECT setval(pg_get_serial_sequence('hub_changes', 'seq'), $1)`, head); err != nil {
				return err
			}
		}
	case HubRoleStandby:
		if HubRole(previous) != HubRoleStandby {
			if _, err := tx.ExecContext(ctx, `UPDATE hub_replication SET applied_seq = 0 WHERE id = 1`); err != nil {
				return err
			}
		}
	case HubRoleFenced, HubRoleNone:
	default:
		return fmt.Errorf("unknown hub role %q", role)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hub_replication SET role = $1, epoch = $2, updated_at = now() WHERE id = 1`, string(role), epoch); err != nil {
		return err
	}
	return tx.Commit()
}

// HubChangesAfter returns up to limit changes after seq, oldest first.
func (s *Store) HubChangesAfter(ctx context.Context, after int64, limit int) ([]HubChange, error) {
	if err := s.requirePostgres(); err != nil {
		return nil, err
	}
	var pruned int64
	if err := s.writer.DB.QueryRowContext(ctx, `SELECT pruned_through FROM hub_replication WHERE id = 1`).Scan(&pruned); err != nil {
		return nil, err
	}
	if after < pruned {
		return nil, ErrHubChangesPruned
	}
	rows, err := s.writer.DB.QueryContext(ctx, `SELECT seq, epoch, tbl, op, row_data FROM hub_changes WHERE seq > $1 ORDER BY seq LIMIT $2`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HubChange
	for rows.Next() {
		var change HubChange
		var row []byte
		if err := rows.Scan(&change.Seq, &change.Epoch, &change.Table, &change.Op, &row); err != nil {
			return nil, err
		}
		change.Row = row
		out = append(out, change)
	}
	return out, rows.Err()
}

// PruneHubChanges drops changes logged before cutoff. A standby whose cursor
// is older than what remains loads a snapshot instead.
func (s *Store) PruneHubChanges(ctx context.Context, cutoff time.Time) error {
	if err := s.requirePostgres(); err != nil {
		return err
	}
	tx, err := s.writer.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var through sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(seq) FROM hub_changes WHERE at < $1`, cutoff).Scan(&through); err != nil || !through.Valid {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hub_changes WHERE seq <= $1`, through.Int64); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hub_replication SET pruned_through = GREATEST(pruned_through, $1) WHERE id = 1`, through.Int64); err != nil {
		return err
	}
	return tx.Commit()
}

// WriteHubSnapshot writes every hub table, and the change log position the
// copy corresponds to, as one JSON document.
func (s *Store) WriteHubSnapshot(ctx context.Context, w io.Writer) error {
	if err := s.requirePostgres(); err != nil {
		return err
	}
	// One repeatable-read transaction sees the tables and the log at the
	// same instant. Writers log in commit order, so every change at or
	// below the highest visible seq is in the copy and none above it is.
	tx, err := s.writer.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	snapshot := HubSnapshot{Tables: map[string][]json.RawMessage{}}
	if err := tx.QueryRowContext(ctx, `SELECT r.epoch, COALESCE((SELECT MAX(seq) FROM hub_changes), r.pruned_through),
		COALESCE((SELECT MAX(version) FROM schema_migrations), 0) FROM hub_replication r WHERE r.id = 1`).
		Scan(&snapshot.Epoch, &snapshot.Seq, &snapshot.SchemaVersion); err != nil {
		return err
	}
	for _, table := range HubTables {
		rows, err := tx.QueryContext(ctx, `SELECT to_jsonb(t) FROM `+table+` t`)
		if err != nil {
			return fmt.Errorf("read %s: %w", table, err)
		}
		items := []json.RawMessage{}
		for rows.Next() {
			var row []byte
			if err := rows.Scan(&row); err != nil {
				rows.Close()
				return err
			}
			items = append(items, row)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		snapshot.Tables[table] = items
	}
	return json.NewEncoder(w).Encode(snapshot)
}

// LoadHubSnapshot replaces this machine's hub tables with a snapshot from the
// primary. It only runs on a standby.
func (s *Store) LoadHubSnapshot(ctx context.Context, snapshot HubSnapshot) error {
	if err := s.requirePostgres(); err != nil {
		return err
	}
	tx, err := s.beginHubApply(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.checkHubApply(ctx, tx, snapshot.SchemaVersion); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `TRUNCATE `+strings.Join(HubTables, ", ")); err != nil {
		return err
	}
	for _, table := range HubTables {
		rows := snapshot.Tables[table]
		if len(rows) == 0 {
			continue
		}
		encoded, err := json.Marshal(rows)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+table+` SELECT * FROM jsonb_populate_recordset(NULL::`+table+`, $1::jsonb)`, string(encoded)); err != nil {
			return fmt.Errorf("load %s: %w", table, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hub_replication SET applied_seq = $1, epoch = GREATEST(epoch, $2), updated_at = now() WHERE id = 1`,
		snapshot.Seq, snapshot.Epoch); err != nil {
		return err
	}
	return tx.Commit()
}

// ApplyHubChanges applies the primary's changes in order, all or nothing.
// Changes at or below what was already applied are skipped, so a batch can be
// delivered twice.
func (s *Store) ApplyHubChanges(ctx context.Context, schemaVersion int, changes []HubChange) error {
	if err := s.requirePostgres(); err != nil {
		return err
	}
	if len(changes) == 0 {
		return nil
	}
	tx, err := s.beginHubApply(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.checkHubApply(ctx, tx, schemaVersion); err != nil {
		return err
	}
	var applied, epoch int64
	if err := tx.QueryRowContext(ctx, `SELECT applied_seq, epoch FROM hub_replication WHERE id = 1`).Scan(&applied, &epoch); err != nil {
		return err
	}
	for _, change := range changes {
		if change.Seq <= applied {
			continue
		}
		spec, err := s.hubTable(ctx, change.Table)
		if err != nil {
			return err
		}
		var statement string
		switch change.Op {
		case "INSERT", "UPDATE":
			statement = spec.upsert
		case "DELETE":
			statement = spec.delete
		default:
			return fmt.Errorf("change %d has unknown operation %q", change.Seq, change.Op)
		}
		if _, err := tx.ExecContext(ctx, statement, string(change.Row)); err != nil {
			return fmt.Errorf("apply change %d to %s: %w", change.Seq, change.Table, err)
		}
		applied = change.Seq
		epoch = max(epoch, change.Epoch)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hub_replication SET applied_seq = $1, epoch = $2, updated_at = now() WHERE id = 1`, applied, epoch); err != nil {
		return err
	}
	return tx.Commit()
}

// HubLocalRows counts what this machine holds in hub tables, so becoming a
// standby (which replaces them) can be refused when they hold real data.
// push_vapid_keys is left out: every machine creates its own key on start.
func (s *Store) HubLocalRows(ctx context.Context) (map[string]int, error) {
	if err := s.requirePostgres(); err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, table := range HubTables {
		if table == "push_vapid_keys" {
			continue
		}
		var count int
		if err := s.writer.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
			return nil, err
		}
		if count > 0 {
			out[table] = count
		}
	}
	return out, nil
}

// SetDeviceHubRole marks a paired device as allowed ("standby") or not ("")
// to follow this hub's data.
func (s *Store) SetDeviceHubRole(ctx context.Context, id, role string) error {
	result, err := s.writer.ExecContext(ctx, `UPDATE devices SET hub_role=? WHERE id=?`, nullableString(role), id)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err == nil && count == 0 {
		return fmt.Errorf("device %s is not paired", id)
	}
	return nil
}

// DeviceHubRoles maps each device that may act as a hub to its role
// ("standby"). Without PostgreSQL there is no hub replication and it is empty.
func (s *Store) DeviceHubRoles(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	if !s.IsPostgres() {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, hub_role FROM devices WHERE hub_role IS NOT NULL AND hub_role <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, role string
		if err := rows.Scan(&id, &role); err != nil {
			return nil, err
		}
		out[id] = role
	}
	return out, rows.Err()
}

// DeviceHubRole returns what a device may do as a hub ("standby" or "").
func (s *Store) DeviceHubRole(ctx context.Context, id string) (string, error) {
	var role sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT hub_role FROM devices WHERE id=?`, id).Scan(&role)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return role.String, err
}

func (s *Store) beginHubApply(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.writer.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	// The follower's writes are the primary's; the trigger neither logs nor
	// fences them.
	if _, err := tx.ExecContext(ctx, `SELECT set_config('agentmux.hub_apply', 'on', true)`); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func (s *Store) checkHubApply(ctx context.Context, tx *sql.Tx, primaryVersion int) error {
	var role string
	var version int
	if err := tx.QueryRowContext(ctx, `SELECT role, COALESCE((SELECT MAX(version) FROM schema_migrations), 0) FROM hub_replication WHERE id = 1 FOR UPDATE`).
		Scan(&role, &version); err != nil {
		return err
	}
	if HubRole(role) != HubRoleStandby {
		return fmt.Errorf("this machine is a %s hub, not a standby", role)
	}
	if version != primaryVersion {
		return &HubSchemaMismatchError{Local: version, Primary: primaryVersion}
	}
	return nil
}

// HubSchemaMismatchError stops a standby from writing rows shaped for another
// version of the hub tables. Updating the older hub resolves it.
type HubSchemaMismatchError struct{ Local, Primary int }

func (e *HubSchemaMismatchError) Error() string {
	return fmt.Sprintf("this hub's database is at schema version %d and the primary's at %d; update the older one", e.Local, e.Primary)
}

type hubTableSpec struct{ upsert, delete string }

var hubTableSpecs sync.Map // table name -> hubTableSpec

// hubTable builds the statements that apply a logged row to one hub table,
// from the table's own columns and primary key.
func (s *Store) hubTable(ctx context.Context, table string) (hubTableSpec, error) {
	if cached, ok := hubTableSpecs.Load(table); ok {
		return cached.(hubTableSpec), nil
	}
	known := false
	for _, name := range HubTables {
		known = known || name == table
	}
	if !known {
		return hubTableSpec{}, fmt.Errorf("%q is not a hub table", table)
	}
	rows, err := s.writer.DB.QueryContext(ctx, `SELECT a.attname, COALESCE(i.indisprimary, false)
		FROM pg_attribute a
		LEFT JOIN pg_index i ON i.indrelid = a.attrelid AND i.indisprimary AND a.attnum = ANY(i.indkey)
		WHERE a.attrelid = $1::regclass AND a.attnum > 0 AND NOT a.attisdropped ORDER BY a.attnum`, table)
	if err != nil {
		return hubTableSpec{}, err
	}
	defer rows.Close()
	var columns, keys, updates []string
	for rows.Next() {
		var name string
		var primary bool
		if err := rows.Scan(&name, &primary); err != nil {
			return hubTableSpec{}, err
		}
		quoted := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
		columns = append(columns, quoted)
		if primary {
			keys = append(keys, quoted)
		} else {
			updates = append(updates, quoted+" = EXCLUDED."+quoted)
		}
	}
	if err := rows.Err(); err != nil {
		return hubTableSpec{}, err
	}
	if len(keys) == 0 {
		return hubTableSpec{}, fmt.Errorf("hub table %s has no primary key", table)
	}
	record := `jsonb_populate_record(NULL::` + table + `, $1::jsonb)`
	conflict := "DO NOTHING"
	if len(updates) > 0 {
		conflict = "DO UPDATE SET " + strings.Join(updates, ", ")
	}
	spec := hubTableSpec{
		upsert: `INSERT INTO ` + table + ` (` + strings.Join(columns, ", ") + `) SELECT ` + strings.Join(columns, ", ") +
			` FROM ` + record + ` ON CONFLICT (` + strings.Join(keys, ", ") + `) ` + conflict,
		delete: `DELETE FROM ` + table + ` WHERE (` + strings.Join(keys, ", ") + `) IN (SELECT ` + strings.Join(keys, ", ") + ` FROM ` + record + `)`,
	}
	hubTableSpecs.Store(table, spec)
	return spec, nil
}
