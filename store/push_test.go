package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// pushStores runs a test against SQLite, and against PostgreSQL when
// AGENTMUX_TEST_DATABASE_URL is set.
func pushStores(t *testing.T, test func(t *testing.T, st *Store)) {
	t.Run("sqlite", func(t *testing.T) {
		st, err := OpenLegacySQLite(filepath.Join(t.TempDir(), "push.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		test(t, st)
	})
	if os.Getenv("AGENTMUX_TEST_DATABASE_URL") != "" {
		t.Run("postgres", func(t *testing.T) { test(t, openPostgresIntegrationStore(t)) })
	}
}

func TestPushVAPIDKeysAreMadeOnce(t *testing.T) {
	pushStores(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		if keys, err := st.PushVAPIDKeys(ctx); err != nil || keys != nil {
			t.Fatalf("before any keys: %+v, %v", keys, err)
		}
		first, err := st.EnsurePushVAPIDKeys(ctx, core.PushVAPIDKeys{PublicKey: "pub-1", PrivateKey: "priv-1"})
		if err != nil || first.PublicKey != "pub-1" || first.CreatedAt.IsZero() {
			t.Fatalf("first = %+v, %v", first, err)
		}
		// A second process that generated its own pair keeps the first one.
		second, err := st.EnsurePushVAPIDKeys(ctx, core.PushVAPIDKeys{PublicKey: "pub-2", PrivateKey: "priv-2"})
		if err != nil || second.PublicKey != "pub-1" || second.PrivateKey != "priv-1" {
			t.Fatalf("second = %+v, %v", second, err)
		}
	})
}

func TestPushSubscriptionsBelongToTheirSubscriber(t *testing.T) {
	pushStores(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		phone := core.PushSubscription{ID: "sub-phone", Subscriber: "admin", Endpoint: "https://push.example/a",
			P256dh: "key-a", Auth: "auth-a", Kinds: []string{core.PushKindWaiting}, Label: "iPhone"}
		tablet := core.PushSubscription{ID: "sub-tablet", Subscriber: "account:a1", Endpoint: "https://push.example/b",
			P256dh: "key-b", Auth: "auth-b", Kinds: []string{}, CreatedAt: time.Now().Add(time.Minute)}
		for _, sub := range []core.PushSubscription{phone, tablet} {
			if err := st.UpsertPushSubscription(ctx, sub); err != nil {
				t.Fatal(err)
			}
		}
		admin, err := st.ListPushSubscriptions(ctx, "admin")
		if err != nil || len(admin) != 1 || admin[0].Endpoint != phone.Endpoint || !admin[0].Wants(core.PushKindWaiting) || admin[0].Label != "iPhone" {
			t.Fatalf("admin list = %+v, %v", admin, err)
		}
		all, err := st.ListPushSubscriptions(ctx, "")
		if err != nil || len(all) != 2 || all[1].Kinds == nil {
			t.Fatalf("all = %+v, %v", all, err)
		}

		// Subscribing again replaces keys and kinds and clears an old error.
		if err := st.RecordPushDelivery(ctx, phone.ID, time.Now(), "push service said 500"); err != nil {
			t.Fatal(err)
		}
		phone.P256dh, phone.Kinds = "key-a2", []string{core.PushKindTickets, core.PushKindMachines}
		if err := st.UpsertPushSubscription(ctx, phone); err != nil {
			t.Fatal(err)
		}
		got, err := st.GetPushSubscription(ctx, phone.ID)
		if err != nil || got.P256dh != "key-a2" || got.Wants(core.PushKindWaiting) || !got.Wants(core.PushKindMachines) || got.LastError != "" {
			t.Fatalf("after resubscribe = %+v, %v", got, err)
		}
		delivered := time.Now().UTC().Truncate(time.Second)
		if err := st.RecordPushDelivery(ctx, phone.ID, delivered, ""); err != nil {
			t.Fatal(err)
		}
		if got, _ := st.GetPushSubscription(ctx, phone.ID); !got.LastSuccessAt.Equal(delivered) {
			t.Fatalf("last success = %v, want %v", got.LastSuccessAt, delivered)
		}

		// Another subscriber cannot remove it; the push service (no
		// subscriber) can.
		if removed, err := st.DeletePushSubscription(ctx, phone.ID, "account:a1"); err != nil || removed {
			t.Fatalf("foreign delete = %v, %v", removed, err)
		}
		if removed, err := st.DeletePushSubscription(ctx, phone.ID, ""); err != nil || !removed {
			t.Fatalf("delete = %v, %v", removed, err)
		}
		if got, err := st.GetPushSubscription(ctx, phone.ID); err != nil || got != nil {
			t.Fatalf("after delete = %+v, %v", got, err)
		}
	})
}
