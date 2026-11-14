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

func TestEventSubscriptionsLifecycle(t *testing.T) {
	srv, st := newTestServer(t)
	srv.cfg.Bridge.Enabled = true
	srv.cfg.Bridge.Token = "admin-secret"
	ctx := context.Background()
	now := time.Now().UTC()
	srv.eventRelay = eventrelay.New(st, nil, nil)
	defer srv.eventRelay.Stop()
	ch := &core.Channel{ID: "relay", Name: "shared", Type: "feishu", CreatedAt: now, UpdatedAt: now}
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
	if rec := call("POST", "/api/v1/event-subscriptions", "not-a-token", sub); rec.Code != 401 {
		t.Fatalf("unknown credential: %d %s", rec.Code, rec.Body)
	}
	missing := sub
	missing.Key, missing.SourceRefs = "missing", []string{"channel:gone"}
	if rec := call("POST", "/api/v1/event-subscriptions", "admin-secret", missing); rec.Code < 400 {
		t.Fatalf("unknown source accepted: %d %s", rec.Code, rec.Body)
	}
	rec := call("POST", "/api/v1/event-subscriptions", "admin-secret", sub)
	var result struct {
		Subscription core.EventSubscription `json:"subscription"`
		Secret       string                 `json:"signing_secret"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || rec.Code != 200 || result.Secret == "" {
		t.Fatalf("create %d %s", rec.Code, rec.Body)
	}
	again := call("POST", "/api/v1/event-subscriptions", "admin-secret", sub)
	if again.Code != 200 || strings.Contains(again.Body.String(), "signing_secret") {
		t.Fatalf("idempotent upsert exposed secret: %s", again.Body)
	}
	list := call("GET", "/api/v1/event-subscriptions", "admin-secret", nil)
	if strings.Contains(list.Body.String(), result.Secret) || !strings.Contains(list.Body.String(), result.Subscription.ID) {
		t.Fatal("list secret or id")
	}
	if rec := call("POST", "/api/v1/event-subscriptions/test", "admin-secret", map[string]string{"id": result.Subscription.ID}); rec.Code != 202 {
		t.Fatalf("test %d %s", rec.Code, rec.Body)
	}
	deliveries, _ := st.ListEventDeliveries(ctx, result.Subscription.ID, "", 100, 0)
	if len(deliveries) != 1 || deliveries[0].Event.Type != "agentmux.subscription.test" {
		t.Fatal("missing marked test event")
	}
	if rec := call("GET", "/api/v1/event-deliveries?id="+deliveries[0].ID, "admin-secret", nil); rec.Code != 200 {
		t.Fatalf("delivery %d %s", rec.Code, rec.Body)
	}
	sub.CallbackURL = "http://192.0.2.1/"
	if rec := call("POST", "/api/v1/event-subscriptions", "admin-secret", sub); rec.Code != 400 {
		t.Fatal("non-loopback accepted")
	}
}
