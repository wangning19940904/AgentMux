package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"time"

	remotepkg "github.com/wangning19940904/AgentMux/remote"
	"github.com/wangning19940904/AgentMux/superagent"
)

// Paths to machines.
//
// The remote Manager lists each computer once, with every way of reaching it
// (remote/machines.go). This file gives it the path only the server knows:
// the relay of the hub this machine is linked to.

const relayPathsTTL = 30 * time.Second

// startMachinePaths gives the remote Manager the relay of the hub this
// machine is linked to, as another path to the machines it reaches. It runs
// once, when the server starts.
func (n *nodeLink) startMachinePaths(context.Context) {
	s := n.server
	if s.remote == nil {
		return
	}
	n.mu.Lock()
	started := n.pathsStarted
	n.pathsStarted = true
	n.mu.Unlock()
	if started {
		return
	}
	s.remote.SetRelayBackend(&hubRelayPaths{node: n})
}

// relayedDevice is a device as the hub's relay list describes it. Hubs
// before machine UIDs leave out machine_uid and online.
type relayedDevice struct {
	superagent.RelayDevice
	MachineUID string `json:"machine_uid,omitempty"`
	Online     *bool  `json:"online,omitempty"`
}

// hubRelayPaths reaches machines through the relay of the hub this machine
// is linked to (server/worker_relay.go on the hub).
type hubRelayPaths struct {
	node *nodeLink

	mu      sync.Mutex
	devices []remotepkg.RelayedDevice
	at      time.Time
}

func (h *hubRelayPaths) RelayDevices(ctx context.Context) ([]remotepkg.RelayedDevice, error) {
	client := h.node.uplink()
	h.mu.Lock()
	defer h.mu.Unlock()
	if client == nil {
		h.devices, h.at = nil, time.Time{}
		return nil, nil
	}
	if h.devices != nil && time.Since(h.at) < relayPathsTTL {
		return h.devices, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://hub"+superagent.WorkerRelayDevicesPath, nil)
	response, err := client.Do(request)
	if err != nil {
		return h.devices, err
	}
	defer response.Body.Close()
	var listed []relayedDevice
	if response.StatusCode != http.StatusOK {
		return h.devices, errors.New("the hub answered " + response.Status)
	}
	if err := json.NewDecoder(response.Body).Decode(&listed); err != nil {
		return h.devices, err
	}
	devices := make([]remotepkg.RelayedDevice, 0, len(listed))
	for _, device := range listed {
		// A hub that does not say is taken at its word that it can relay.
		online := device.Online == nil || *device.Online
		devices = append(devices, remotepkg.RelayedDevice{ID: device.ID, Name: device.Name, MachineUID: device.MachineUID, Online: online})
	}
	h.devices, h.at = devices, time.Now()
	return devices, nil
}

func (h *hubRelayPaths) RoundTrip(deviceID string, request *http.Request) (*http.Response, error) {
	client := h.node.uplink()
	if client == nil {
		if request.Body != nil {
			_ = request.Body.Close()
		}
		return nil, errors.New("this machine is not linked to a hub")
	}
	out := request.Clone(request.Context())
	out.URL = &url.URL{Scheme: "http", Host: "hub", Path: superagent.WorkerRelayPrefix + url.PathEscape(deviceID) + request.URL.Path, RawQuery: request.URL.RawQuery}
	out.Host = "hub"
	out.Header.Del("Authorization")
	return client.Transport.RoundTrip(out)
}
