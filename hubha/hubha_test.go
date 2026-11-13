package hubha

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/link"
	"github.com/wangning19940904/AgentMux/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("AGENTMUX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("AGENTMUX_TEST_DATABASE_URL is not set")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("amux_hubha_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(dsn)
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	st, err := store.OpenPostgres(context.Background(), store.DatabaseConfig{URL: parsed.String()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = st.Close()
		_, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		_ = admin.Close()
	})
	return st
}

func lease(t *testing.T, controller *Controller, request LeaseRequest) (LeaseResponse, int) {
	t.Helper()
	body, _ := json.Marshal(request)
	r := httptest.NewRequest(http.MethodPost, LeasePath, bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	controller.ServeLease(w, r)
	var response LeaseResponse
	_ = json.Unmarshal(w.Body.Bytes(), &response)
	return response, w.Code
}

func eventually(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestStandbyFollowsOverTheLinkAndTakesOver(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	primary, standby := openStore(t), openStore(t)

	// The primary hub: device endpoints, the feed on the uplink, the lease.
	certDir := t.TempDir()
	cert, fingerprint, err := link.LoadOrCreateCertificate(certDir)
	if err != nil {
		t.Fatal(err)
	}
	tracker := NewTracker()
	primaryCtl := &Controller{Store: primary, Secret: "secret"}
	if err := primaryCtl.Start(ctx); err != nil {
		t.Fatal(err)
	}
	hub := link.NewHub(primary, nil)
	hub.SetGate(primaryCtl.Accepting)
	primaryCtl.OnFence = hub.Close
	identity := func() (Identity, error) {
		certPath, keyPath := link.CertificateFiles(certDir)
		certPEM, err := os.ReadFile(certPath)
		if err != nil {
			return Identity{}, err
		}
		keyPEM, err := os.ReadFile(keyPath)
		return Identity{Cert: certPEM, Key: keyPEM}, err
	}
	hub.SetUplink(NewFeedHandler(primary, identity, tracker, nil))
	mux := http.NewServeMux()
	hub.Register(mux)
	mux.HandleFunc("POST "+LeasePath, primaryCtl.ServeLease)
	server := httptest.NewUnstartedServer(mux)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	server.StartTLS()
	defer server.Close()
	defer hub.Close()

	// The standby pairs as an ordinary device, then is allowed to follow.
	code, err := primary.CreateDevicePairingCode(ctx, "standby")
	if err != nil {
		t.Fatal(err)
	}
	state, err := link.Pair(ctx, link.JoinToken{HubURL: server.URL, Code: code.Code, Fingerprint: fingerprint}, core.DeviceInfo{Name: "standby"})
	if err != nil {
		t.Fatal(err)
	}
	if err := primary.SetDeviceHubRole(ctx, state.DeviceID, "standby"); err != nil {
		t.Fatal(err)
	}

	// The relay makes the first hub primary.
	if response, status := lease(t, primaryCtl, LeaseRequest{Epoch: 1, Active: true, TTLms: 60000}); status != http.StatusOK || response.Role != store.HubRolePrimary {
		t.Fatalf("grant = %d %+v", status, response)
	}

	client := &link.Client{State: state, Info: core.DeviceInfo{Name: "standby"}, Handler: http.NotFoundHandler()}
	go func() { _ = client.Run(ctx) }()
	var savedMu sync.Mutex
	var saved Identity
	follower := &Follower{Sink: standby, Uplink: client.Uplink, SaveIdentity: func(id Identity) error {
		savedMu.Lock()
		saved = id
		savedMu.Unlock()
		return nil
	}}
	standbyCtl := &Controller{Store: standby, Secret: "secret", Follower: follower}
	if err := standbyCtl.Start(ctx); err != nil {
		t.Fatal(err)
	}
	// A standby with nothing yet cannot be promoted.
	if _, status := lease(t, standbyCtl, LeaseRequest{Epoch: 1, Active: true, TTLms: 1000}); status != http.StatusConflict {
		t.Fatalf("empty standby granted: %d", status)
	}
	if response, _ := lease(t, standbyCtl, LeaseRequest{Epoch: 1, Active: false}); response.Role != store.HubRoleStandby {
		t.Fatalf("standby role = %+v", response)
	}
	if standbyCtl.Accepting() == nil {
		t.Fatal("a standby accepts devices")
	}

	// The snapshot brings the standby's own record; changes follow.
	eventually(t, "the snapshot", func() bool {
		device, _ := standby.AuthenticateDevice(ctx, state.Token)
		return device != nil
	})
	if err := primary.RenameDevice(ctx, state.DeviceID, "renamed on the primary"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the rename", func() bool {
		device, _ := standby.GetDevice(ctx, state.DeviceID)
		return device != nil && device.Name == "renamed on the primary"
	})
	eventually(t, "the standby's ack", func() bool { return len(tracker.Standbys()) == 1 })
	savedMu.Lock()
	certPath, _ := link.CertificateFiles(certDir)
	want, _ := os.ReadFile(certPath)
	if !bytes.Equal(saved.Cert, want) {
		t.Fatal("the standby did not receive the primary's certificate")
	}
	savedMu.Unlock()

	// The relay stops renewing: the primary fences itself.
	lease(t, primaryCtl, LeaseRequest{Epoch: 1, Active: true, TTLms: 1000})
	eventually(t, "the primary to fence", func() bool {
		replication, _ := primary.HubReplication(ctx)
		return replication.Role == store.HubRoleFenced
	})
	if primaryCtl.Accepting() == nil {
		t.Fatal("a fenced primary accepts devices")
	}
	if _, err := primary.CreateDevicePairingCode(ctx, "late"); err == nil {
		t.Fatal("a fenced primary wrote hub data")
	}

	// The relay promotes the standby.
	if response, status := lease(t, standbyCtl, LeaseRequest{Epoch: 2, Active: true, TTLms: 60000}); status != http.StatusOK || response.Role != store.HubRolePrimary {
		t.Fatalf("promote = %d %+v", status, response)
	}
	if standbyCtl.Accepting() != nil {
		t.Fatal("the new primary refuses devices")
	}
	if _, err := standby.CreateDevicePairingCode(ctx, "after failover"); err != nil {
		t.Fatal(err)
	}
	// A grant older than what a hub has seen is refused. (The relay keeps
	// its epoch, so it never sends one; a hub cannot know of epochs it was
	// never told about.)
	if _, status := lease(t, primaryCtl, LeaseRequest{Epoch: 0, Active: true, TTLms: 60000}); status != http.StatusConflict {
		t.Fatalf("stale grant = %d", status)
	}
	// Told who is primary now, the old one becomes a standby that resyncs.
	if response, _ := lease(t, primaryCtl, LeaseRequest{Epoch: 2, Active: false}); response.Role != store.HubRoleStandby || response.AppliedSeq != 0 || response.Ready {
		t.Fatalf("old primary after release = %+v", response)
	}
}

func TestTrackerWaitsOnlyForActiveStandbys(t *testing.T) {
	tracker := NewTracker()
	if tracker.WaitFor(context.Background(), 5, time.Second) {
		t.Fatal("no standby, yet the wait succeeded")
	}
	tracker.Ack("dev", 3)
	go func() {
		time.Sleep(50 * time.Millisecond)
		tracker.Ack("dev", 5)
	}()
	if !tracker.WaitFor(context.Background(), 5, 5*time.Second) {
		t.Fatal("the standby's ack was missed")
	}
	if tracker.WaitFor(context.Background(), 9, 100*time.Millisecond) {
		t.Fatal("waited past the timeout")
	}
}
