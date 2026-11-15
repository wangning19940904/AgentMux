package remote

import (
	"context"
	"errors"
	"math"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// Machines.
//
// The Manager knows computers as entries: SSH hosts, devices paired with this
// hub, devices the hub this machine is linked to can relay to, and this
// machine itself. Every AgentMux reports a machine UID however it is reached,
// so entries with the same UID are one machine with several paths. An entry
// whose UID is unknown, such as an SSH host never contacted or a node too old
// to report one, is a machine of its own.
//
// The machine list shows each machine once, as its first entry (SSH hosts
// before devices), with all of its paths. Dialing any of its entries, or its
// UID, then picks the path (pathChooser). A machine with a single path is
// reached exactly as before, without the chooser.

// Ways of reaching a machine.
const (
	PathLocal = "local"
	PathSSH   = "ssh"
	PathLink  = "link"
	PathRelay = "relay"
)

const (
	machineIndexTTL = 5 * time.Second
	// PathProbeInterval is how often the paths of machines with more than
	// one are checked, so the chooser knows which work and how fast.
	PathProbeInterval = 30 * time.Second
	pathProbeTimeout  = 10 * time.Second
)

// MachinePath is one way of reaching a machine, as the machine list shows it.
type MachinePath struct {
	Via string `json:"via"`
	// HostID is the entry this path uses: an SSH host, a paired device or a
	// device the hub relays to. It is empty for this machine itself.
	HostID string `json:"host_id,omitempty"`
	Name   string `json:"name,omitempty"`
	// OK says the path works as far as is known now.
	OK bool `json:"ok"`
	// RTTMS is the last measured round trip, when there is one.
	RTTMS float64 `json:"rtt_ms,omitempty"`
	// Active marks the path in use.
	Active bool   `json:"active"`
	Error  string `json:"error,omitempty"`
}

// RelayedDevice is a machine the hub this one is linked to can reach for it.
type RelayedDevice struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	MachineUID string `json:"machine_uid,omitempty"`
	Online     bool   `json:"online"`
}

// RelayBackend reaches machines through the hub this machine is linked to.
// Only the hub's worker API travels this way, so a relay path carries HTTP
// requests (Transport), not connections (DialContext).
type RelayBackend interface {
	// RelayDevices lists the machines the hub relays to; none without a hub.
	RelayDevices(ctx context.Context) ([]RelayedDevice, error)
	// RoundTrip sends one request for the device through the hub.
	RoundTrip(deviceID string, request *http.Request) (*http.Response, error)
}

// SetRelayBackend adds the hub's relay as a path to the machines it reaches.
func (m *Manager) SetRelayBackend(backend RelayBackend) {
	m.mu.Lock()
	m.relays = backend
	m.mu.Unlock()
	m.invalidateMachines()
}

// SetLocal tells the Manager which machine it runs on, and how to reach this
// machine's own API. An entry that reaches this machine then has a local
// path, preferred to every other.
func (m *Manager) SetLocal(uid string, dial func(ctx context.Context) (net.Conn, error)) {
	m.mu.Lock()
	m.localUID, m.localDial = core.NormalizeMachineUID(uid), dial
	m.mu.Unlock()
	m.invalidateMachines()
}

func (m *Manager) relayBackend() RelayBackend {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.relays
}

func (m *Manager) local() (string, func(ctx context.Context) (net.Conn, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.localUID, m.localDial
}

// pathEntry is one way of reaching a machine.
type pathEntry struct {
	via    string
	id     string
	uid    string
	host   Host
	device LinkedDevice
	relay  RelayedDevice
}

func (e pathEntry) key() string { return e.via + ":" + e.id }

func (e pathEntry) name() string {
	switch e.via {
	case PathSSH:
		return e.host.Name
	case PathLink:
		return e.device.Name
	case PathRelay:
		return e.relay.Name
	}
	return ""
}

// machineGroup is the entries that reach one computer.
type machineGroup struct {
	// key names the machine to the chooser: its UID, or its only entry.
	key     string
	uid     string
	entries []pathEntry
}

// primary is the entry the machine is listed as.
func (g *machineGroup) primary() pathEntry { return g.entries[0] }

// dialable are the paths that open connections; the relay carries requests
// only.
func (g *machineGroup) dialable() []pathEntry {
	var out []pathEntry
	for _, entry := range g.entries {
		if entry.via != PathRelay {
			out = append(out, entry)
		}
	}
	return out
}

func (g *machineGroup) entry(key string) (pathEntry, bool) {
	for _, entry := range g.entries {
		if entry.key() == key {
			return entry, true
		}
	}
	return pathEntry{}, false
}

// sshToken is the API token of the machine's SSH entry. A request built for
// another of its entries may travel over SSH, where it needs one; the link
// and local paths put the node's own token in its place.
func (g *machineGroup) sshToken() string {
	for _, entry := range g.entries {
		if entry.via == PathSSH && entry.host.APIToken != "" {
			return entry.host.APIToken
		}
	}
	return ""
}

type machineIndex struct {
	groups []*machineGroup
	// byID finds a machine by any entry's id or by its UID.
	byID  map[string]*machineGroup
	built time.Time
}

// groupMachines puts entries with the same UID together. Groups keep the
// order of their first entry. A group with no SSH host or paired device,
// only a relay or only this machine, is not one this Manager lists.
func groupMachines(entries []pathEntry) *machineIndex {
	index := &machineIndex{byID: map[string]*machineGroup{}}
	byKey := map[string]*machineGroup{}
	var order []*machineGroup
	for _, entry := range entries {
		key := entry.uid
		if key == "" {
			// A device the hub relays to is the same record as the paired
			// device with that id, as on a standby hub.
			switch entry.via {
			case PathLink, PathRelay:
				key = "device:" + entry.id
			default:
				key = entry.key()
			}
		}
		group := byKey[key]
		if group == nil {
			group = &machineGroup{key: key, uid: entry.uid}
			byKey[key] = group
			order = append(order, group)
		}
		group.entries = append(group.entries, entry)
	}
	for _, group := range order {
		listed := false
		for _, entry := range group.entries {
			listed = listed || entry.via == PathSSH || entry.via == PathLink
		}
		if !listed {
			continue
		}
		// The listed entry comes first, ahead of a relay or this machine.
		for index, entry := range group.entries {
			if entry.via == PathSSH || entry.via == PathLink {
				group.entries[0], group.entries[index] = group.entries[index], group.entries[0]
				break
			}
		}
		index.groups = append(index.groups, group)
		for _, entry := range group.entries {
			if entry.id != "" && entry.via != PathRelay {
				if _, taken := index.byID[entry.id]; !taken {
					index.byID[entry.id] = group
				}
			}
		}
		if group.uid != "" {
			index.byID[group.uid] = group
		}
	}
	return index
}

// collectEntries lists every entry the Manager knows now. devices are the
// paired devices, already listed by the caller.
func (m *Manager) collectEntries(ctx context.Context, devices []LinkedDevice) []pathEntry {
	var entries []pathEntry
	for _, host := range m.store.List() {
		entries = append(entries, pathEntry{via: PathSSH, id: host.ID, uid: host.MachineUID, host: host})
	}
	for _, device := range devices {
		entries = append(entries, pathEntry{via: PathLink, id: device.ID, uid: core.NormalizeMachineUID(device.MachineUID), device: device})
	}
	if relay := m.relayBackend(); relay != nil {
		relayed, err := relay.RelayDevices(ctx)
		if err != nil && m.log != nil {
			m.log.Debug("list relayed devices failed", "err", err)
		}
		for _, device := range relayed {
			entries = append(entries, pathEntry{via: PathRelay, id: device.ID, uid: core.NormalizeMachineUID(device.MachineUID), relay: device})
		}
	}
	if uid, dial := m.local(); uid != "" && dial != nil {
		entries = append(entries, pathEntry{via: PathLocal, uid: uid})
	}
	return entries
}

// listDevices lists the paired devices, and whether that worked.
func (m *Manager) listDevices(ctx context.Context) ([]LinkedDevice, bool) {
	backend := m.linkBackend()
	if backend == nil {
		return nil, true
	}
	devices, err := backend.Devices(ctx)
	if err != nil {
		if m.log != nil {
			m.log.Warn("list paired devices failed", "err", err)
		}
		return devices, false
	}
	return devices, true
}

// buildMachines groups the entries known now. A complete result is kept for
// a few seconds, for the dials that follow.
func (m *Manager) buildMachines(ctx context.Context) *machineIndex {
	devices, complete := m.listDevices(ctx)
	index := groupMachines(m.collectEntries(ctx, devices))
	index.built = time.Now()
	if complete {
		m.machinesMu.Lock()
		m.machines = index
		m.machinesMu.Unlock()
	}
	return index
}

// machineIndex returns the machines, grouped at most a few seconds ago. The
// grouping outlives the caller's request, so it does not end with it.
func (m *Manager) machineIndex(ctx context.Context) *machineIndex {
	m.machinesMu.Lock()
	index := m.machines
	m.machinesMu.Unlock()
	if index != nil && time.Since(index.built) < machineIndexTTL {
		return index
	}
	buildCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.timeout)
	defer cancel()
	return m.buildMachines(buildCtx)
}

func (m *Manager) invalidateMachines() {
	m.machinesMu.Lock()
	m.machines = nil
	m.machinesMu.Unlock()
}

// machine finds the machine an entry id or UID belongs to.
func (m *Manager) machine(ctx context.Context, id string) (*machineGroup, bool) {
	group, ok := m.machineIndex(ctx).byID[strings.TrimSpace(id)]
	return group, ok
}

// multiPath returns the machine id belongs to when it can be dialed more than
// one way. Everything else is reached as it always was.
func (m *Manager) multiPath(ctx context.Context, id string) (*machineGroup, bool) {
	group, ok := m.machine(ctx, id)
	if !ok || len(group.dialable()) < 2 {
		return nil, false
	}
	return group, true
}

// resolveID turns a machine UID into the id of the entry it is listed as;
// any other id is returned as it is.
func (m *Manager) resolveID(ctx context.Context, id string) string {
	if core.ValidMachineUID(id) {
		if group, ok := m.machine(ctx, id); ok {
			return group.primary().id
		}
	}
	return id
}

// machineView is a machine as the list shows it.
func (m *Manager) machineView(ctx context.Context, group *machineGroup) HostView {
	primary := group.primary()
	var view HostView
	if primary.via == PathSSH {
		view = primary.host.View()
	} else {
		view = primary.device.view()
	}
	view.MachineUID = group.uid
	if len(group.entries) > 1 {
		view.Paths = m.pathViews(ctx, group)
	}
	return view
}

// pathAvailable says why a path cannot be tried now, or "" when it can.
func (m *Manager) pathAvailable(ctx context.Context, entry pathEntry) string {
	switch entry.via {
	case PathSSH:
		if entry.host.HostKeyFingerprint == "" {
			return "SSH host key is not trusted; test the connection first"
		}
	case PathLink:
		// The connection comes and goes faster than the list is grouped.
		device, ok := m.linked(ctx, entry.id)
		if !ok || !device.Online {
			return ErrDeviceOffline.Error()
		}
	case PathRelay:
		if m.relayBackend() == nil || !entry.relay.Online {
			return "offline at the hub"
		}
	case PathLocal:
		if _, dial := m.local(); dial == nil {
			return "this machine's API is not available"
		}
	}
	return ""
}

// ErrDeviceOffline is how a paired device that is not connected is reported.
var ErrDeviceOffline = errors.New("device is offline")

func (m *Manager) pathViews(ctx context.Context, group *machineGroup) []MachinePath {
	active := m.paths.activePath(group.key)
	out := make([]MachinePath, 0, len(group.entries))
	for _, entry := range group.entries {
		report := m.paths.report(entry.key())
		path := MachinePath{Via: entry.via, HostID: entry.id, Name: entry.name(), Active: entry.key() == active}
		if report.rtt > 0 {
			// Tenths of a millisecond; a measured round trip never reads as none.
			path.RTTMS = max(math.Round(float64(report.rtt)/float64(time.Millisecond)*10)/10, 0.1)
		}
		unavailable := m.pathAvailable(ctx, entry)
		switch {
		case unavailable != "":
			path.Error = unavailable
		case report.backingOff || (report.checked && !report.ok):
			path.Error = report.err
		case entry.via == PathSSH && !report.checked:
			path.Error = "not checked yet"
		default:
			path.OK = true
		}
		out = append(out, path)
	}
	return out
}

// learnMachineUID records the computer an SSH host reached, from the status
// its AgentMux answered. A host that now reaches another computer, or one too
// old to say, moves to where it belongs the next time machines are grouped.
func (m *Manager) learnMachineUID(hostID string, status map[string]any) {
	if status == nil {
		return
	}
	value, _ := status["machine_uid"].(string)
	changed, err := m.store.SetMachineUID(hostID, core.NormalizeMachineUID(value))
	if err != nil {
		if m.log != nil {
			m.log.Warn("record machine UID failed", "host", hostID, "err", err)
		}
		return
	}
	if changed {
		m.invalidateMachines()
	}
}
