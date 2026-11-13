package server

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
	pushpkg "github.com/wangning19940904/AgentMux/push"
	"github.com/wangning19940904/AgentMux/store"
)

type fakePushService struct {
	mu     sync.Mutex
	status int
	sent   []map[string]any
}

func (f *fakePushService) Send(_ context.Context, _ core.PushSubscription, payload []byte, _ pushpkg.SendOptions) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var message map[string]any
	_ = json.Unmarshal(payload, &message)
	f.sent = append(f.sent, message)
	if f.status != 0 {
		return f.status, nil
	}
	return http.StatusCreated, nil
}

func newPushTestServer(t *testing.T, sender pushpkg.Sender) (*Server, *store.Store) {
	t.Helper()
	st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "push.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := config.Default()
	cfg.Remote.HostsFile = filepath.Join(t.TempDir(), "missing-remotes.json")
	cfg.Hub.PublicURL = "https://hub.example.net:8766"
	s := New(Dependencies{Config: cfg, Store: st, PushSender: sender})
	t.Cleanup(s.Push().Close)
	return s, st
}

func browserSubscription(t *testing.T, endpoint string) map[string]any {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secret := make([]byte, 16)
	_, _ = rand.Read(secret)
	return map[string]any{
		"endpoint": endpoint,
		"keys": map[string]string{
			"p256dh": base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
			"auth":   base64.RawURLEncoding.EncodeToString(secret),
		},
		"kinds": []string{core.PushKindWaiting, core.PushKindMachines},
		"label": "iPhone · Safari",
		"lang":  "zh",
	}
}

func TestPushEndpoints(t *testing.T) {
	sender := &fakePushService{}
	s, st := newPushTestServer(t, sender)
	if s.pushSubject() != "https://hub.example.net:8766" {
		t.Fatalf("subject = %q", s.pushSubject())
	}

	var status pushStatus
	rec := doJSON(t, s, http.MethodGet, "/api/v1/push", nil)
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &status) != nil {
		t.Fatalf("status = %d %s", rec.Code, rec.Body)
	}
	if status.PublicKey == "" || status.Subscriber != pushpkg.SubscriberAdmin || len(status.Kinds) != len(core.PushKinds()) || len(status.Subscriptions) != 0 {
		t.Fatalf("status = %+v", status)
	}

	if rec := doJSON(t, s, http.MethodPost, "/api/v1/push/subscriptions", map[string]any{"endpoint": "http://insecure.example/x"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("insecure endpoint = %d %s", rec.Code, rec.Body)
	}
	const endpoint = "https://web.push.apple.com/QGuQyavXutnMH8"
	rec = doJSON(t, s, http.MethodPost, "/api/v1/push/subscriptions", browserSubscription(t, endpoint))
	if rec.Code != http.StatusOK {
		t.Fatalf("subscribe = %d %s", rec.Code, rec.Body)
	}
	// The endpoint and keys are capabilities: never sent back.
	if strings.Contains(rec.Body.String(), "web.push.apple.com") || strings.Contains(rec.Body.String(), "p256dh") {
		t.Fatalf("subscription response leaks the endpoint: %s", rec.Body)
	}
	var sub core.PushSubscription
	_ = json.Unmarshal(rec.Body.Bytes(), &sub)
	if sub.ID != pushpkg.SubscriptionID(endpoint) || sub.Lang != "zh" || len(sub.Kinds) != 2 {
		t.Fatalf("subscription = %+v", sub)
	}

	rec = doJSON(t, s, http.MethodPost, "/api/v1/push/test", map[string]string{"id": sub.ID})
	var tested struct {
		Results []pushpkg.Delivery `json:"results"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &tested) != nil || len(tested.Results) != 1 || !tested.Results[0].Delivered {
		t.Fatalf("test = %d %s", rec.Code, rec.Body)
	}
	if len(sender.sent) != 1 || sender.sent[0]["title"] != "AgentMux 通知已可用" || sender.sent[0]["url"] != "/#settings" {
		t.Fatalf("sent = %+v", sender.sent)
	}

	// A push service that says the subscription is gone removes it.
	sender.status = http.StatusGone
	doJSON(t, s, http.MethodPost, "/api/v1/push/test", nil)
	if got, _ := st.GetPushSubscription(context.Background(), sub.ID); got != nil {
		t.Fatalf("expired subscription kept: %+v", got)
	}

	sender.status = 0
	doJSON(t, s, http.MethodPost, "/api/v1/push/subscriptions", browserSubscription(t, endpoint))
	if rec := doJSON(t, s, http.MethodDelete, "/api/v1/push/subscriptions?id="+sub.ID, nil); rec.Code != http.StatusOK {
		t.Fatalf("unsubscribe = %d %s", rec.Code, rec.Body)
	}
	rec = doJSON(t, s, http.MethodGet, "/api/v1/push", nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &status)
	if len(status.Subscriptions) != 0 {
		t.Fatalf("after unsubscribe = %+v", status.Subscriptions)
	}
}

// A node's pending interaction, reported up its link, is notified by the hub
// with the names from the node's own reports.
func TestHubNotifiesWaitingInteractionsFromNodeReports(t *testing.T) {
	sender := &fakePushService{}
	s, st := newPushTestServer(t, sender)
	ctx := context.Background()
	code, err := st.CreateDevicePairingCode(ctx, "Build box")
	if err != nil {
		t.Fatal(err)
	}
	device, _, err := st.RedeemDevicePairingCode(ctx, code.Code, core.DeviceInfo{Hostname: "box"})
	if err != nil || device == nil {
		t.Fatalf("pair: %+v, %v", device, err)
	}
	report := func(collection, path string, value any) core.DeviceReport {
		data, _ := json.Marshal(value)
		return core.DeviceReport{Collection: collection, Path: path, Shape: core.DeviceReportList,
			Upserts: []core.DeviceReportDoc{{ID: "x", Data: data}}, Order: []string{"x"}}
	}
	now := time.Now()
	for _, r := range []core.DeviceReport{
		report("channels", "/api/v1/channels", core.Channel{ID: "ch1", Name: "Feishu", AgentID: "ag1"}),
		report("agents", "/api/v1/agent-instances", core.AgentInstance{ID: "ag1", Name: "Release bot"}),
	} {
		if err := st.ApplyDeviceReport(ctx, device.ID, r, now); err != nil {
			t.Fatal(err)
		}
	}
	doJSON(t, s, http.MethodPost, "/api/v1/push/subscriptions", browserSubscription(t, "https://fcm.googleapis.com/fcm/send/a"))

	sink := s.push.ReportSink(st, pushDeviceNames{server: s})
	pending := report(pushpkg.PendingInteractionsCollection, "/api/v1/channel-interactions?pending=true",
		core.ChannelInteraction{ID: "int1", ChannelID: "ch1", Status: core.ChannelInteractionPending,
			Request: core.AgentInteraction{Kind: core.AgentInteractionCommandApproval, Command: "rm -rf /secret"}})
	if err := sink.ApplyDeviceReport(ctx, device.ID, pending, now); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the waiting notification", func() bool {
		sender.mu.Lock()
		defer sender.mu.Unlock()
		return len(sender.sent) == 1
	})
	sent := sender.sent[0]
	if sent["title"] != "有 Agent 在等你批准" || sent["body"] != "Release bot · Build box" || sent["url"] != "/#sessions" {
		t.Fatalf("notification = %+v", sent)
	}
	if raw, _ := json.Marshal(sent); strings.Contains(string(raw), "rm -rf") {
		t.Fatalf("notification carries the command: %s", raw)
	}
}
