package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func relayFixture(t *testing.T, st *Store) (core.EventSubscription, core.RelayEvent) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := st.UpsertChannel(ctx, &core.Channel{ID: "relay-channel", Name: "relay", Type: "feishu", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	sub := core.EventSubscription{Key: "receiver", Name: "receiver", Source: "feishu", SourceRefs: []string{"channel:relay-channel"}, EventTypes: []string{"drive.file.bitable_record_changed_v1"}, CallbackURL: "http://127.0.0.1:8000/events", Secret: "secret"}
	if created, err := st.SaveEventSubscription(ctx, &sub); err != nil || !created {
		t.Fatalf("save %v %v", created, err)
	}
	event := core.RelayEvent{SchemaVersion: "1", ID: "event-original", Source: "feishu", Type: sub.EventTypes[0], SourceRef: sub.SourceRefs[0], SourceEventID: "upstream-1", Attributes: map[string]string{"app_id": "app-1"}, OccurredAt: now, ReceivedAt: now, Data: json.RawMessage(`{"event":{"table_id":"t1"}}`)}
	return sub, event
}
func sqliteRelayStore(t *testing.T) *Store {
	t.Helper()
	st, err := OpenLegacySQLite(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func testRelayDurability(t *testing.T, st *Store) {
	ctx := context.Background()
	sub, event := relayFixture(t, st)
	if err := st.IngestRelayEvent(ctx, event, ""); err != nil {
		t.Fatal(err)
	}
	second := sub
	second.ID = ""
	second.Key = "second"
	if _, err := st.SaveEventSubscription(ctx, &second); err != nil {
		t.Fatal(err)
	}
	event.ID = "duplicate"
	if err := st.IngestRelayEvent(ctx, event, ""); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListEventDeliveries(ctx, nil, "", "", 100, 0)
	if err != nil || len(list) != 1 {
		t.Fatalf("duplicate backfilled new subscriber: %v %v", list, err)
	}
	oldID := sub.ID
	sub.Secret = "must-not-replace"
	if created, err := st.SaveEventSubscription(ctx, &sub); err != nil || created || sub.ID != oldID || sub.Secret != "secret" {
		t.Fatalf("idempotency/secret: %+v %v %v", sub, created, err)
	}
	now := time.Now().Add(time.Second)
	first, err := st.ClaimEventDelivery(ctx, now)
	if err != nil || first == nil {
		t.Fatalf("claim %v %v", first, err)
	}
	if d, err := st.ClaimEventDelivery(ctx, now); err != nil || d != nil {
		t.Fatalf("concurrent subscriber claim: %v %v", d, err)
	}
	reclaimed, err := st.ClaimEventDelivery(ctx, now.Add(31*time.Second))
	if err != nil || reclaimed == nil || reclaimed.ID != first.ID || reclaimed.LeaseToken == first.LeaseToken {
		t.Fatalf("lease recovery %v %v", reclaimed, err)
	}
	if err := st.FinishEventDelivery(ctx, first, "", "sent", 200, "", now); err == nil {
		t.Fatal("stale worker completed reclaimed delivery")
	}
	attempt, err := st.BeginEventAttempt(ctx, reclaimed, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinishEventDelivery(ctx, reclaimed, attempt, "dead", 503, "unavailable", now); err != nil {
		t.Fatal(err)
	}
	if err := st.RetryEventDelivery(ctx, reclaimed.ID); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetEventDelivery(ctx, reclaimed.ID)
	if err != nil || got.EventID != "event-original" || got.Attempts != 1 || len(got.History) != 1 || got.FirstAttemptAt != nil {
		t.Fatalf("retry history %+v %v", got, err)
	}
	sub.Paused = true
	if _, err := st.SaveEventSubscription(ctx, &sub); err != nil {
		t.Fatal(err)
	}
	if d, err := st.ClaimEventDelivery(ctx, time.Now().Add(time.Hour)); err != nil || d != nil {
		t.Fatalf("paused claim %+v %v", d, err)
	}
	if err := st.DeleteEventSubscription(ctx, sub.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetEventDelivery(ctx, got.ID)
	if got.Status != "cancelled" {
		t.Fatal(got.Status)
	}
	if _, err := st.SaveEventSubscription(ctx, &sub); err == nil {
		t.Fatal("deleted subscription resurrected")
	}
}
func TestEventRelayDurability(t *testing.T) { testRelayDurability(t, sqliteRelayStore(t)) }
func TestPostgresEventRelayDurability(t *testing.T) {
	testRelayDurability(t, openPostgresIntegrationStore(t))
}

func TestEventRelayPermissionAndFilters(t *testing.T) {
	st := sqliteRelayStore(t)
	ctx := context.Background()
	sub, event := relayFixture(t, st)
	now := time.Now()
	tenant := &core.Tenant{ID: "consumer", Name: "consumer", Status: core.TenantStatusActive, CreatedAt: now, UpdatedAt: now}
	if err := st.UpsertTenant(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	channel, _ := st.GetChannel(ctx, "relay-channel")
	channel.Visibility = core.VisibilityPublic
	st.UpsertChannel(ctx, channel)
	st.UpsertResourceGrant(ctx, &core.ResourceGrant{TenantID: tenant.ID, ResourceType: core.ResourceTypeChannel, ResourceID: channel.ID, Level: core.GrantLevelManage})
	if allowed, err := st.EventSourceAllowed(ctx, tenant.ID, event.SourceRef); err != nil || allowed {
		t.Fatalf("ordinary grant leaked events: %v %v", allowed, err)
	}
	st.UpsertResourceGrant(ctx, &core.ResourceGrant{TenantID: tenant.ID, ResourceType: core.ResourceTypeEventSource, ResourceID: event.SourceRef, Level: core.GrantLevelUse})
	if allowed, err := st.EventSourceAllowed(ctx, tenant.ID, event.SourceRef); err != nil || !allowed {
		t.Fatalf("explicit grant: %v %v", allowed, err)
	}
	sub.ID = ""
	sub.OwnerTenantID = tenant.ID
	sub.Key = "tenant"
	sub.Filters = map[string][]string{"table_id": {"t1", "t2"}, "file_token": {"f1"}}
	st.SaveEventSubscription(ctx, &sub)
	event.Attributes["table_id"] = "t2"
	if sub.Matches(event) {
		t.Fatal("missing attribute matched")
	}
	event.Attributes["file_token"] = "f1"
	if !sub.Matches(event) {
		t.Fatal("AND/OR filter did not match")
	}
	tenant.Status = core.TenantStatusDisabled
	st.UpsertTenant(ctx, tenant)
	if allowed, err := st.EventSourceAllowed(ctx, tenant.ID, event.SourceRef); err != nil || allowed {
		t.Fatal("disabled tenant retained event permission")
	}
}

func TestPostgresEventRelayFanoutAndClaims(t *testing.T) {
	st := openPostgresIntegrationStore(t)
	ctx := context.Background()
	sub, event := relayFixture(t, st)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := st.IngestRelayEvent(ctx, event, ""); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var claims int
	var mu sync.Mutex
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := st.ClaimEventDelivery(ctx, time.Now())
			if err != nil {
				t.Error(err)
			}
			if d != nil {
				mu.Lock()
				claims++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if claims != 1 {
		t.Fatalf("claims=%d", claims)
	}
	list, err := st.ListEventDeliveries(ctx, nil, sub.ID, "", 100, 0)
	if err != nil || len(list) != 1 {
		t.Fatalf("fanout: %d %v", len(list), err)
	}
}

func TestEventRelayAtomicFanoutAndRetention(t *testing.T) {
	st := sqliteRelayStore(t)
	ctx := context.Background()
	sub, event := relayFixture(t, st)
	second := sub
	second.ID = ""
	second.Key = "second"
	if _, err := st.SaveEventSubscription(ctx, &second); err != nil {
		t.Fatal(err)
	}
	// Abort after the first delivery insert; the event and earlier delivery must
	// be rolled back together, rather than leaving an unrepeatable partial fanout.
	_, err := st.writer.ExecContext(ctx, `CREATE TRIGGER reject_delivery BEFORE INSERT ON event_deliveries WHEN NEW.subscription_id='`+second.ID+`' BEGIN SELECT RAISE(ABORT,'synthetic delivery failure'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.IngestRelayEvent(ctx, event, ""); err == nil {
		t.Fatal("fault injection did not fail")
	}
	for _, table := range []string{"relay_events", "event_deliveries"} {
		var count int
		if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial fanout in %s: %d %v", table, count, err)
		}
	}
	if _, err := st.writer.ExecContext(ctx, `DROP TRIGGER reject_delivery`); err != nil {
		t.Fatal(err)
	}
	st.DeleteEventSubscription(ctx, second.ID)
	now := time.Now()
	for i, status := range []string{"sent", "dead", "pending"} {
		event.ID = fmt.Sprintf("event-%d", i)
		event.SourceEventID = fmt.Sprintf("native-%d", i)
		if err := st.IngestRelayEvent(ctx, event, ""); err != nil {
			t.Fatal(err)
		}
		if status == "pending" {
			continue
		}
		d, err := st.ClaimEventDelivery(ctx, now.Add(time.Minute))
		if err != nil || d == nil {
			t.Fatal("claim", err)
		}
		attempt, err := st.BeginEventAttempt(ctx, d, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.FinishEventDelivery(ctx, d, attempt, status, 200, "", now); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PruneEventDeliveries(ctx, now.Add(8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	list, _ := st.ListEventDeliveries(ctx, nil, "", "", 100, 0)
	if len(list) != 2 {
		t.Fatalf("7-day retention: %d", len(list))
	}
	if err := st.PruneEventDeliveries(ctx, now.Add(31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	list, _ = st.ListEventDeliveries(ctx, nil, "", "", 100, 0)
	if len(list) != 1 || list[0].Status != "pending" {
		t.Fatalf("unfinished event pruned: %+v", list)
	}
}
