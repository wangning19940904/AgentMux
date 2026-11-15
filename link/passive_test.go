package link

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

const testMachineUID = "6f1c1d0e-3c7b-4e8e-9a51-2b7f0c9e4d21"

// passiveRegistry is a hub's record of devices, both kinds.
type passiveRegistry struct {
	*fakeRegistry
	byID  map[string]*core.Device
	links map[string]core.PassiveLink
}

func newPassiveRegistry() *passiveRegistry {
	return &passiveRegistry{fakeRegistry: newFakeRegistry(), byID: map[string]*core.Device{}, links: map[string]core.PassiveLink{}}
}

func (r *passiveRegistry) add(device *core.Device, link core.PassiveLink) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[device.ID] = device
	link.DeviceID = device.ID
	r.links[device.ID] = link
}

func (r *passiveRegistry) remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byID, id)
	delete(r.links, id)
}

func (r *passiveRegistry) PassiveLinks(context.Context) ([]core.PassiveLink, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []core.PassiveLink
	for _, link := range r.links {
		out = append(out, link)
	}
	return out, nil
}

func (r *passiveRegistry) GetDevice(_ context.Context, id string) (*core.Device, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byID[id], nil
}

func (r *passiveRegistry) PassiveDeviceForMachine(_ context.Context, uid string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id := range r.links {
		if r.byID[id] != nil && r.byID[id].MachineUID == uid {
			return id, nil
		}
	}
	return "", nil
}

func (r *passiveRegistry) seenInfo(id string) core.DeviceInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seen[id]
}

// startPassiveNode listens on a loopback port with a key in a fresh state
// directory. Its API answers with the path asked for.
func startPassiveNode(t *testing.T) (*PassiveNode, string) {
	t.Helper()
	dir := t.TempDir()
	node := &PassiveNode{
		Dir:  dir,
		Info: core.DeviceInfo{Name: "listener", Hostname: "listener.local", Version: "test", MachineUID: testMachineUID},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "node answered %s", r.URL.Path)
		}),
		Reports: []Report{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { node.Close(); cancel() })
	if err := node.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if err := node.Serve("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	return node, dir
}

func newInvite(t *testing.T, node *PassiveNode) Invite {
	t.Helper()
	invite, err := node.Invite([]string{node.ListenAddr()}, "")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseInvite(invite.String())
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

// pairPassive does what the hub's add route does: ask, register, pair.
func pairPassive(t *testing.T, registry *passiveRegistry, invite Invite, id string) core.PassiveLink {
	t.Helper()
	ctx := context.Background()
	info, addr, err := PassiveInfo(ctx, invite)
	if err != nil {
		t.Fatal(err)
	}
	credential := NewPassiveCredential()
	if _, err := PassivePair(ctx, invite, addr, credential, id, "the listener"); err != nil {
		t.Fatal(err)
	}
	link := core.PassiveLink{Addrs: invite.Addrs, Fingerprint: invite.Fingerprint, Credential: credential}
	registry.add(&core.Device{ID: id, Name: "the listener", MachineUID: info.MachineUID}, link)
	link.DeviceID = id
	return link
}

func runDialer(t *testing.T, hub *Hub, registry *passiveRegistry) *PassiveDialer {
	t.Helper()
	dialer := hub.NewPassiveDialer(registry)
	dialer.gateInterval = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); dialer.Run(ctx) }()
	t.Cleanup(func() { cancel(); hub.Close(); <-done })
	return dialer
}

func TestPassiveIdentityIsPrivateAndStable(t *testing.T) {
	dir := t.TempDir()
	_, first, err := LoadOrCreatePassiveIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, again, err := LoadOrCreatePassiveIdentity(dir)
	if err != nil || again != first {
		t.Fatalf("fingerprint changed: %s then %s (%v)", first, again, err)
	}
	if _, err := NormalizeFingerprint(first); err != nil {
		t.Fatalf("fingerprint %q: %v", first, err)
	}
	for _, name := range []string{passiveKeyFile, passiveCertFile} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("%s mode = %o, want 600", name, mode)
		}
	}
}

func TestInviteRoundTripAndValidation(t *testing.T) {
	invite := Invite{Addrs: []string{"192.0.2.10:8767", "[2001:db8::1]:8767"}, Fingerprint: strings.Repeat("AB", 32), Secret: "s3cret"}
	parsed, err := ParseInvite(invite.String())
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Fingerprint != strings.Repeat("ab", 32) || len(parsed.Addrs) != 2 || parsed.Secret != "s3cret" {
		t.Fatalf("parsed = %+v", parsed)
	}
	for _, bad := range []Invite{
		{Addrs: []string{"192.0.2.10"}, Fingerprint: strings.Repeat("ab", 32), Secret: "s"},
		{Addrs: []string{"192.0.2.10:1"}, Fingerprint: "abc", Secret: "s"},
		{Addrs: []string{"192.0.2.10:1"}, Fingerprint: strings.Repeat("ab", 32)},
		{Fingerprint: strings.Repeat("ab", 32), Secret: "s"},
	} {
		if _, err := ParseInvite(bad.String()); err == nil {
			t.Errorf("invite %+v was accepted", bad)
		}
	}
	if _, err := ParseInvite("amxj_x"); err == nil {
		t.Error("a join token was taken for an invite")
	}
}

func TestPassivePairConnectAndBothDirections(t *testing.T) {
	node, dir := startPassiveNode(t)
	registry := newPassiveRegistry()
	hub := NewHub(registry, nil)
	var presence atomic.Int32
	hub.SetPresenceListener(func(_ string, online bool) {
		if online {
			presence.Add(1)
		}
	})
	// The hub's uplink knows which device asked, as for a device that dials.
	hub.SetUplink(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hub heard %s from %s", r.URL.Path, DeviceID(r.Context()))
	}))
	invite := newInvite(t, node)
	if invite.Fingerprint != node.Fingerprint() {
		t.Fatalf("invite pins %s, node presents %s", invite.Fingerprint, node.Fingerprint())
	}

	info, _, err := PassiveInfo(context.Background(), invite)
	if err != nil {
		t.Fatal(err)
	}
	if info.MachineUID != testMachineUID || info.Name != "listener" {
		t.Fatalf("info = %+v", info)
	}
	pairPassive(t, registry, invite, "dev_p1")

	// The node keeps the credential's hash, never the credential, and
	// nothing it does not need, owner-only.
	path := filepath.Join(dir, passiveStateFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	registry.mu.Lock()
	credential := registry.links["dev_p1"].Credential
	registry.mu.Unlock()
	if bytes.Contains(data, []byte(credential)) || bytes.Contains(data, []byte(invite.Secret)) {
		t.Fatal("the passive state holds a secret in the clear")
	}
	if stat, _ := os.Stat(path); stat.Mode().Perm() != 0o600 {
		t.Fatalf("state mode = %o", stat.Mode().Perm())
	}
	if state := node.State(); !state.Paired() || state.DeviceID != "dev_p1" || state.SecretSHA256 != "" {
		t.Fatalf("node state = %+v", state)
	}

	dialer := runDialer(t, hub, registry)
	waitFor(t, "the hub to connect", func() bool { _, ok := hub.Connection("dev_p1"); return ok })
	waitFor(t, "the node to report connected", func() bool { return node.Status().Connected })
	if status, _ := dialer.Status("dev_p1"); status.State != PassiveConnected {
		t.Fatalf("dial status = %+v", status)
	}
	if presence.Load() != 1 {
		t.Fatalf("presence online reported %d times", presence.Load())
	}
	if got := registry.seenInfo("dev_p1"); got.MachineUID != testMachineUID || got.Version != "test" {
		t.Fatalf("hub recorded info %+v", got)
	}
	if status := node.Status(); status.DeviceID != "dev_p1" || status.Name != "the listener" || status.LastHubAt == nil {
		t.Fatalf("node status = %+v", status)
	}

	// Hub to node, exactly as for a device that dials.
	response, err := hubHTTPClient(hub, "dev_p1").Get("http://node/api/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != "node answered /api/v1/status" {
		t.Fatalf("hub to node: %q", body)
	}
	// Node to hub, up the same connection.
	response, err = node.Uplink().Get("http://hub/uplink/v1/test")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != "hub heard /uplink/v1/test from dev_p1" {
		t.Fatalf("node to hub: %q", body)
	}
}

func TestPassiveSecretWrongReusedAndRateLimited(t *testing.T) {
	node, _ := startPassiveNode(t)
	registry := newPassiveRegistry()
	invite := newInvite(t, node)

	wrong := invite
	wrong.Secret = "not-the-secret"
	if _, _, err := PassiveInfo(context.Background(), wrong); err == nil || !strings.Contains(err.Error(), "wrong") {
		t.Fatalf("info with a wrong secret: %v", err)
	}
	if _, err := PassivePair(context.Background(), wrong, invite.Addrs[0], NewPassiveCredential(), "dev_x", "x"); err == nil {
		t.Fatal("paired with a wrong secret")
	}
	if node.State().Paired() {
		t.Fatal("a wrong secret paired the node")
	}

	pairPassive(t, registry, invite, "dev_p1")
	// The secret is used up: a second hub holding the same invite cannot
	// take the node.
	if _, err := PassivePair(context.Background(), invite, invite.Addrs[0], NewPassiveCredential(), "dev_x", "x"); err == nil ||
		!strings.Contains(err.Error(), "already used") {
		t.Fatalf("reused secret: %v", err)
	}

	// Failures from one source hold it off, even with the right credential
	// afterwards. The two above count; eight more use up ten.
	for range passiveFailures - 2 {
		_, _, _ = PassiveInfo(context.Background(), wrong)
	}
	registry.mu.Lock()
	link := registry.links["dev_p1"]
	registry.mu.Unlock()
	var answer passiveInfoResponse
	err := passiveCall(context.Background(), invite.Addrs[0], invite.Fingerprint, http.MethodGet, PassiveInfoPath, "Bearer "+link.Credential, nil, &answer)
	if !errors.Is(err, errPassiveRateLimited) {
		t.Fatalf("after %d failures: %v", passiveFailures, err)
	}
	// The window passes.
	node.mu.Lock()
	node.now = func() time.Time { return time.Now().Add(passiveFailureSpan + time.Second) }
	node.mu.Unlock()
	if err := passiveCall(context.Background(), invite.Addrs[0], invite.Fingerprint, http.MethodGet, PassiveInfoPath, "Bearer "+link.Credential, nil, &answer); err != nil {
		t.Fatalf("after the window: %v", err)
	}
}

func TestPassiveSecretExpires(t *testing.T) {
	node, _ := startPassiveNode(t)
	invite := newInvite(t, node)
	node.mu.Lock()
	node.now = func() time.Time { return time.Now().Add(PassiveSecretTTL + time.Minute) }
	node.mu.Unlock()
	if _, err := PassivePair(context.Background(), invite, invite.Addrs[0], NewPassiveCredential(), "dev_x", "x"); err == nil {
		t.Fatal("an expired secret paired the node")
	}
}

func TestPassiveFingerprintMismatchIsHard(t *testing.T) {
	node, _ := startPassiveNode(t)
	registry := newPassiveRegistry()
	invite := newInvite(t, node)

	// Pairing with an invite whose key is not the node's never reaches it.
	forged := invite
	forged.Fingerprint = strings.Repeat("0", 64)
	if _, _, err := PassiveInfo(context.Background(), forged); !IsFingerprintMismatch(err) {
		t.Fatalf("info against another key: %v", err)
	}
	if node.State().Paired() {
		t.Fatal("paired despite the mismatch")
	}

	// A paired node whose key changed: the hub stops, it does not connect.
	link := pairPassive(t, registry, invite, "dev_p1")
	link.Fingerprint = strings.Repeat("0", 64)
	registry.add(&core.Device{ID: "dev_p1", Name: "the listener"}, link)
	hub := NewHub(registry, nil)
	dialer := runDialer(t, hub, registry)
	waitFor(t, "the dialer to stop", func() bool {
		status, _ := dialer.Status("dev_p1")
		return status.State == PassiveStopped
	})
	status, _ := dialer.Status("dev_p1")
	if !strings.Contains(status.LastError, "not the pinned") {
		t.Fatalf("status = %+v", status)
	}
	if _, ok := hub.Connection("dev_p1"); ok {
		t.Fatal("connected to a node with another key")
	}
	if node.Status().LastHubAt != nil {
		t.Fatal("the node saw a connection")
	}
}

func TestPassiveRedialsAfterDrop(t *testing.T) {
	node, _ := startPassiveNode(t)
	registry := newPassiveRegistry()
	pairPassive(t, registry, newInvite(t, node), "dev_p1")
	hub := NewHub(registry, nil)
	runDialer(t, hub, registry)
	waitFor(t, "the first connection", func() bool { _, ok := hub.Connection("dev_p1"); return ok })
	first, _ := hub.Connection("dev_p1")

	// The node restarts its listener: the connection drops and the hub
	// dials again on its own.
	addr := node.ListenAddr()
	node.Close()
	waitFor(t, "the connection to drop", func() bool { _, ok := hub.Connection("dev_p1"); return !ok })
	if err := node.Serve(addr); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the hub to dial again", func() bool {
		again, ok := hub.Connection("dev_p1")
		return ok && again.Since.After(first.Since)
	})
	waitFor(t, "the node to report connected", func() bool { return node.Status().Connected })
}

func TestPassiveHubWithoutLeaseDoesNotDial(t *testing.T) {
	node, _ := startPassiveNode(t)
	registry := newPassiveRegistry()
	pairPassive(t, registry, newInvite(t, node), "dev_p1")
	hub := NewHub(registry, nil)
	var active atomic.Bool
	hub.SetGate(func() error {
		if active.Load() {
			return nil
		}
		return errors.New("this hub is a standby")
	})
	dialer := runDialer(t, hub, registry)
	waitFor(t, "the dialer to stand by", func() bool {
		status, _ := dialer.Status("dev_p1")
		return status.State == PassiveStandby
	})
	time.Sleep(200 * time.Millisecond)
	if node.Status().LastHubAt != nil {
		t.Fatal("a hub without the lease dialed the node")
	}
	// It takes over: it dials at once.
	active.Store(true)
	waitFor(t, "the hub to dial after taking over", func() bool { _, ok := hub.Connection("dev_p1"); return ok })
	// It loses the lease again: it is fenced, its connections close
	// (Hub.Close, as OnFence does), and it does not dial again.
	active.Store(false)
	hub.Close()
	waitFor(t, "the connection to close", func() bool { _, ok := hub.Connection("dev_p1"); return !ok })
	waitFor(t, "the dialer to stand by again", func() bool {
		status, _ := dialer.Status("dev_p1")
		return status.State == PassiveStandby
	})
	visits := node.Status().LastHubAt
	time.Sleep(200 * time.Millisecond)
	if again := node.Status().LastHubAt; !again.Equal(*visits) {
		t.Fatal("a fenced hub dialed the node")
	}
}

func TestPassiveNodePairedElsewhereStopsTheHub(t *testing.T) {
	node, _ := startPassiveNode(t)
	registry := newPassiveRegistry()
	pairPassive(t, registry, newInvite(t, node), "dev_p1")
	hub := NewHub(registry, nil)
	dialer := runDialer(t, hub, registry)
	waitFor(t, "the first hub to connect", func() bool { _, ok := hub.Connection("dev_p1"); return ok })

	// Another hub pairs with a new invite: the first hub's credential stops
	// working, its connection closes, and it stops dialing.
	other := newPassiveRegistry()
	pairPassive(t, other, newInvite(t, node), "dev_other")
	waitFor(t, "the first hub to stop", func() bool {
		status, _ := dialer.Status("dev_p1")
		return status.State == PassiveStopped
	})
	status, _ := dialer.Status("dev_p1")
	if status.LastError != ErrPassiveRefused.Error() {
		t.Fatalf("status = %+v", status)
	}
}

func TestPassiveRemovedDeviceIsDropped(t *testing.T) {
	node, _ := startPassiveNode(t)
	registry := newPassiveRegistry()
	pairPassive(t, registry, newInvite(t, node), "dev_p1")
	hub := NewHub(registry, nil)
	dialer := runDialer(t, hub, registry)
	waitFor(t, "the hub to connect", func() bool { _, ok := hub.Connection("dev_p1"); return ok })
	registry.remove("dev_p1")
	hub.Disconnect("dev_p1")
	dialer.Refresh()
	waitFor(t, "the dialer to forget the device", func() bool { _, ok := dialer.Status("dev_p1"); return !ok })
	time.Sleep(100 * time.Millisecond)
	if _, ok := hub.Connection("dev_p1"); ok {
		t.Fatal("a removed device was dialed again")
	}
}

func TestDialingInRefusedForAMachineTheHubDials(t *testing.T) {
	registry := newPassiveRegistry()
	registry.codes["CODE1234"] = true
	registry.add(&core.Device{ID: "dev_p1", MachineUID: testMachineUID}, core.PassiveLink{})
	hub, join := startHub(t, registry)
	join.Code = "CODE1234"
	_, err := Pair(context.Background(), join, core.DeviceInfo{Name: "node", MachineUID: testMachineUID})
	if err == nil || !strings.Contains(err.Error(), "one way at a time") {
		t.Fatalf("pairing a machine the hub dials: %v", err)
	}
	// The code was not used up: another machine can still pair with it.
	if _, err := Pair(context.Background(), join, core.DeviceInfo{Name: "other"}); err != nil {
		t.Fatalf("the code was used up by the refused machine: %v", err)
	}
	_ = hub
}

func TestPassiveNodeAnswersNothingElse(t *testing.T) {
	node, _ := startPassiveNode(t)
	invite := newInvite(t, node)
	client := passiveClient(invite.Addrs[0], invite.Fingerprint)
	for _, path := range []string{"/", "/api/v1/status", ConnectPath, PairPath} {
		response, err := client.Get("https://" + invite.Addrs[0] + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Errorf("%s answered %d", path, response.StatusCode)
		}
	}
	// A connection without the credential is refused before any upgrade.
	request, _ := http.NewRequest(http.MethodGet, "https://"+invite.Addrs[0]+PassiveConnectPath, nil)
	request.Header.Set(headerProtocol, ProtocolVersion)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]string
	_ = json.NewDecoder(response.Body).Decode(&envelope)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("connect without a credential: %d %v", response.StatusCode, envelope)
	}
}
