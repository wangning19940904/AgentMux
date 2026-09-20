package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/eventrelay"
)

func TestEventSubscriptionTenantIsolation(t *testing.T) {
	srv, st, tenant, token := newTenantServer(t)
	ctx := context.Background()
	now := time.Now().UTC()
	srv.eventRelay = eventrelay.New(st, nil, nil)
	defer srv.eventRelay.Stop()
	ch := &core.Channel{ID: "relay", Name: "shared", Type: "feishu", Visibility: core.VisibilityPublic, CreatedAt: now, UpdatedAt: now}
	if err := st.UpsertChannel(ctx, ch); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, credential string, body any) *httptest.ResponseRecorder {
		buf := &bytes.Buffer{}
		if body != nil {
			json.NewEncoder(buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, buf)
		req.Header.Set("Authorization", "Bearer "+credential)
		rec := httptest.NewRecorder()
		srv.withAuth(srv.mux).ServeHTTP(rec, req)
		return rec
	}
	sub := core.EventSubscription{Key: "homebook", Name: "Homebook", Source: "feishu", SourceRefs: []string{"channel:relay"}, EventTypes: []string{"drive.file.bitable_record_changed_v1"}, CallbackURL: "http://127.0.0.1:8000/events"}
	if rec := call("POST", "/api/v1/event-subscriptions", token, sub); rec.Code != 403 {
		t.Fatalf("public source allowed: %d %s", rec.Code, rec.Body)
	}
	grant := core.ResourceGrant{TenantID: tenant.ID, ResourceType: core.ResourceTypeEventSource, ResourceID: "channel:relay", Level: core.GrantLevelUse}
	if rec := call("POST", "/api/v1/tenancy/grants", "admin-secret", grant); rec.Code != 200 {
		t.Fatalf("grant: %s", rec.Body)
	}
	rec := call("POST", "/api/v1/event-subscriptions", token, sub)
	var result struct {
		Subscription core.EventSubscription `json:"subscription"`
		Secret       string                 `json:"signing_secret"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || rec.Code != 200 || result.Secret == "" {
		t.Fatalf("create %d %s", rec.Code, rec.Body)
	}
	if result.Subscription.OwnerTenantID != tenant.ID {
		t.Fatal("owner not stamped")
	}
	again := call("POST", "/api/v1/event-subscriptions", token, sub)
	if again.Code != 200 || strings.Contains(again.Body.String(), "signing_secret") {
		t.Fatalf("idempotent upsert exposed secret: %s", again.Body)
	}
	list := call("GET", "/api/v1/event-subscriptions", token, nil)
	if strings.Contains(list.Body.String(), result.Secret) || !strings.Contains(list.Body.String(), result.Subscription.ID) {
		t.Fatal("list secret or id")
	}
	other := *tenant
	other.ID = "peer"
	other.Name = "peer"
	st.UpsertTenant(ctx, &other)
	peerToken, _ := st.CreateTenantToken(ctx, other.ID, "peer", nil)
	if rec := call("DELETE", "/api/v1/event-subscriptions?id="+result.Subscription.ID, peerToken.Secret, nil); rec.Code != 404 {
		t.Fatal("peer could delete")
	}
	if rec := call("POST", "/api/v1/event-subscriptions/test", token, map[string]string{"id": result.Subscription.ID}); rec.Code != 202 {
		t.Fatalf("test %d %s", rec.Code, rec.Body)
	}
	deliveries, _ := st.ListEventDeliveries(ctx, nil, result.Subscription.ID, "", 100, 0)
	if len(deliveries) != 1 || deliveries[0].Event.Type != "agentmux.subscription.test" {
		t.Fatal("missing marked test event")
	}
	st.DeleteResourceGrant(ctx, tenant.ID, core.ResourceTypeEventSource, "channel:relay")
	if rec := call("GET", "/api/v1/event-deliveries?id="+deliveries[0].ID, token, nil); rec.Code != 403 {
		t.Fatal("revoked source payload readable")
	}
	rec = call("GET", "/api/v1/event-deliveries", token, nil)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), `"data"`) {
		t.Fatalf("revoked payload in list: %s", rec.Body)
	}
	if rec := call("POST", "/api/v1/event-deliveries/retry", peerToken.Secret, map[string]string{"id": deliveries[0].ID}); rec.Code != 404 {
		t.Fatal("peer could retry")
	}
	sub.CallbackURL = "http://192.0.2.1/"
	if rec := call("POST", "/api/v1/event-subscriptions", token, sub); rec.Code != 400 {
		t.Fatal("non-loopback accepted")
	}
}
