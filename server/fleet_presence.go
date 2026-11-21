package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	linkpkg "github.com/wangning19940904/AgentMux/link"
	"github.com/wangning19940904/AgentMux/store"
)

// Fleet presence: which machines are connected to the hub right now.
//
// Only the primary hub holds the machines' connections, so only it knows
// which are online. Every other machine (a standby hub or a plain node) asks
// it over its own link and shows the answer beside its machine list.

const (
	fleetUplinkPath  = "/uplink/v1/fleet"
	fleetFetchWait   = 5 * time.Second
	fleetSourceHub   = "hub"
	fleetSourceLocal = "local"
	// fleetHubID stands for a hub that has no device record of its own.
	fleetHubID = "hub"
)

// fleetMachine is one machine as the hub sees it.
type fleetMachine struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Version    string     `json:"version,omitempty"`
	Online     bool       `json:"online"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	ReportedAt *time.Time `json:"reported_at,omitempty"`
	// HubRole is "hub" or "standby" for a hub, empty for other machines.
	HubRole string `json:"hub_role,omitempty"`
	// MachineUID is the computer the machine said it is, so a machine that
	// lists the fleet can tell it from an SSH host to the same computer.
	MachineUID string `json:"machine_uid,omitempty"`
}

// fleetView answers GET /api/v1/fleet.
type fleetView struct {
	// Source is "hub" when the list came from this machine's hub, "local"
	// when this machine is the hub that holds the connections.
	Source    string         `json:"source"`
	FetchedAt time.Time      `json:"fetched_at"`
	Stale     bool           `json:"stale,omitempty"`
	Error     string         `json:"error,omitempty"`
	Machines  []fleetMachine `json:"machines"`
}

// fleetCache keeps the last list a hub sent, for when the hub is away.
type fleetCache struct {
	mu   sync.Mutex
	last *fleetView
	// presence is which machines the hub last said are connected, and
	// presenceAt when it was asked, answered or not.
	presence   map[string]bool
	presenceAt time.Time
}

// hubFleet lists the machines this hub holds, the hub itself included, as
// seen by the device asking (sourceID) or by the administrator (""). A
// device sees only machines of its owner, never itself.
func (s *Server) hubFleet(ctx context.Context, sourceID, ownerID string) ([]fleetMachine, error) {
	if s.devices == nil {
		return nil, errors.New("this machine holds no device links")
	}
	devices, err := s.st.ListDevices(ctx)
	if err != nil {
		return nil, err
	}
	latest := s.devices.lastReports(ctx)
	roles := s.devices.roles(ctx)
	self := s.ownDeviceIDs()
	machines := make([]fleetMachine, 0, len(devices)+1)
	sawSelf := false
	for _, device := range devices {
		if device.ID == sourceID || (sourceID != "" && device.OwnerAccountID != ownerID) {
			continue
		}
		machine := fleetMachine{ID: device.ID, Name: device.Name, Version: device.Version,
			LastSeenAt: device.LastSeenAt, HubRole: roles[device.ID], MachineUID: device.MachineUID}
		if at, ok := latest[device.ID]; ok {
			machine.ReportedAt = &at
		}
		if self[device.ID] {
			sawSelf = true
			machine.Online, machine.Version, machine.HubRole = true, s.version, "hub"
		} else {
			_, machine.Online = s.devices.hub.Connection(device.ID)
		}
		machines = append(machines, machine)
	}
	// A hub without a standby has no device record of its own; the machines
	// asking still need to see it.
	if !sawSelf && sourceID != "" && !self[sourceID] {
		hostname, _ := os.Hostname()
		machines = append(machines, fleetMachine{ID: fleetHubID, Name: strings.TrimSuffix(hostname, ".local"),
			Version: s.version, Online: true, HubRole: "hub"})
	}
	return machines, nil
}

// handleUplinkFleet answers a device asking its hub for the fleet.
func (s *Server) handleUplinkFleet(w http.ResponseWriter, r *http.Request) {
	source, ok := s.relaySource(w, r)
	if !ok {
		return
	}
	machines, err := s.hubFleet(r.Context(), source.ID, source.OwnerAccountID)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, machines)
}

// handleFleet lists every machine of the fleet with whether it is connected
// to the hub, wherever this machine stands in it.
func (s *Server) handleFleet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if uplink := s.HubRelay().Uplink(); uplink != nil && !s.isPrimaryHub(r.Context()) {
		view, err := s.fetchHubFleet(r.Context(), uplink)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, view)
		return
	}
	if s.devices == nil {
		writeJSON(w, http.StatusOK, fleetView{Source: fleetSourceLocal, FetchedAt: time.Now(), Machines: []fleetMachine{}})
		return
	}
	machines, err := s.hubFleet(r.Context(), "", "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, fleetView{Source: fleetSourceLocal, FetchedAt: time.Now(), Machines: s.withoutSelf(machines)})
}

// isPrimaryHub reports whether this machine holds the hub lease. A primary
// may still have a link up to the relay, which only leads back to itself.
func (s *Server) isPrimaryHub(ctx context.Context) bool {
	if s.primaryHubForTest != nil {
		return *s.primaryHubForTest
	}
	if s.hubHA == nil || s.st == nil {
		return false
	}
	state, err := s.st.HubReplication(ctx)
	return err == nil && state.Role == store.HubRolePrimary
}

// fetchHubFleet asks the hub, falling back to its last answer when the hub
// cannot be reached.
func (s *Server) fetchHubFleet(ctx context.Context, uplink http.RoundTripper) (fleetView, error) {
	machines, err := requestHubFleet(ctx, uplink)
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	s.fleet.presenceAt = time.Now()
	if err != nil {
		// Without the hub, nothing is reachable through it.
		s.fleet.presence = nil
		if s.fleet.last == nil {
			return fleetView{}, fmt.Errorf("the hub did not answer: %w", err)
		}
		stale := *s.fleet.last
		stale.Stale, stale.Error = true, err.Error()
		return stale, nil
	}
	s.fleet.presence = make(map[string]bool, len(machines))
	for _, machine := range machines {
		if machine.Online {
			s.fleet.presence[machine.ID] = true
		}
	}
	view := fleetView{Source: fleetSourceHub, FetchedAt: time.Now(), Machines: s.withoutSelf(machines)}
	s.fleet.last = &view
	return view, nil
}

func requestHubFleet(ctx context.Context, uplink http.RoundTripper) ([]fleetMachine, error) {
	ctx, cancel := context.WithTimeout(ctx, fleetFetchWait)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://hub"+fleetUplinkPath, nil)
	if err != nil {
		return nil, err
	}
	response, err := uplink.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return nil, fmt.Errorf("the hub answered HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	var machines []fleetMachine
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&machines); err != nil {
		return nil, err
	}
	return machines, nil
}

// ownDeviceIDs are the device records that are this machine: the one a hub
// made for itself, and its membership of a hub (a primary may hold one that
// leads back to itself through the relay).
func (s *Server) ownDeviceIDs() map[string]bool {
	self := map[string]bool{}
	if id := s.hubPeerDeviceID(); id != "" {
		self[id] = true
	}
	if s.node != nil {
		if path, err := s.node.statePath(); err == nil {
			if state, ok, err := linkpkg.LoadState(path); err == nil && ok && state.DeviceID != "" {
				self[state.DeviceID] = true
			}
		}
	}
	return self
}

// withoutSelf drops this machine, which the machine list shows on its own.
func (s *Server) withoutSelf(machines []fleetMachine) []fleetMachine {
	self := s.ownDeviceIDs()
	out := make([]fleetMachine, 0, len(machines))
	for _, machine := range machines {
		if !self[machine.ID] {
			out = append(out, machine)
		}
	}
	return out
}
