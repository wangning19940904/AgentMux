package eventrelay

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

func TestCallbackValidationAndSignature(t *testing.T) {
	for _, raw := range []string{"https://example.com/hook", "http://169.254.169.254/", "http://user:pass@127.0.0.1/", "ftp://127.0.0.1/", "http://127.0.0.1/#fragment", "http://127.0.0.1:99999/"} {
		if ValidateCallback(raw) == nil {
			t.Error("accepted", raw)
		}
	}
	for _, raw := range []string{"http://127.0.0.1:8000/events", "http://[::1]:8000/events", "https://localhost/hook"} {
		if err := ValidateCallback(raw); err != nil {
			t.Error(raw, err)
		}
	}
	now := time.Now()
	stamp := strconv.FormatInt(now.Unix(), 10)
	body := []byte(`{"id":"e"}`)
	sig := Signature("secret", stamp, "d", body)
	if !VerifySignature("secret", stamp, "d", sig, body, now) || VerifySignature("secret", stamp, "d2", sig, body, now) || VerifySignature("secret", stamp, "d", sig, body, now.Add(6*time.Minute)) {
		t.Fatal("signature or replay validation")
	}
}
func TestLocalClientRejectsRedirectAndProxy(t *testing.T) {
	var reached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer redirect.Close()
	t.Setenv("HTTP_PROXY", target.URL)
	t.Setenv("HTTPS_PROXY", target.URL)
	client := LocalHTTPClient()
	defer client.CloseIdleConnections()
	resp, err := client.Get(redirect.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 302 || reached.Load() {
		t.Fatal("followed redirect or proxy")
	}
	if _, err := client.Get("http://192.0.2.1/"); err == nil {
		t.Fatal("dialed non-loopback")
	}
}
func TestDeliveryFanoutRetryAndRevocation(t *testing.T) {
	st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	svc := New(st, nil, nil)
	defer svc.Stop()
	now := time.Now().UTC()
	st.UpsertChannel(ctx, &core.Channel{ID: "ch", Name: "ch", Type: "feishu", CreatedAt: now, UpdatedAt: now})
	var failed atomic.Bool
	failed.Store(true)
	var received atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !VerifySignature("secret", r.Header.Get(TimestampHeader), r.Header.Get(DeliveryHeader), r.Header.Get(SignatureHeader), body, time.Now()) {
			t.Error("bad callback signature")
		}
		if failed.Load() {
			w.WriteHeader(503)
			return
		}
		received.Add(1)
		w.WriteHeader(202)
	}))
	defer receiver.Close()
	sub := core.EventSubscription{Key: "a", Name: "a", Source: "feishu", SourceRefs: []string{"channel:ch"}, EventTypes: []string{"changed"}, CallbackURL: receiver.URL, Secret: "secret"}
	st.SaveEventSubscription(ctx, &sub)
	second := sub
	second.ID = ""
	second.Key = "b"
	st.SaveEventSubscription(ctx, &second)
	e := core.RelayEvent{Source: "feishu", Type: "changed", SourceRef: "channel:ch", SourceEventID: "upstream", Attributes: map[string]string{"app_id": "app"}, Data: json.RawMessage(`{"changed":true}`)}
	if err := svc.Publish(ctx, e); err != nil {
		t.Fatal(err)
	}
	// Simulate a restart between durable ingress and worker dispatch.
	svc = New(st, nil, nil)
	defer svc.Stop()
	for range 2 {
		d, err := st.ClaimEventDelivery(ctx, time.Now())
		if err != nil || d == nil {
			t.Fatalf("claim %v %v", d, err)
		}
		svc.deliver(ctx, d)
	}
	items, _ := st.ListEventDeliveries(ctx, nil, "", "", 100, 0)
	if len(items) != 2 || items[0].Status != "retry" || items[1].Status != "retry" {
		t.Fatalf("outbox %+v", items)
	}
	failed.Store(false)
	for range 2 {
		d, err := st.ClaimEventDelivery(ctx, time.Now().Add(time.Minute))
		if err != nil || d == nil {
			t.Fatalf("claim retry %v %v", d, err)
		}
		svc.deliver(ctx, d)
	}
	if received.Load() != 2 {
		t.Fatalf("fanout delivered %d", received.Load())
	}
	for _, item := range items {
		d, _ := st.GetEventDelivery(ctx, item.ID)
		if d.Status != "sent" || d.Attempts != 2 || len(d.History) != 2 {
			t.Fatalf("history %+v", d)
		}
	}
	// Unacknowledged upstream retries do not re-fanout.
	svc.Publish(ctx, e)
	items, _ = st.ListEventDeliveries(ctx, nil, "", "", 100, 0)
	if len(items) != 2 {
		t.Fatal("duplicate fanout")
	}
}

func TestLifecyclePersistenceFailureIsReported(t *testing.T) {
	st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	svc := New(st, nil, nil)
	defer svc.Stop()
	st.Close()
	start := time.Now()
	err = svc.Publish(context.Background(), core.RelayEvent{Source: "agentmux", SourceRef: "system", Type: "task.completed", Data: json.RawMessage(`{}`)})
	if err == nil || svc.Health("system").Failures != 1 || time.Since(start) > time.Second {
		t.Fatal("missing bounded failure status")
	}
}

func TestDeliveryRechecksRevocationAndDeadLetterWindow(t *testing.T) {
	st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	svc := New(st, nil, nil)
	defer svc.Stop()
	if err := st.UpsertChannel(ctx, &core.Channel{ID: "ch", Name: "ch", Type: "feishu", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	tenant := &core.Tenant{ID: "t", Name: "t", Status: core.TenantStatusActive, CreatedAt: now, UpdatedAt: now}
	st.UpsertTenant(ctx, tenant)
	grant := &core.ResourceGrant{TenantID: "t", ResourceType: core.ResourceTypeEventSource, ResourceID: "channel:ch", Level: core.GrantLevelUse}
	st.UpsertResourceGrant(ctx, grant)
	var calls atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(503) }))
	defer receiver.Close()
	sub := core.EventSubscription{OwnerTenantID: "t", Key: "a", Name: "a", Source: "feishu", SourceRefs: []string{"channel:ch"}, EventTypes: []string{"changed"}, CallbackURL: receiver.URL, Secret: "secret"}
	st.SaveEventSubscription(ctx, &sub)
	if err := svc.Publish(ctx, core.RelayEvent{Source: "feishu", Type: "changed", SourceRef: "channel:ch", Attributes: map[string]string{}, Data: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	d, err := st.ClaimEventDelivery(ctx, time.Now())
	if err != nil || d == nil {
		t.Fatal("claim", err)
	}
	st.DeleteResourceGrant(ctx, "t", core.ResourceTypeEventSource, "channel:ch")
	svc.deliver(ctx, d)
	blocked, _ := st.GetEventDelivery(ctx, d.ID)
	if calls.Load() != 0 || blocked.Status != "blocked" || blocked.FirstAttemptAt != nil {
		t.Fatalf("revoked delivery %+v calls=%d", blocked, calls.Load())
	}
	st.UpsertResourceGrant(ctx, grant)
	tenant.Status = core.TenantStatusDisabled
	st.UpsertTenant(ctx, tenant)
	d, _ = st.ClaimEventDelivery(ctx, time.Now().Add(time.Minute))
	svc.deliver(ctx, d)
	if calls.Load() != 0 {
		t.Fatal("disabled tenant received event")
	}
	tenant.Status = core.TenantStatusActive
	st.UpsertTenant(ctx, tenant)
	d, _ = st.ClaimEventDelivery(ctx, time.Now().Add(time.Minute))
	old := time.Now().Add(-25 * time.Hour)
	attempt, err := st.BeginEventAttempt(ctx, d, old)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinishEventDelivery(ctx, d, attempt, "retry", 503, "offline", time.Now()); err != nil {
		t.Fatal(err)
	}
	d, _ = st.ClaimEventDelivery(ctx, time.Now().Add(time.Second))
	svc.deliver(ctx, d)
	terminal, _ := st.GetEventDelivery(ctx, d.ID)
	if terminal.Status != "dead" || calls.Load() != 1 {
		t.Fatalf("expired retries %+v", terminal)
	}
}
