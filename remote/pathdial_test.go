package remote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// local is a SetLocal dialer that can be made to fail.
type localPath struct {
	handler http.Handler
	failing atomic.Bool
	dials   atomic.Int32
}

func (l *localPath) dial(context.Context) (net.Conn, error) {
	l.dials.Add(1)
	if l.failing.Load() {
		return nil, errors.New("local API down")
	}
	return serveConn(l.handler, "local:"), nil
}

func get(t *testing.T, manager *Manager, id, path string) (string, map[string]any) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, "http://"+linkedRemoteAddr+path, nil)
	response, err := manager.Transport(id).RoundTrip(request)
	if err != nil {
		t.Fatalf("GET %s on %s: %v", path, id, err)
	}
	defer response.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(response.Body).Decode(&body)
	return response.Header.Get("X-Via"), body
}

func TestMachineFailsOverAndMovesBackOnce(t *testing.T) {
	manager, clock := newMachineManager(t)
	devices := newFakeDevices(statusHandler(uidA))
	manager.SetLinkBackend(devices)
	devices.add(LinkedDevice{ID: "dev-self", Name: "this mac", Online: true, MachineUID: uidA})
	local := &localPath{handler: statusHandler(uidA)}
	manager.SetLocal(uidA, local.dial)

	// The paired device is this very machine: the local path wins.
	if via, _ := get(t, manager, "dev-self", "/api/v1/status"); via != "local:" {
		t.Fatalf("first request went through %q", via)
	}
	// The local API fails: the next path is used at once, in the same call.
	local.failing.Store(true)
	if via, _ := get(t, manager, "dev-self", "/api/v1/status"); via != "link:dev-self" {
		t.Fatalf("after the local path failed: %q", via)
	}
	dials := local.dials.Load()
	// While it backs off, the local path is not tried at all.
	if via, _ := get(t, manager, "dev-self", "/api/v1/status"); via != "link:dev-self" || local.dials.Load() != dials {
		t.Fatalf("a backing-off path was tried: via %q, dials %d -> %d", via, dials, local.dials.Load())
	}

	// It works again. Being a better class, it is clearly better, but the
	// link is kept until that has lasted the hold period; then exactly one
	// switch back.
	local.failing.Store(false)
	clock.advance(pathBackoffMin)
	switches := 0
	current := "link:dev-self"
	for elapsed := time.Duration(0); elapsed <= 2*defaultPathHold; elapsed += 10 * time.Second {
		via, _ := get(t, manager, "dev-self", "/api/v1/status")
		if via != current {
			switches++
			if elapsed < defaultPathHold {
				t.Fatalf("moved to %s after %s", via, elapsed)
			}
			current = via
		}
		clock.advance(10 * time.Second)
	}
	if switches != 1 || current != "local:" {
		t.Fatalf("switches = %d, now on %s", switches, current)
	}

	// DialContext and DoHTTP choose the same way.
	conn, err := manager.DialContext(context.Background(), uidA, "tcp")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	request, _ := http.NewRequest(http.MethodGet, "http://"+linkedRemoteAddr+"/api/v1/agents", nil)
	status, body, err := manager.DoHTTP("dev-self", request, 1<<20)
	if err != nil || status != http.StatusOK || !strings.Contains(string(body), `"/api/v1/agents"`) {
		t.Fatalf("DoHTTP = %d %s, %v", status, body, err)
	}
}

func TestMachineStatusChecksEveryPath(t *testing.T) {
	manager, _ := newMachineManager(t)
	devices := newFakeDevices(statusHandler(uidA))
	manager.SetLinkBackend(devices)
	devices.add(LinkedDevice{ID: "dev-self", Name: "this mac", Online: true, MachineUID: uidA})
	local := &localPath{handler: statusHandler(uidA)}
	manager.SetLocal(uidA, local.dial)

	result, err := manager.Status(context.Background(), "dev-self")
	if err != nil || !result.OK || result.Status["machine_uid"] != uidA {
		t.Fatalf("status = %+v, %v", result, err)
	}
	views := manager.List()
	if len(views) != 1 || len(views[0].Paths) != 2 {
		t.Fatalf("views = %+v", views)
	}
	for _, path := range views[0].Paths {
		if !path.OK || path.RTTMS <= 0 {
			t.Fatalf("path after a status check = %+v", path)
		}
		if path.Active != (path.Via == PathLocal) {
			t.Fatalf("active path = %+v", path)
		}
	}

	// The device drops its link: Status still answers, and says which path broke.
	devices.set("dev-self", func(device *LinkedDevice) { device.Online = false })
	if _, err := manager.Status(context.Background(), "dev-self"); err != nil {
		t.Fatalf("status with one path down: %v", err)
	}
	views = manager.List()
	if link := views[0].Paths[0]; link.Via != PathLink || link.OK || link.Error != ErrDeviceOffline.Error() {
		t.Fatalf("offline link path = %+v", link)
	}
	if local := views[0].Paths[1]; local.Via != PathLocal || !local.OK || !local.Active {
		t.Fatalf("local path = %+v", local)
	}
}

// fakeRelay relays requests to a handler, as a hub would.
type fakeRelay struct {
	devices []RelayedDevice
	handler http.Handler
	calls   atomic.Int32
}

func (f *fakeRelay) RelayDevices(context.Context) ([]RelayedDevice, error) { return f.devices, nil }

func (f *fakeRelay) RoundTrip(deviceID string, request *http.Request) (*http.Response, error) {
	f.calls.Add(1)
	recorder := httptest.NewRecorder()
	recorder.Header().Set("X-Via", "relay:"+deviceID)
	f.handler.ServeHTTP(recorder, request)
	return recorder.Result(), nil
}

func TestTransportFallsBackToTheHubsRelay(t *testing.T) {
	manager, _ := newMachineManager(t)
	devices := newFakeDevices(statusHandler(uidA))
	manager.SetLinkBackend(devices)
	addSSHHost(t, manager, "unrelated", "")
	// As on a standby hub: the device is in this machine's records but
	// connected to the other hub, which can relay to it.
	devices.add(LinkedDevice{ID: "dev-remote", Name: "remote", Online: false, MachineUID: uidA})
	relay := &fakeRelay{devices: []RelayedDevice{{ID: "dev-remote", Name: "remote", MachineUID: uidA, Online: true}}, handler: statusHandler(uidA)}
	manager.SetRelayBackend(relay)

	if via, _ := get(t, manager, "dev-remote", "/api/v1/agent-instances"); via != "relay:dev-remote" {
		t.Fatalf("request went through %q", via)
	}
	// Connections cannot travel the relay: DialContext reaches the device as before.
	if _, err := manager.DialContext(context.Background(), "dev-remote", "tcp"); !errors.Is(err, ErrDeviceOffline) {
		t.Fatalf("DialContext over a relay-only path = %v", err)
	}
	// The device connects to this hub: the direct path is clearly better,
	// but the relay that works is kept until the hold period has passed.
	devices.set("dev-remote", func(device *LinkedDevice) { device.Online = true })
	if via, _ := get(t, manager, "dev-remote", "/api/v1/agent-instances"); via != "relay:dev-remote" {
		t.Fatalf("left a working relay at once: %q", via)
	}
	// A request whose body cannot be sent twice goes direct first.
	request, _ := http.NewRequest(http.MethodPost, "http://"+linkedRemoteAddr+"/api/v1/invocations/stream", io.NopCloser(strings.NewReader("{}")))
	response, err := manager.Transport("dev-remote").RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if via := response.Header.Get("X-Via"); via != "link:dev-remote" {
		t.Fatalf("a one-shot body went through %q", via)
	}
}

func TestMachineKnownOneWayIsDialedAsBefore(t *testing.T) {
	manager, _ := newMachineManager(t)
	devices := newFakeDevices(statusHandler(uidA))
	manager.SetLinkBackend(devices)
	devices.add(LinkedDevice{ID: "dev-only", Name: "only", Online: true, MachineUID: uidA})
	local := &localPath{handler: statusHandler(uidB)}
	manager.SetLocal(uidB, local.dial)

	for range 3 {
		conn, err := manager.DialContext(context.Background(), "dev-only", "tcp")
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
	}
	if devices.dialCount("dev-only") != 3 || local.dials.Load() != 0 {
		t.Fatalf("dials: link %d, local %d", devices.dialCount("dev-only"), local.dials.Load())
	}
	// The chooser never saw it, so an offline device fails as before, every
	// time, with no backoff in between.
	devices.set("dev-only", func(device *LinkedDevice) { device.Online = false })
	for range 2 {
		if _, err := manager.DialContext(context.Background(), "dev-only", "tcp"); !errors.Is(err, ErrDeviceOffline) {
			t.Fatalf("offline dial = %v", err)
		}
	}
	if devices.dialCount("dev-only") != 5 {
		t.Fatalf("link dials = %d, want every one tried", devices.dialCount("dev-only"))
	}
	if report := manager.paths.report("link:dev-only"); report.checked {
		t.Fatalf("the chooser tracked a single path: %+v", report)
	}
	views := manager.List()
	if len(views) != 1 || views[0].Paths != nil || views[0].MachineUID != uidA {
		t.Fatalf("views = %+v", views)
	}
}
