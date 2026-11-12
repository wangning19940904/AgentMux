package link

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

type fakeRegistry struct {
	mu      sync.Mutex
	codes   map[string]bool
	devices map[string]*core.Device // by token
	seen    map[string]core.DeviceInfo
}

func newFakeRegistry(codes ...string) *fakeRegistry {
	registry := &fakeRegistry{codes: map[string]bool{}, devices: map[string]*core.Device{}, seen: map[string]core.DeviceInfo{}}
	for _, code := range codes {
		registry.codes[code] = true
	}
	return registry
}

func (f *fakeRegistry) RedeemDevicePairingCode(_ context.Context, code string, info core.DeviceInfo) (*core.Device, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.codes[code] {
		return nil, "", nil
	}
	delete(f.codes, code)
	device := &core.Device{ID: fmt.Sprintf("dev_%d", len(f.devices)+1), Name: info.Name}
	token := "amxd_" + device.ID
	f.devices[token] = device
	return device, token, nil
}

func (f *fakeRegistry) AuthenticateDevice(_ context.Context, token string) (*core.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.devices[token], nil
}

func (f *fakeRegistry) TouchDevice(_ context.Context, id string, info core.DeviceInfo, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if info != (core.DeviceInfo{}) {
		f.seen[id] = info
	}
	return nil
}

func (f *fakeRegistry) revoke(token string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.devices, token)
}

// startHub serves a hub over TLS with its own certificate, as a hub with no
// domain name does.
func startHub(t *testing.T, registry Registry) (*Hub, JoinToken) {
	t.Helper()
	cert, fingerprint, err := LoadOrCreateCertificate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hub := NewHub(registry, nil)
	server := httptest.NewUnstartedServer(hub.Handler())
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	server.StartTLS()
	t.Cleanup(func() { hub.Close(); server.Close() })
	return hub, JoinToken{HubURL: server.URL, Fingerprint: fingerprint}
}

func runClient(t *testing.T, state State, handler http.Handler) (*Client, context.CancelFunc, <-chan error) {
	t.Helper()
	client := &Client{State: state, Info: core.DeviceInfo{Name: "node", Version: "test"}, Handler: handler}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	t.Cleanup(cancel)
	return client, cancel, done
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func hubHTTPClient(hub *Hub, id string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return hub.Dial(ctx, id)
		},
	}}
}

func TestPairConnectAndProxyThroughLink(t *testing.T) {
	registry := newFakeRegistry("CODE1234")
	hub, join := startHub(t, registry)
	join.Code = "CODE1234"

	// The node's own API: it must see the node's token, never the hub's.
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/stream" {
			flusher := w.(http.Flusher)
			for index := range 3 {
				fmt.Fprintf(w, "chunk %d\n", index)
				flusher.Flush()
				time.Sleep(20 * time.Millisecond)
			}
			return
		}
		fmt.Fprintf(w, "%s %s auth=%q cookie=%q", r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), r.Header.Get("Cookie"))
	}))
	defer api.Close()

	state, err := Pair(context.Background(), join, core.DeviceInfo{Name: "node"})
	if err != nil {
		t.Fatal(err)
	}
	if state.DeviceID == "" || state.Token == "" || state.Fingerprint != join.Fingerprint {
		t.Fatalf("state = %+v", state)
	}
	if _, err := Pair(context.Background(), join, core.DeviceInfo{Name: "again"}); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("second use of a pairing code: err = %v", err)
	}

	client, _, _ := runClient(t, state, LocalAPIHandler(strings.TrimPrefix(api.URL, "http://"), "node-token"))
	waitFor(t, "the device to connect", func() bool { _, ok := hub.Connection(state.DeviceID); return ok })
	waitFor(t, "the node to report connected", func() bool { return client.Status().Connected })

	httpClient := hubHTTPClient(hub, state.DeviceID)
	request, _ := http.NewRequest(http.MethodPost, "http://node/api/v1/agents?x=1", strings.NewReader("{}"))
	request.Header.Set("Authorization", "Bearer hub-admin-token")
	request.Header.Set("Cookie", "session=hub")
	response, err := httpClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if got, want := string(body), `POST /api/v1/agents?x=1 auth="Bearer node-token" cookie=""`; got != want {
		t.Fatalf("proxied request = %s, want %s", got, want)
	}

	// Streams arrive as they are written, not when the response ends.
	response, err = httpClient.Get("http://node/api/v1/stream")
	if err != nil {
		t.Fatal(err)
	}
	first := make([]byte, 8)
	started := time.Now()
	if _, err := io.ReadFull(response.Body, first); err != nil || string(first) != "chunk 0\n" {
		t.Fatalf("first chunk = %q, %v", first, err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("first chunk was buffered")
	}
	rest, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(rest) != "chunk 1\nchunk 2\n" {
		t.Fatalf("rest = %q", rest)
	}

	// Only the API is reachable.
	response, err = httpClient.Get("http://node/")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("non-API path status = %d", response.StatusCode)
	}

	registry.mu.Lock()
	info := registry.seen[state.DeviceID]
	registry.mu.Unlock()
	if info.Name != "node" || info.Version != "test" {
		t.Fatalf("hub recorded info %+v", info)
	}
}

func TestClientReconnectsAndStopsWhenRevoked(t *testing.T) {
	registry := newFakeRegistry("CODE1234")
	hub, join := startHub(t, registry)
	join.Code = "CODE1234"
	state, err := Pair(context.Background(), join, core.DeviceInfo{Name: "node"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, done := runClient(t, state, http.NotFoundHandler())
	waitFor(t, "the device to connect", func() bool { _, ok := hub.Connection(state.DeviceID); return ok })
	first, _ := hub.Connection(state.DeviceID)

	hub.Disconnect(state.DeviceID)
	waitFor(t, "the device to reconnect", func() bool {
		connection, ok := hub.Connection(state.DeviceID)
		return ok && connection.Since.After(first.Since)
	})

	registry.revoke(state.Token)
	hub.Disconnect(state.DeviceID)
	select {
	case err := <-done:
		if !errors.Is(err, ErrRevoked) {
			t.Fatalf("Run = %v, want ErrRevoked", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the client kept retrying after it was revoked")
	}
	if _, err := hub.Dial(context.Background(), state.DeviceID); !IsOffline(err) {
		t.Fatalf("Dial to a revoked device = %v", err)
	}
}

func TestPinnedFingerprintRejectsAnotherHub(t *testing.T) {
	_, join := startHub(t, newFakeRegistry("CODE1234"))
	join.Code = "CODE1234"
	join.Fingerprint = strings.Repeat("0", 64)
	if _, err := Pair(context.Background(), join, core.DeviceInfo{}); err == nil || !strings.Contains(err.Error(), "not the one this device paired with") {
		t.Fatalf("Pair with the wrong fingerprint: err = %v", err)
	}
	// With no fingerprint a self-signed hub is not trusted either.
	join.Fingerprint = ""
	if _, err := Pair(context.Background(), join, core.DeviceInfo{}); err == nil {
		t.Fatal("Pair trusted a self-signed hub without its fingerprint")
	}
}

func TestPairAttemptsAreLimited(t *testing.T) {
	hub := NewHub(newFakeRegistry(), nil)
	now := time.Now()
	for range pairAttempts {
		if !hub.allowPairAttempt("10.0.0.1", now) {
			t.Fatal("attempt refused too early")
		}
	}
	if hub.allowPairAttempt("10.0.0.1", now) {
		t.Fatal("attempt past the limit was allowed")
	}
	if !hub.allowPairAttempt("10.0.0.2", now) {
		t.Fatal("another address was limited")
	}
	if !hub.allowPairAttempt("10.0.0.1", now.Add(pairAttemptSpan+time.Second)) {
		t.Fatal("the limit did not expire")
	}
}

func TestJoinTokenAndStateRoundTrip(t *testing.T) {
	token := JoinToken{HubURL: "https://hub.example:8766", Code: "ABCD2345", Fingerprint: "ab12"}
	parsed, err := ParseJoinToken(" " + token.String() + "\n")
	if err != nil || parsed != token {
		t.Fatalf("parsed = %+v, %v", parsed, err)
	}
	for _, bad := range []string{"", "ABCD2345", joinTokenPrefix + "!!", JoinToken{HubURL: "ftp://x", Code: "c"}.String(), JoinToken{HubURL: "https://x"}.String()} {
		if _, err := ParseJoinToken(bad); err == nil {
			t.Fatalf("ParseJoinToken(%q) succeeded", bad)
		}
	}

	path := t.TempDir() + "/nested/hub-link.json"
	if _, ok, err := LoadState(path); ok || err != nil {
		t.Fatalf("missing state: ok=%v err=%v", ok, err)
	}
	state := State{HubURL: "https://hub.example:8766", DeviceID: "dev_1", Token: "amxd_x", JoinedAt: time.Now().UTC().Truncate(time.Second)}
	if err := SaveState(path, state); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := LoadState(path)
	if err != nil || !ok || loaded != state {
		t.Fatalf("loaded = %+v ok=%v err=%v", loaded, ok, err)
	}
	if err := RemoveState(path); err != nil {
		t.Fatal(err)
	}
	if err := RemoveState(path); err != nil {
		t.Fatalf("removing a missing state: %v", err)
	}
}

func TestCertificateIsStable(t *testing.T) {
	dir := t.TempDir()
	_, first, err := LoadOrCreateCertificate(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := LoadOrCreateCertificate(dir)
	if err != nil || first != second || len(first) != 64 {
		t.Fatalf("fingerprints %q, %q, %v", first, second, err)
	}
}
