package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
	linkpkg "github.com/wangning19940904/AgentMux/link"
	remotepkg "github.com/wangning19940904/AgentMux/remote"
	"github.com/wangning19940904/AgentMux/store"
)

// liveServer runs a test Server behind a real listener, as a daemon does.
type liveServer struct {
	*Server
	url string
}

func newLiveServer(t *testing.T, configure func(*config.Config)) liveServer {
	t.Helper()
	s, _ := newTestServer(t)
	s.cfg.Hub.StateDir = t.TempDir()
	s.version = "9.9.9-test"
	if configure != nil {
		configure(s.cfg)
	}
	listener := httptest.NewServer(s.withAuth(s.mux))
	t.Cleanup(listener.Close)
	t.Cleanup(s.node.stop)
	if s.linkHub != nil {
		t.Cleanup(s.linkHub.Close)
	}
	s.cfg.Server.Addr = strings.TrimPrefix(listener.URL, "http://")
	return liveServer{Server: s, url: listener.URL}
}

func (l liveServer) call(t *testing.T, method, path, token string, body, output any) int {
	t.Helper()
	var payload bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&payload).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	request, err := http.NewRequest(method, l.url+path, &payload)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	if output != nil && response.StatusCode < 300 {
		if err := json.Unmarshal(data, output); err != nil {
			t.Fatalf("%s %s: decode %q: %v", method, path, data, err)
		}
	}
	if response.StatusCode >= 300 {
		t.Logf("%s %s -> %d %s", method, path, response.StatusCode, strings.TrimSpace(string(data)))
	}
	return response.StatusCode
}

func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A node with no address the hub could dial joins it, and the hub then
// reaches the node through everything that reaches an SSH host.
func TestNodeJoinsHubAndIsReachedLikeASSHHost(t *testing.T) {
	const nodeToken = "node-bridge-token"
	hub := newLiveServer(t, nil)
	node := newLiveServer(t, func(cfg *config.Config) {
		cfg.Bridge.Enabled, cfg.Bridge.Token = true, nodeToken
	})

	var pairing pairingCodeResponse
	if code := hub.call(t, http.MethodPost, "/api/v1/devices/pairing-codes", "", map[string]any{"name": "build box", "hub_url": hub.url}, &pairing); code != http.StatusOK {
		t.Fatalf("pairing code status = %d", code)
	}
	if pairing.JoinToken == "" || pairing.Fingerprint != "" || !strings.HasPrefix(pairing.Command, "amux join amxj_") {
		t.Fatalf("pairing = %+v", pairing)
	}

	// Joining is the node administrator's decision.
	if code := node.call(t, http.MethodPost, "/api/v1/link/join", "", map[string]any{"token": pairing.JoinToken}, nil); code != http.StatusUnauthorized {
		t.Fatalf("join without the node's token = %d", code)
	}
	var joined struct {
		DeviceID string `json:"device_id"`
		Name     string `json:"name"`
	}
	if code := node.call(t, http.MethodPost, "/api/v1/link/join", nodeToken, map[string]any{"token": pairing.JoinToken}, &joined); code != http.StatusOK {
		t.Fatalf("join status = %d", code)
	}
	if joined.DeviceID == "" || joined.Name != "build box" {
		t.Fatalf("joined = %+v", joined)
	}
	if code := node.call(t, http.MethodPost, "/api/v1/link/join", nodeToken, map[string]any{"token": pairing.JoinToken}, nil); code != http.StatusConflict {
		t.Fatalf("second join = %d, want conflict", code)
	}
	statePath, _ := linkpkg.StatePath(node.cfg.Hub.StateDir)
	if info, err := os.Stat(statePath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("membership file: %v, %v", info, err)
	}

	eventually(t, "the hub to see the device online", func() bool {
		var listed struct {
			Devices []deviceView `json:"devices"`
		}
		hub.call(t, http.MethodGet, "/api/v1/devices", "", nil, &listed)
		return len(listed.Devices) == 1 && listed.Devices[0].Online && listed.Devices[0].Version == "9.9.9-test"
	})
	var linkStatus linkStatusResponse
	node.call(t, http.MethodGet, "/api/v1/link", nodeToken, nil, &linkStatus)
	if !linkStatus.Joined || !linkStatus.Connected || linkStatus.HubURL != hub.url {
		t.Fatalf("node link status = %+v", linkStatus)
	}

	// The machine list, the fleet and the proxy all see the device.
	var hosts []remotepkg.HostView
	hub.call(t, http.MethodGet, "/api/v1/remote/hosts", "", nil, &hosts)
	if len(hosts) != 1 || hosts[0].ID != joined.DeviceID || hosts[0].Kind != remotepkg.KindLink || !hosts[0].Trusted || hosts[0].Online == nil || !*hosts[0].Online {
		t.Fatalf("hosts = %+v", hosts)
	}
	var targets []machineTarget
	hub.call(t, http.MethodGet, "/api/v1/remote/fleet/targets", "", nil, &targets)
	if len(targets) != 2 || targets[1].Kind != remotepkg.KindLink || !targets[1].Online || targets[1].Version != "9.9.9-test" {
		t.Fatalf("fleet targets = %+v", targets)
	}
	var batch fleetBatchResponse
	query := map[string]any{"target_ids": []string{joined.DeviceID}, "requests": []map[string]string{{"key": "status", "path": "/api/v1/status"}}}
	if code := hub.call(t, http.MethodPost, "/api/v1/remote/fleet/query", "", query, &batch); code != http.StatusOK {
		t.Fatalf("fleet query status = %d", code)
	}
	if len(batch.Targets) != 1 || len(batch.Targets[0].Responses) != 1 || !batch.Targets[0].Responses[0].OK ||
		!strings.Contains(string(batch.Targets[0].Responses[0].Data), "9.9.9-test") {
		t.Fatalf("fleet query = %+v", batch)
	}
	// The node's API demands its own token; the hub never had it, so a 200
	// here means the node added it.
	var status map[string]any
	if code := hub.call(t, http.MethodGet, "/api/v1/remote/proxy/"+joined.DeviceID+"/status", "", nil, &status); code != http.StatusOK || status["version"] != "9.9.9-test" {
		t.Fatalf("proxied status = %d %+v", code, status)
	}

	// What SSH does with shell commands goes through the node's API.
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	var listing remotepkg.DirectoryListing
	if code := hub.call(t, http.MethodGet, "/api/v1/remote/directories?id="+joined.DeviceID+"&path="+root, "", nil, &listing); code != http.StatusOK {
		t.Fatalf("directory listing status = %d", code)
	}
	if len(listing.Entries) != 1 || listing.Entries[0].Name != "repo" {
		t.Fatalf("listing = %+v", listing)
	}
	if code := hub.call(t, http.MethodPost, "/api/v1/remote/hosts/update?id="+joined.DeviceID, "", nil, nil); code < 400 {
		t.Fatalf("an SSH update of a linked device succeeded: %d", code)
	}

	// The Super Agent fleet reaches workers through the same dialer.
	dialer := hub.RemoteHosts()
	host, ok := dialer.Get(joined.DeviceID)
	if !ok || host.Kind != remotepkg.KindLink {
		t.Fatalf("worker host = %+v, %v", host, ok)
	}
	conn, err := dialer.DialContext(context.Background(), joined.DeviceID, "tcp")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	// A restarted node reconnects from its membership file.
	first, _ := hub.linkHub.Connection(joined.DeviceID)
	node.node.stop()
	eventually(t, "the hub to see the device offline", func() bool { _, ok := hub.linkHub.Connection(joined.DeviceID); return !ok })
	hub.call(t, http.MethodGet, "/api/v1/remote/fleet/targets", "", nil, &targets)
	if targets[1].Online || targets[1].Error != "device is offline" {
		t.Fatalf("offline target = %+v", targets[1])
	}
	node.node.run(context.Background())
	eventually(t, "the device to reconnect", func() bool {
		connection, ok := hub.linkHub.Connection(joined.DeviceID)
		return ok && connection.Since.After(first.Since)
	})

	// Removing the machine on the hub unpairs it for good.
	if code := hub.call(t, http.MethodDelete, "/api/v1/remote/hosts?id="+joined.DeviceID, "", nil, nil); code != http.StatusOK {
		t.Fatalf("remove status = %d", code)
	}
	eventually(t, "the node to learn it was removed", func() bool {
		node.call(t, http.MethodGet, "/api/v1/link", nodeToken, nil, &linkStatus)
		return linkStatus.Revoked && !linkStatus.Connected
	})
	hub.call(t, http.MethodGet, "/api/v1/remote/hosts", "", nil, &hosts)
	if len(hosts) != 0 {
		t.Fatalf("hosts after removal = %+v", hosts)
	}

	if code := node.call(t, http.MethodPost, "/api/v1/link/leave", nodeToken, nil, nil); code != http.StatusOK {
		t.Fatalf("leave status = %d", code)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("membership file after leaving: %v", err)
	}
}

func TestLinkRoutesAreClosedToMembersAndPairingIsOpenToDevices(t *testing.T) {
	for _, path := range []string{"/api/v1/devices/owner", "/api/v1/link", "/api/v1/link/join", "/api/v1/link/leave"} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
			if memberRouteAllowed(method, path) {
				t.Fatalf("a member may %s %s", method, path)
			}
		}
	}
	// A device has no bridge token. With the bridge on, it must still be able
	// to pair, and a wrong code must be the only thing refused.
	hub := newLiveServer(t, func(cfg *config.Config) {
		cfg.Bridge.Enabled, cfg.Bridge.Token = true, "hub-admin"
	})
	if code := hub.call(t, http.MethodPost, "/api/v1/devices/pairing-codes", "", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("minting a code without the admin token = %d", code)
	}
	if code := hub.call(t, http.MethodPost, linkpkg.PairPath, "", map[string]any{"code": "WRONG234"}, nil); code != http.StatusUnauthorized {
		t.Fatalf("pairing with a wrong code = %d", code)
	}
	var pairing pairingCodeResponse
	if code := hub.call(t, http.MethodPost, "/api/v1/devices/pairing-codes", "hub-admin", nil, &pairing); code != http.StatusOK {
		t.Fatalf("minting a code = %d", code)
	}
	var paired struct {
		Token string `json:"token"`
	}
	if code := hub.call(t, http.MethodPost, linkpkg.PairPath, "", map[string]any{"code": pairing.Code}, &paired); code != http.StatusOK || paired.Token == "" {
		t.Fatalf("pairing = %d, token set=%v", code, paired.Token != "")
	}
}

func TestHubJoinAddress(t *testing.T) {
	s, _ := newTestServer(t)
	s.cfg.Hub.StateDir = t.TempDir()
	yes, no := true, false

	// With no hub listener the management port is all a device could dial.
	address, err := s.hubJoinAddress("", nil)
	if err != nil || address.url != "http://127.0.0.1:8765" || address.fingerprint != "" || !strings.Contains(address.warning, "only works on this machine") {
		t.Fatalf("default address = %+v, %v", address, err)
	}

	// The hub's own listener pins its certificate, so no domain is needed.
	s.cfg.Hub.Addr, s.cfg.Hub.TLS = "10.1.2.3:8766", config.HubTLSAuto
	address, err = s.hubJoinAddress("", nil)
	if err != nil || address.url != "https://10.1.2.3:8766" || len(address.fingerprint) != 64 || address.warning != "" {
		t.Fatalf("hub listener address = %+v, %v", address, err)
	}
	again, _ := s.hubJoinAddress("", nil)
	if again.fingerprint != address.fingerprint {
		t.Fatal("the hub certificate changed between two join tokens")
	}

	s.cfg.Hub.TLS = config.HubTLSOff
	address, err = s.hubJoinAddress("", nil)
	if err != nil || address.url != "http://10.1.2.3:8766" || address.fingerprint != "" || !strings.Contains(address.warning, "unencrypted") {
		t.Fatalf("plain hub listener = %+v, %v", address, err)
	}

	// A proxy with a real certificate in front: nothing to pin.
	s.cfg.Hub.TLS, s.cfg.Hub.PublicURL = config.HubTLSAuto, "https://hub.example.com/"
	address, err = s.hubJoinAddress("", nil)
	if err != nil || address.url != "https://hub.example.com" || address.fingerprint != "" || address.warning != "" {
		t.Fatalf("public URL = %+v, %v", address, err)
	}
	// A forwarded port still shows the hub's own certificate.
	s.cfg.Hub.PinCertificate = true
	if address, err = s.hubJoinAddress("", nil); err != nil || address.fingerprint == "" {
		t.Fatalf("pinned public URL = %+v, %v", address, err)
	}

	// One token can name another way in, such as a tunnel for one machine.
	if address, err = s.hubJoinAddress("https://127.0.0.1:9443", &yes); err != nil || address.url != "https://127.0.0.1:9443" || address.fingerprint == "" {
		t.Fatalf("override = %+v, %v", address, err)
	}
	if address, err = s.hubJoinAddress("https://127.0.0.1:9443", &no); err != nil || address.fingerprint != "" {
		t.Fatalf("unpinned override = %+v, %v", address, err)
	}
	if _, err = s.hubJoinAddress("http://127.0.0.1:9443", &yes); err == nil {
		t.Fatal("a pinned http address was accepted")
	}
	if _, err = s.hubJoinAddress("ftp://x", nil); err == nil {
		t.Fatal("a non-http hub address was accepted")
	}
}

// joinNode pairs node with hub and returns the device id the hub gave it.
func joinNode(t *testing.T, hub, node liveServer, nodeToken string) string {
	t.Helper()
	var pairing pairingCodeResponse
	if code := hub.call(t, http.MethodPost, "/api/v1/devices/pairing-codes", "", map[string]any{"hub_url": hub.url}, &pairing); code != http.StatusOK {
		t.Fatalf("pairing code status = %d", code)
	}
	var joined struct {
		DeviceID string `json:"device_id"`
	}
	if code := node.call(t, http.MethodPost, "/api/v1/link/join", nodeToken, map[string]any{"token": pairing.JoinToken, "name": "reporting-node"}, &joined); code != http.StatusOK {
		t.Fatalf("join status = %d", code)
	}
	return joined.DeviceID
}

// A node keeps its own database and tells the hub what it holds. The hub
// then answers for the node while the node is offline, from its own copy.
func TestHubKeepsWhatANodeReportsAndAnswersWhileItIsOffline(t *testing.T) {
	const nodeToken = "node-bridge-token"
	hub := newLiveServer(t, nil)
	node := newLiveServer(t, func(cfg *config.Config) {
		cfg.Bridge.Enabled, cfg.Bridge.Token = true, nodeToken
	})
	node.node.reportInterval = 25 * time.Millisecond
	ctx := context.Background()
	agent := func(id, name string) {
		if err := node.st.UpsertAgentInstance(ctx, &core.AgentInstance{ID: id, Name: name, RuntimeID: "claudecode", Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	agent("agent-1", "first")
	deviceID := joinNode(t, hub, node, nodeToken)

	names := func(payload []byte) string {
		var items []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(payload, &items); err != nil {
			return "invalid: " + string(payload)
		}
		var out []string
		for _, item := range items {
			out = append(out, item.Name)
		}
		return strings.Join(out, ",")
	}
	reported := func() string {
		payload, _, ok, err := hub.st.DeviceReportedResponse(ctx, deviceID, "/api/v1/agent-instances")
		if err != nil || !ok {
			return ""
		}
		return names(payload)
	}
	eventually(t, "the node's agents to reach the hub", func() bool { return reported() == "first" })

	// A change on the node reaches the hub's copy without anyone asking.
	agent("agent-2", "second")
	eventually(t, "the new agent to reach the hub", func() bool { return strings.Contains(reported(), "second") && strings.Contains(reported(), "first") })

	var summary struct {
		Collections []core.DeviceCollection `json:"collections"`
	}
	hub.call(t, http.MethodGet, "/api/v1/devices/reports?device_id="+deviceID, "", nil, &summary)
	found := map[string]int{}
	for _, collection := range summary.Collections {
		found[collection.Collection] = collection.Items
	}
	if found["agents"] != 2 || found["status"] != 1 {
		t.Fatalf("hub collections = %+v", found)
	}
	var nodeStatus linkStatusResponse
	node.call(t, http.MethodGet, "/api/v1/link", nodeToken, nil, &nodeStatus)
	if len(nodeStatus.Reports) == 0 {
		t.Fatalf("the node does not say what it reported: %+v", nodeStatus)
	}

	query := map[string]any{"target_ids": []string{deviceID}, "requests": []map[string]string{
		{"key": "agents", "path": "/api/v1/agent-instances"},
		{"key": "unreported", "path": "/api/v1/memory"},
	}}
	fleet := func() (agents, unreported fleetOperationResult) {
		var batch fleetBatchResponse
		if code := hub.call(t, http.MethodPost, "/api/v1/remote/fleet/query", "", query, &batch); code != http.StatusOK {
			t.Fatalf("fleet query status = %d", code)
		}
		return batch.Targets[0].Responses[0], batch.Targets[0].Responses[1]
	}
	// While the node is connected it answers for itself.
	live, _ := fleet()
	if !live.OK || live.ReportedAt != nil || !strings.Contains(names(live.Data), "second") {
		t.Fatalf("live answer = %+v", live)
	}

	// The node goes away, having changed something the hub never heard of.
	node.node.stop()
	eventually(t, "the hub to see the node offline", func() bool { _, ok := hub.linkHub.Connection(deviceID); return !ok })
	agent("agent-3", "unseen")

	stale, unreported := fleet()
	if !stale.OK || stale.ReportedAt == nil || names(stale.Data) != reported() || strings.Contains(names(stale.Data), "unseen") {
		t.Fatalf("offline answer = %+v", stale)
	}
	// What the node never reported stays an honest failure.
	if unreported.OK || !strings.Contains(unreported.Error, "offline") {
		t.Fatalf("an unreported path was answered: %+v", unreported)
	}

	// One machine selected in the Console goes through the proxy.
	request, _ := http.NewRequest(http.MethodGet, hub.url+"/api/v1/remote/proxy/"+deviceID+"/agent-instances", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get(reportedAtHeader) == "" || names(body) != reported() {
		t.Fatalf("offline proxy = %d %q %s", response.StatusCode, response.Header.Get(reportedAtHeader), body)
	}
	// A write is never answered from the copy.
	if code := hub.call(t, http.MethodPost, "/api/v1/remote/proxy/"+deviceID+"/agent-instances", "", map[string]any{}, nil); code != http.StatusBadGateway {
		t.Fatalf("offline write = %d", code)
	}

	// Back online, the node answers for itself again and the hub catches up.
	node.node.run(ctx)
	eventually(t, "the hub to catch up after the node returns", func() bool { return strings.Contains(reported(), "unseen") })
	live, _ = fleet()
	if !live.OK || live.ReportedAt != nil || !strings.Contains(names(live.Data), "unseen") {
		t.Fatalf("answer after reconnect = %+v", live)
	}

	// Unpairing removes the hub's copy.
	if code := hub.call(t, http.MethodDelete, "/api/v1/devices?id="+deviceID, "", nil, nil); code != http.StatusOK {
		t.Fatalf("remove status = %d", code)
	}
	if reported() != "" {
		t.Fatal("the hub kept an unpaired node's data")
	}
}

func TestDeviceDialWaitsOnlyForDevicesExpectedBack(t *testing.T) {
	st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "reconnect.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	pair := func(name string) string {
		code, err := st.CreateDevicePairingCode(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		device, _, err := st.RedeemDevicePairingCode(ctx, code.Code, core.DeviceInfo{Name: name})
		if err != nil || device == nil {
			t.Fatal(err)
		}
		return device.ID
	}
	recent, stale, never := pair("recent"), pair("stale"), pair("never")
	if err := st.TouchDevice(ctx, recent, core.DeviceInfo{}, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := st.TouchDevice(ctx, stale, core.DeviceInfo{}, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	backend := &deviceBackend{st: st, hub: linkpkg.NewHub(st, nil)}
	time.Sleep(10 * time.Millisecond)
	if !backend.expectingReconnect(ctx, recent) {
		t.Fatal("a device connected a minute ago is not expected back after a start")
	}
	if backend.expectingReconnect(ctx, stale) || backend.expectingReconnect(ctx, never) {
		t.Fatal("a device gone long before the start is expected back")
	}
	// One that connected after the start and left is simply offline.
	left := pair("left")
	if err := st.TouchDevice(ctx, left, core.DeviceInfo{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if backend.expectingReconnect(ctx, left) {
		t.Fatal("a device that left after the start is expected back")
	}
	// Waiting is bounded by the request; an absent device still fails.
	short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := backend.Dial(short, recent); !linkpkg.IsOffline(err) {
		t.Fatalf("dial err = %v, want offline", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("the wait ignored the request's deadline")
	}
}

func TestHubOwnRecordIsHiddenAndHubsAreLabelled(t *testing.T) {
	st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "peer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	ids := map[string]string{}
	for _, name := range []string{"laptop", "standby-mac", "this-mac"} {
		code, err := st.CreateDevicePairingCode(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		device, _, err := st.RedeemDevicePairingCode(ctx, code.Code, core.DeviceInfo{Name: name})
		if err != nil || device == nil {
			t.Fatal(err)
		}
		ids[name] = device.ID
	}
	manager, err := remotepkg.NewManager(filepath.Join(t.TempDir(), "hosts.json"), time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{st: st, remote: manager}
	s.linkHub = linkpkg.NewHub(st, nil)
	s.devices = &deviceBackend{st: st, hub: s.linkHub,
		self:     func() string { return ids["this-mac"] },
		hubRoles: func(context.Context) map[string]string { return map[string]string{ids["standby-mac"]: "standby"} }}
	s.remote.SetLinkBackend(s.devices)

	views := map[string]remotepkg.HostView{}
	for _, view := range s.remote.List() {
		views[view.Name] = view
	}
	if _, ok := views["this-mac"]; ok {
		t.Fatalf("this hub's own record is listed as another machine: %+v", views)
	}
	if views["standby-mac"].HubRole != "standby" || views["laptop"].HubRole != "" {
		t.Fatalf("hub labels = %+v", views)
	}
	if _, ok := s.devices.Device(ctx, ids["this-mac"]); ok {
		t.Fatal("this hub's own record resolves as a machine")
	}
	targets, err := s.resolveFleetTargets([]string{"all"})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		if target.ID == ids["this-mac"] {
			t.Fatal("this hub's own record is a fleet target")
		}
	}
}
