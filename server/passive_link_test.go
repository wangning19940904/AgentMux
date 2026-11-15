package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
	remotepkg "github.com/wangning19940904/AgentMux/remote"
)

// A node the hub can reach but which cannot reach the hub listens; the hub
// adds it from its invite, dials it, and then reaches it exactly as a node
// that dials in.
func TestHubDialsANodeThatListens(t *testing.T) {
	const nodeToken = "node-bridge-token"
	hub := newLiveServer(t, nil)
	node := newLiveServer(t, func(cfg *config.Config) {
		cfg.Bridge.Enabled, cfg.Bridge.Token = true, nodeToken
	})
	node.node.loadMachineUID()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	hub.startPassiveLink(ctx)
	t.Cleanup(func() {
		if node.node.passive != nil {
			node.node.passive.Close()
		}
	})

	// Listening is the node administrator's decision.
	if code := node.call(t, http.MethodPost, "/api/v1/link/passive", "", map[string]any{"listen": "127.0.0.1:0"}, nil); code != http.StatusUnauthorized {
		t.Fatalf("listen without the node's token = %d", code)
	}
	var invited struct {
		Invite      string   `json:"invite"`
		Addrs       []string `json:"addrs"`
		Fingerprint string   `json:"fingerprint"`
		Command     string   `json:"command"`
	}
	if code := node.call(t, http.MethodPost, "/api/v1/link/passive", nodeToken, map[string]any{"listen": "127.0.0.1:0"}, &invited); code != http.StatusOK {
		t.Fatalf("listen status = %d", code)
	}
	if !strings.HasPrefix(invited.Invite, "amxi_") || len(invited.Addrs) != 1 || !strings.HasPrefix(invited.Addrs[0], "127.0.0.1:") ||
		len(invited.Fingerprint) != 64 || invited.Command != "amux hub add "+invited.Invite {
		t.Fatalf("invite = %+v", invited)
	}

	// While it listens, it cannot also dial a hub.
	var pairing pairingCodeResponse
	hub.call(t, http.MethodPost, "/api/v1/devices/pairing-codes", "", map[string]any{"hub_url": hub.url}, &pairing)
	if code := node.call(t, http.MethodPost, "/api/v1/link/join", nodeToken, map[string]any{"token": pairing.JoinToken}, nil); code != http.StatusConflict {
		t.Fatalf("joining a hub while listening = %d", code)
	}

	// The same machine already dialing this hub: refused before the invite
	// is used up.
	uid := node.node.machineUID()
	code, err := hub.st.CreateDevicePairingCode(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	outbound, _, err := hub.st.RedeemDevicePairingCode(ctx, code.Code, core.DeviceInfo{Name: "same box", MachineUID: uid})
	if err != nil {
		t.Fatal(err)
	}
	if code := hub.call(t, http.MethodPost, "/api/v1/devices/passive", "", map[string]any{"invite": invited.Invite}, nil); code != http.StatusConflict {
		t.Fatalf("adding a machine that dials in = %d", code)
	}
	if err := hub.st.DeleteDevice(ctx, outbound.ID); err != nil {
		t.Fatal(err)
	}

	var added struct {
		Device   core.Device       `json:"device"`
		Replaced bool              `json:"replaced"`
		Online   bool              `json:"online"`
		Passive  passiveDeviceView `json:"passive"`
	}
	if code := hub.call(t, http.MethodPost, "/api/v1/devices/passive", "", map[string]any{"invite": invited.Invite, "name": "listening box"}, &added); code != http.StatusOK {
		t.Fatalf("add status = %d", code)
	}
	if added.Device.Name != "listening box" || added.Device.MachineUID != uid || added.Replaced || !added.Online {
		t.Fatalf("added = %+v", added)
	}
	id := added.Device.ID
	// The invite is used up.
	if code := hub.call(t, http.MethodPost, "/api/v1/devices/passive", "", map[string]any{"invite": invited.Invite}, nil); code != http.StatusBadGateway {
		t.Fatalf("adding with a used invite = %d", code)
	}

	var listed struct {
		Devices []deviceView `json:"devices"`
	}
	hub.call(t, http.MethodGet, "/api/v1/devices", "", nil, &listed)
	if len(listed.Devices) != 1 || !listed.Devices[0].Online || listed.Devices[0].LinkMode != "passive" || listed.Devices[0].Passive == nil ||
		listed.Devices[0].Passive.State != "connected" || listed.Devices[0].Version != "9.9.9-test" {
		t.Fatalf("devices = %+v", listed.Devices)
	}
	var linkStatus linkStatusResponse
	node.call(t, http.MethodGet, "/api/v1/link", nodeToken, nil, &linkStatus)
	if linkStatus.Joined || linkStatus.Passive == nil || !linkStatus.Passive.Paired || !linkStatus.Passive.Connected || linkStatus.Passive.DeviceID != id {
		t.Fatalf("node link status = %+v %+v", linkStatus, linkStatus.Passive)
	}

	// From here on nothing knows who dialed: the machine list, the proxy and
	// the worker dialer reach it as any linked device.
	var hosts []remotepkg.HostView
	hub.call(t, http.MethodGet, "/api/v1/remote/hosts", "", nil, &hosts)
	if len(hosts) != 1 || hosts[0].ID != id || hosts[0].Kind != remotepkg.KindLink || hosts[0].LinkMode != "passive" ||
		hosts[0].Online == nil || !*hosts[0].Online || hosts[0].MachineUID != uid {
		t.Fatalf("hosts = %+v", hosts)
	}
	var status map[string]any
	if code := hub.call(t, http.MethodGet, "/api/v1/remote/proxy/"+id+"/status", "", nil, &status); code != http.StatusOK || status["version"] != "9.9.9-test" {
		t.Fatalf("proxied status = %d %+v", code, status)
	}
	conn, err := hub.RemoteHosts().DialContext(context.Background(), id, "tcp")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	// The node's uplink travels up the connection the hub opened.
	if node.node.uplink() == nil {
		t.Fatal("the node has no uplink")
	}

	// Removing the device stops the hub dialing it.
	if code := hub.call(t, http.MethodDelete, "/api/v1/devices?id="+id, "", nil, nil); code != http.StatusOK {
		t.Fatalf("remove status = %d", code)
	}
	eventually(t, "the hub to drop the device", func() bool { _, ok := hub.linkHub.Connection(id); return !ok })
	eventually(t, "the dialer to forget it", func() bool { _, ok := hub.passiveDialer.Status(id); return !ok })

	// Stopping on the node forgets the pairing.
	if code := node.call(t, http.MethodDelete, "/api/v1/link/passive", nodeToken, nil, nil); code != http.StatusOK {
		t.Fatalf("stop status = %d", code)
	}
	node.call(t, http.MethodGet, "/api/v1/link", nodeToken, nil, &linkStatus)
	if linkStatus.Passive == nil || linkStatus.Passive.Listening || linkStatus.Passive.Paired {
		t.Fatalf("after stopping = %+v", linkStatus.Passive)
	}
}

func TestPassiveAddIsTheAdministratorsAlone(t *testing.T) {
	if memberRouteAllowed(http.MethodPost, "/api/v1/devices/passive") || memberRouteAllowed(http.MethodPost, "/api/v1/link/passive") {
		t.Fatal("a member may add a listening node or make this machine listen")
	}
}
