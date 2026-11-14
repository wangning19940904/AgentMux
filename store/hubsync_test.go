package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// syncHub copies everything the primary logged after the standby's cursor.
func syncHub(t *testing.T, primary, standby *Store) {
	t.Helper()
	ctx := context.Background()
	state, err := standby.HubReplication(ctx)
	if err != nil {
		t.Fatal(err)
	}
	source, err := primary.HubReplication(ctx)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := primary.HubChangesAfter(ctx, state.AppliedSeq, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if err := standby.ApplyHubChanges(ctx, source.SchemaVersion, changes); err != nil {
		t.Fatal(err)
	}
}

func snapshotHub(t *testing.T, primary, standby *Store) {
	t.Helper()
	var buf bytes.Buffer
	if err := primary.WriteHubSnapshot(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	var snapshot HubSnapshot
	if err := json.Unmarshal(buf.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if err := standby.LoadHubSnapshot(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresHubReplicationFollowsAndFences(t *testing.T) {
	ctx := context.Background()
	primary := openPostgresIntegrationStore(t)
	standby := openPostgresIntegrationStore(t)

	// Without a role nothing is logged: a single hub pays nothing.
	if _, err := primary.CreateDevicePairingCode(ctx, "before"); err != nil {
		t.Fatal(err)
	}
	if state, _ := primary.HubReplication(ctx); state.Role != HubRoleNone || state.HeadSeq != 0 {
		t.Fatalf("state without a role = %+v", state)
	}

	if err := primary.SetHubRole(ctx, HubRolePrimary, 1); err != nil {
		t.Fatal(err)
	}
	if err := standby.SetHubRole(ctx, HubRoleStandby, 0); err != nil {
		t.Fatal(err)
	}
	// The standby's own rows in hub tables are replaced by the snapshot.
	if err := standby.SetHubRole(ctx, HubRoleNone, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := standby.CreateDevicePairingCode(ctx, "standby's own"); err != nil {
		t.Fatal(err)
	}
	if local, err := standby.HubLocalRows(ctx); err != nil || local["device_pairing_codes"] != 1 {
		t.Fatalf("local rows = %v, %v", local, err)
	}
	if err := standby.SetHubRole(ctx, HubRoleStandby, 0); err != nil {
		t.Fatal(err)
	}
	snapshotHub(t, primary, standby)

	code, err := primary.CreateDevicePairingCode(ctx, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	device, token, err := primary.RedeemDevicePairingCode(ctx, code.Code, core.DeviceInfo{Hostname: "laptop.local"})
	if err != nil || device == nil {
		t.Fatalf("pair: %v %v", device, err)
	}
	if err := primary.RenameDevice(ctx, device.ID, "Laptop"); err != nil {
		t.Fatal(err)
	}
	if err := primary.ApplyDeviceReport(ctx, device.ID, core.DeviceReport{
		Collection: "status", Path: "/api/v1/status", Shape: core.DeviceReportValue,
		Upserts: []core.DeviceReportDoc{{ID: "_", Data: json.RawMessage(`{"ok":true}`)}},
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	syncHub(t, primary, standby)
	syncHub(t, primary, standby) // delivered twice, applied once

	// The standby recognizes the device's token and holds its report.
	got, err := standby.AuthenticateDevice(ctx, token)
	if err != nil || got == nil || got.Name != "Laptop" {
		t.Fatalf("standby device = %+v, %v", got, err)
	}
	if payload, _, ok, err := standby.DeviceReportedResponse(ctx, device.ID, "/api/v1/status"); err != nil || !ok || string(payload) != `{"ok":true}` {
		t.Fatalf("standby report = %s %v %v", payload, ok, err)
	}
	codes, _ := standby.HubLocalRows(ctx)
	if codes["device_pairing_codes"] != 2 {
		t.Fatalf("standby pairing codes = %v; the standby's own code should be gone", codes)
	}

	// A standby refuses writes of its own.
	if err := standby.RenameDevice(ctx, device.ID, "rogue"); err == nil || !strings.Contains(err.Error(), "standby") {
		t.Fatalf("standby write err = %v", err)
	}

	// Deletes follow too.
	if err := primary.DeleteDevice(ctx, device.ID); err != nil {
		t.Fatal(err)
	}
	syncHub(t, primary, standby)
	if got, _ := standby.GetDevice(ctx, device.ID); got != nil {
		t.Fatalf("deleted device still on standby: %+v", got)
	}

	// Failover: the standby continues the log above what it applied, and the
	// lapsed primary is fenced.
	applied, _ := standby.HubReplication(ctx)
	if err := primary.SetHubRole(ctx, HubRoleFenced, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := primary.CreateDevicePairingCode(ctx, "fenced"); err == nil {
		t.Fatal("fenced primary accepted a write")
	}
	if err := standby.SetHubRole(ctx, HubRolePrimary, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := standby.CreateDevicePairingCode(ctx, "after failover"); err != nil {
		t.Fatal(err)
	}
	changes, err := standby.HubChangesAfter(ctx, applied.AppliedSeq, 10)
	if err != nil || len(changes) == 0 || changes[0].Seq <= applied.AppliedSeq || changes[0].Epoch != 2 {
		t.Fatalf("new primary's log = %+v, %v (applied %d)", changes, err, applied.AppliedSeq)
	}

	// The old primary rejoins as a standby from a snapshot.
	if err := primary.SetHubRole(ctx, HubRoleStandby, 2); err != nil {
		t.Fatal(err)
	}
	if state, _ := primary.HubReplication(ctx); state.AppliedSeq != 0 {
		t.Fatalf("rejoining standby kept its cursor: %+v", state)
	}
	snapshotHub(t, standby, primary)
	syncHub(t, standby, primary)
	if rows, _ := primary.HubLocalRows(ctx); rows["device_pairing_codes"] != 3 {
		t.Fatalf("old primary after resync = %v", rows)
	}
}

func TestPostgresHubReplicationPruneAndSchemaCheck(t *testing.T) {
	ctx := context.Background()
	primary := openPostgresIntegrationStore(t)
	standby := openPostgresIntegrationStore(t)
	if err := primary.SetHubRole(ctx, HubRolePrimary, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := primary.CreateDevicePairingCode(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	if err := primary.PruneHubChanges(ctx, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := primary.HubChangesAfter(ctx, 0, 10); !errors.Is(err, ErrHubChangesPruned) {
		t.Fatalf("pruned read err = %v", err)
	}
	if err := standby.SetHubRole(ctx, HubRoleStandby, 0); err != nil {
		t.Fatal(err)
	}
	var mismatch *HubSchemaMismatchError
	err := standby.ApplyHubChanges(ctx, 9999, []HubChange{{Seq: 1, Table: "devices", Op: "DELETE", Row: json.RawMessage(`{"id":"x"}`)}})
	if !errors.As(err, &mismatch) {
		t.Fatalf("schema mismatch err = %v", err)
	}
}

// A standby runs the same migrations as the primary, so a migration that
// rewrites hub rows must not be refused there (or logged on a primary).
func TestPostgresMigrationsRunOnStandby(t *testing.T) {
	ctx := context.Background()
	standby := openPostgresIntegrationStore(t)
	now := time.Now().UTC()
	if err := standby.UpsertPushSubscription(ctx, core.PushSubscription{ID: "legacy", Subscriber: "tenant:gone",
		Endpoint: "https://push.example/legacy", P256dh: "k", Auth: "a", Kinds: []string{"message"}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := standby.SetHubRole(ctx, HubRoleStandby, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := standby.CreateDevicePairingCode(ctx, "refused"); err == nil {
		t.Fatal("standby accepted a hub write")
	}
	if _, err := standby.db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version=29`); err != nil {
		t.Fatal(err)
	}
	if err := standby.migratePostgres(ctx); err != nil {
		t.Fatalf("migrations on a standby: %v", err)
	}
	var left int
	if err := standby.db.QueryRowContext(ctx, `SELECT count(*) FROM push_subscriptions WHERE subscriber LIKE 'tenant:%'`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("legacy push rows left = %d, %v", left, err)
	}
}
