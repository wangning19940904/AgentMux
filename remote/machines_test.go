package remote

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

const (
	uidA = "0f8fad5b-d9cb-469f-a165-70867728950e"
	uidB = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	uidC = "16fd2706-8baf-433b-82eb-8c7fada847da"
)

func entryKeys(group *machineGroup) []string {
	keys := make([]string, len(group.entries))
	for index, entry := range group.entries {
		keys[index] = entry.key()
	}
	return keys
}

func TestGroupMachinesByUID(t *testing.T) {
	index := groupMachines([]pathEntry{
		{via: PathSSH, id: "ssh-a", uid: uidA},
		{via: PathSSH, id: "ssh-new"}, // never contacted
		{via: PathLink, id: "dev-a", uid: uidA},
		{via: PathLink, id: "dev-old"}, // too old to report a UID
		{via: PathSSH, id: "ssh-old"},  // the same old node over SSH: not merged
		{via: PathRelay, id: "dev-a", uid: uidA},
		{via: PathRelay, id: "dev-old"},            // the hub's record of dev-old itself
		{via: PathRelay, id: "dev-far", uid: uidC}, // only through the hub: not listed
		{via: PathLocal, uid: uidB},                // this machine, known no other way
	})
	var got [][]string
	for _, group := range index.groups {
		got = append(got, entryKeys(group))
	}
	want := [][]string{
		{"ssh:ssh-a", "link:dev-a", "relay:dev-a"},
		{"ssh:ssh-new"},
		{"link:dev-old", "relay:dev-old"},
		{"ssh:ssh-old"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("groups = %v\nwant %v", got, want)
	}
	for id, wantKey := range map[string]string{"ssh-a": uidA, "dev-a": uidA, uidA: uidA, "dev-old": "device:dev-old", "ssh-old": "ssh:ssh-old"} {
		if group := index.byID[id]; group == nil || group.key != wantKey {
			t.Fatalf("byID[%s] = %+v, want machine %s", id, group, wantKey)
		}
	}
	for _, id := range []string{"dev-far", uidC, uidB} {
		if group := index.byID[id]; group != nil {
			t.Fatalf("%s is listed: %+v", id, group)
		}
	}
	// A device listed first stays the machine's entry; a relay never is.
	index = groupMachines([]pathEntry{
		{via: PathRelay, id: "dev-a", uid: uidA},
		{via: PathLocal, uid: uidA},
		{via: PathLink, id: "dev-a", uid: uidA},
	})
	if len(index.groups) != 1 || index.groups[0].primary().key() != "link:dev-a" {
		t.Fatalf("groups = %+v", index.groups)
	}
}

// fakeDevices is a link backend whose devices answer from an HTTP handler.
type fakeDevices struct {
	mu      sync.Mutex
	devices map[string]*LinkedDevice
	order   []string
	handler http.Handler
	dials   map[string]int
	refuse  map[string]bool
}

func newFakeDevices(handler http.Handler) *fakeDevices {
	return &fakeDevices{devices: map[string]*LinkedDevice{}, handler: handler, dials: map[string]int{}, refuse: map[string]bool{}}
}

func (f *fakeDevices) add(device LinkedDevice) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.devices[device.ID]; !ok {
		f.order = append(f.order, device.ID)
	}
	f.devices[device.ID] = &device
}

func (f *fakeDevices) set(id string, change func(*LinkedDevice)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f.devices[id])
}

func (f *fakeDevices) Devices(context.Context) ([]LinkedDevice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]LinkedDevice, 0, len(f.order))
	for _, id := range f.order {
		out = append(out, *f.devices[id])
	}
	return out, nil
}

func (f *fakeDevices) Device(_ context.Context, id string) (LinkedDevice, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	device, ok := f.devices[id]
	if !ok {
		return LinkedDevice{}, false
	}
	return *device, true
}

func (f *fakeDevices) Dial(_ context.Context, id string) (net.Conn, error) {
	f.mu.Lock()
	f.dials[id]++
	refused := f.refuse[id] || f.devices[id] == nil || !f.devices[id].Online
	f.mu.Unlock()
	if refused {
		return nil, ErrDeviceOffline
	}
	return serveConn(f.handler, "link:"+id), nil
}

func (f *fakeDevices) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.devices, id)
	return nil
}

func (f *fakeDevices) dialCount(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dials[id]
}

// serveConn answers one HTTP connection with handler, marking each response
// with the path it came through.
func serveConn(handler http.Handler, via string) net.Conn {
	client, server := net.Pipe()
	listener := &oneConnListener{conn: server, done: make(chan struct{})}
	go func() {
		_ = (&http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Via", via)
			handler.ServeHTTP(w, r)
		})}).Serve(listener)
	}()
	return client
}

type oneConnListener struct {
	mu   sync.Mutex
	conn net.Conn
	done chan struct{}
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	conn := l.conn
	l.conn = nil
	l.mu.Unlock()
	if conn != nil {
		return conn, nil
	}
	<-l.done
	return nil, net.ErrClosed
}

func (l *oneConnListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.done:
	default:
		close(l.done)
	}
	return nil
}

func (l *oneConnListener) Addr() net.Addr { return &net.TCPAddr{} }

// statusHandler answers like an AgentMux reporting uid.
func statusHandler(uid string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "version": "test", "machine_uid": uid, "path": r.URL.Path})
	})
}

func newMachineManager(t *testing.T) (*Manager, *fakeClock) {
	t.Helper()
	manager, err := NewManager(filepath.Join(t.TempDir(), "hosts.json"), 2*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	clock := &fakeClock{at: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)}
	manager.paths = newPathChooser(clock.now, 0)
	return manager, clock
}

func addSSHHost(t *testing.T, manager *Manager, name, uid string) Host {
	t.Helper()
	view, err := manager.Upsert(Host{
		Name: name, Host: name + ".example", Port: 22, User: "ops", APIToken: "token-" + name,
		HostKeyFingerprint: "SHA256:" + name,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if uid != "" {
		manager.learnMachineUID(view.ID, map[string]any{"machine_uid": uid})
	}
	host, _ := manager.store.Get(view.ID)
	return host
}

func listedIDs(views []HostView) []string {
	ids := make([]string, len(views))
	for index, view := range views {
		ids[index] = view.ID
	}
	return ids
}

func TestListShowsAMachineKnownSeveralWaysOnce(t *testing.T) {
	manager, _ := newMachineManager(t)
	devices := newFakeDevices(statusHandler(uidA))
	manager.SetLinkBackend(devices)

	shared := addSSHHost(t, manager, "builder", uidA)
	lone := addSSHHost(t, manager, "lone", "")
	devices.add(LinkedDevice{ID: "dev-builder", Name: "builder (link)", Online: true, MachineUID: uidA})
	devices.add(LinkedDevice{ID: "dev-old", Name: "old node", Online: true})

	views := manager.List()
	if got := listedIDs(views); !reflect.DeepEqual(got, []string{shared.ID, lone.ID, "dev-old"}) {
		t.Fatalf("listed %v", got)
	}
	merged := views[0]
	if merged.Kind != KindSSH || merged.MachineUID != uidA || len(merged.Paths) != 2 {
		t.Fatalf("merged machine = %+v", merged)
	}
	if merged.Paths[0].Via != PathSSH || merged.Paths[0].HostID != shared.ID || merged.Paths[0].OK ||
		merged.Paths[0].Error != "not checked yet" {
		t.Fatalf("ssh path = %+v", merged.Paths[0])
	}
	if merged.Paths[1].Via != PathLink || merged.Paths[1].HostID != "dev-builder" || !merged.Paths[1].OK {
		t.Fatalf("link path = %+v", merged.Paths[1])
	}

	// Machines known one way are listed exactly as before.
	if !reflect.DeepEqual(views[1], lone.View()) {
		t.Fatalf("lone SSH host = %+v\nwant %+v", views[1], lone.View())
	}
	old, _ := devices.Device(context.Background(), "dev-old")
	if !reflect.DeepEqual(views[2], old.view()) {
		t.Fatalf("old device = %+v\nwant %+v", views[2], old.view())
	}

	// Every entry of the machine, and its UID, still finds it; a request
	// built for the device borrows the SSH host's token, since it may
	// travel over SSH.
	for _, id := range []string{shared.ID, "dev-builder", uidA} {
		host, ok := manager.Get(id)
		if !ok || host.APIToken != shared.APIToken {
			t.Fatalf("Get(%s) = %+v, %v", id, host, ok)
		}
	}
	if host, ok := manager.Get("dev-old"); !ok || host.APIToken != "" {
		t.Fatalf("an unmerged device borrowed a token: %+v", host)
	}
}

func TestMachineRegroupsWhenAnEntryReportsAnotherUID(t *testing.T) {
	manager, _ := newMachineManager(t)
	devices := newFakeDevices(statusHandler(uidA))
	manager.SetLinkBackend(devices)
	host := addSSHHost(t, manager, "vm", uidA)
	devices.add(LinkedDevice{ID: "dev-a", Name: "vm a", Online: true, MachineUID: uidA})
	devices.add(LinkedDevice{ID: "dev-b", Name: "vm b", Online: true, MachineUID: uidB})
	if got := listedIDs(manager.List()); !reflect.DeepEqual(got, []string{host.ID, "dev-b"}) {
		t.Fatalf("listed %v", got)
	}

	// The SSH host now reaches the other computer, as after its address was
	// reused: it moves to that machine.
	manager.learnMachineUID(host.ID, map[string]any{"machine_uid": uidB})
	views := manager.List()
	if got := listedIDs(views); !reflect.DeepEqual(got, []string{host.ID, "dev-a"}) {
		t.Fatalf("listed %v after the change", got)
	}
	if views[0].MachineUID != uidB || len(views[0].Paths) != 2 || views[0].Paths[1].HostID != "dev-b" {
		t.Fatalf("regrouped machine = %+v", views[0])
	}
	if views[1].MachineUID != uidA || views[1].Paths != nil {
		t.Fatalf("machine left behind = %+v", views[1])
	}

	// An AgentMux that stops reporting a UID is no longer merged with anything.
	manager.learnMachineUID(host.ID, map[string]any{"version": "old"})
	if got := listedIDs(manager.List()); !reflect.DeepEqual(got, []string{host.ID, "dev-a", "dev-b"}) {
		t.Fatalf("listed %v after the UID went away", got)
	}
	if stored, _ := manager.store.Get(host.ID); stored.MachineUID != "" {
		t.Fatalf("stored UID = %q", stored.MachineUID)
	}
}

func TestSSHHostKeepsItsUIDOnlyWhileItsAddressStays(t *testing.T) {
	manager, _ := newMachineManager(t)
	host := addSSHHost(t, manager, "box", uidA)
	host.Name, host.User = "box renamed", "admin"
	if _, err := manager.Upsert(host, false); err != nil {
		t.Fatal(err)
	}
	if stored, _ := manager.store.Get(host.ID); stored.MachineUID != uidA {
		t.Fatalf("UID after a rename = %q", stored.MachineUID)
	}
	host.Host = "elsewhere.example"
	if _, err := manager.Upsert(host, false); err != nil {
		t.Fatal(err)
	}
	if stored, _ := manager.store.Get(host.ID); stored.MachineUID != "" {
		t.Fatalf("UID after a new address = %q", stored.MachineUID)
	}
	// It survives a restart of the Manager.
	manager.learnMachineUID(host.ID, map[string]any{"machine_uid": uidB})
	reopened, err := NewStore(manager.store.path)
	if err != nil {
		t.Fatal(err)
	}
	if stored, _ := reopened.Get(host.ID); stored.MachineUID != uidB {
		t.Fatalf("UID after reopening = %q", stored.MachineUID)
	}
}
