package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
	linkpkg "github.com/wangning19940904/AgentMux/link"
	remotepkg "github.com/wangning19940904/AgentMux/remote"
)

// A node reports its machine UID to whoever reaches it: the hub it pairs with
// and connects to, and a controller probing its status over SSH.
func TestNodeReportsItsMachineUID(t *testing.T) {
	const nodeToken = "node-bridge-token"
	hub := newLiveServer(t, nil)
	node := newLiveServer(t, func(cfg *config.Config) {
		cfg.Bridge.Enabled, cfg.Bridge.Token = true, nodeToken
	})

	// Before the server runs it has read no UID, and reports none.
	var status map[string]any
	if code := node.call(t, http.MethodGet, "/api/v1/status", nodeToken, nil, &status); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if _, ok := status["machine_uid"]; ok {
		t.Fatalf("status before start = %v", status)
	}

	node.node.run(context.Background())
	uid := node.node.machineUID()
	if !core.ValidMachineUID(uid) {
		t.Fatalf("machine UID = %q", uid)
	}
	if stored, err := linkpkg.LoadOrCreateMachineUID(node.cfg.Hub.StateDir); err != nil || stored != uid {
		t.Fatalf("state directory holds %q, %v", stored, err)
	}
	status = nil
	node.call(t, http.MethodGet, "/api/v1/status", nodeToken, nil, &status)
	if status["machine_uid"] != uid {
		t.Fatalf("status = %v, want machine_uid %s", status, uid)
	}

	deviceID := joinNode(t, hub, node, nodeToken)
	device, err := hub.st.GetDevice(context.Background(), deviceID)
	if err != nil || device == nil || device.MachineUID != uid {
		t.Fatalf("hub's device = %+v, %v", device, err)
	}
	eventually(t, "the device to connect", func() bool { _, ok := hub.linkHub.Connection(deviceID); return ok })

	// Each connection reports it again, so a reset UID reaches the hub.
	reset, err := linkpkg.ResetMachineUID(node.cfg.Hub.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	node.node.stop()
	eventually(t, "the device to disconnect", func() bool { _, ok := hub.linkHub.Connection(deviceID); return !ok })
	node.node.run(context.Background())
	eventually(t, "the hub to record the new UID", func() bool {
		device, _ := hub.st.GetDevice(context.Background(), deviceID)
		return device != nil && device.MachineUID == reset
	})
}

// The hub lists a paired device with its machine UID. When that UID is the
// hub's own, the device is this very machine, and the hub reaches it through
// its own API rather than through the link.
func TestHubReachesADeviceThatIsItselfLocally(t *testing.T) {
	const nodeToken = "node-bridge-token"
	hub := newLiveServer(t, nil)
	node := newLiveServer(t, func(cfg *config.Config) {
		cfg.Bridge.Enabled, cfg.Bridge.Token = true, nodeToken
	})
	// The same state directory contents, as on a cloned VM.
	uid, err := linkpkg.LoadOrCreateMachineUID(hub.cfg.Hub.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(node.cfg.Hub.StateDir, linkpkg.MachineUIDFileName), []byte(uid+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	hub.node.run(ctx)
	node.node.run(ctx)
	deviceID := joinNode(t, hub, node, nodeToken)
	eventually(t, "the device to connect", func() bool { _, ok := hub.linkHub.Connection(deviceID); return ok })

	var hosts []remotepkg.HostView
	hub.call(t, http.MethodGet, "/api/v1/remote/hosts", "", nil, &hosts)
	if len(hosts) != 1 || hosts[0].ID != deviceID || hosts[0].MachineUID != uid || len(hosts[0].Paths) != 2 {
		t.Fatalf("hosts = %+v", hosts)
	}
	// The hub's own API answers; the node's would carry its other version.
	hub.version, node.version = "hub-version", "node-version"
	var status map[string]any
	if code := hub.call(t, http.MethodGet, "/api/v1/remote/proxy/"+deviceID+"/status", "", nil, &status); code != http.StatusOK {
		t.Fatalf("proxied status = %d", code)
	}
	if status["version"] != "hub-version" {
		t.Fatalf("proxied status came from %v", status["version"])
	}
	hub.call(t, http.MethodGet, "/api/v1/remote/hosts", "", nil, &hosts)
	for _, path := range hosts[0].Paths {
		if path.Active != (path.Via == remotepkg.PathLocal) {
			t.Fatalf("paths = %+v", hosts[0].Paths)
		}
	}

	// After a reset the clone is a machine of its own again.
	reset, err := linkpkg.ResetMachineUID(node.cfg.Hub.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	node.node.stop()
	node.node.run(ctx) // as a restart: the UID is read again
	eventually(t, "the hub to list the clone on its own", func() bool {
		var hosts []remotepkg.HostView
		hub.call(t, http.MethodGet, "/api/v1/remote/hosts", "", nil, &hosts)
		return len(hosts) == 1 && hosts[0].MachineUID == reset && hosts[0].Paths == nil
	})
}
