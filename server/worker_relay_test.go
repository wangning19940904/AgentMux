package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	linkpkg "github.com/wangning19940904/AgentMux/link"
	remotepkg "github.com/wangning19940904/AgentMux/remote"
	"github.com/wangning19940904/AgentMux/store"
	"github.com/wangning19940904/AgentMux/superagent"
)

// pipeLinks is a link backend whose devices answer with one handler each.
type pipeLinks struct {
	st       *store.Store
	handlers map[string]http.Handler
}

func (p pipeLinks) Devices(ctx context.Context) ([]remotepkg.LinkedDevice, error) {
	var devices []remotepkg.LinkedDevice
	for id := range p.handlers {
		device, _ := p.Device(ctx, id)
		devices = append(devices, device)
	}
	return devices, nil
}

func (p pipeLinks) Device(ctx context.Context, id string) (remotepkg.LinkedDevice, bool) {
	if _, ok := p.handlers[id]; !ok {
		return remotepkg.LinkedDevice{}, false
	}
	device, _ := p.st.GetDevice(ctx, id)
	return remotepkg.LinkedDevice{ID: id, Name: device.Name, Online: true}, true
}

func (p pipeLinks) Dial(_ context.Context, id string) (net.Conn, error) {
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		request, err := http.ReadRequest(bufio.NewReader(server))
		if err != nil {
			return
		}
		recorder := httptest.NewRecorder()
		p.handlers[id].ServeHTTP(recorder, request)
		_ = recorder.Result().Write(server)
	}()
	return client, nil
}

func (p pipeLinks) Remove(context.Context, string) error { return nil }

func TestWorkerRelayReachesAnotherMachineOfTheSameOwner(t *testing.T) {
	st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	ids := map[string]string{}
	for _, name := range []string{"work-mac", "dev-box", "someone-elses"} {
		code, err := st.CreateDevicePairingCode(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		device, _, err := st.RedeemDevicePairingCode(ctx, code.Code, core.DeviceInfo{Name: name})
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = device.ID
	}
	other, err := st.CreateAccount(ctx, store.NewAccount{Login: "bob", Password: "test-only-password"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetDeviceOwner(ctx, ids["someone-elses"], other.ID); err != nil {
		t.Fatal(err)
	}
	var seen *http.Request
	answer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r
		body, _ := io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"echo":` + string(body) + `}`))
	})
	manager, err := remotepkg.NewManager(filepath.Join(t.TempDir(), "hosts.json"), time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.SetLinkBackend(pipeLinks{st: st, handlers: map[string]http.Handler{
		ids["work-mac"]: answer, ids["dev-box"]: answer, ids["someone-elses"]: answer,
	}})
	s := &Server{st: st, remote: manager}
	mux := http.NewServeMux()
	s.registerWorkerRelay(mux)
	call := func(from, method, path, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer must-not-pass")
		if from != "" {
			request = request.WithContext(linkpkg.WithDeviceID(request.Context(), ids[from]))
		}
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		return recorder
	}

	launch := superagent.WorkerRelayPrefix + ids["dev-box"] + "/api/v1/super-agents/workers/launch"
	if got := call("work-mac", http.MethodPost, launch, `{"owner_id":"sa_1"}`); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"owner_id":"sa_1"`) {
		t.Fatalf("relayed launch = %d %s", got.Code, got.Body)
	}
	if seen.URL.Path != "/api/v1/super-agents/workers/launch" || seen.Header.Get("Authorization") != "" || seen.Header.Get("X-AgentMux-Relayed-From") != "work-mac" {
		t.Fatalf("the device saw %s %v", seen.URL.Path, seen.Header)
	}
	if got := call("", http.MethodPost, launch, `{}`); got.Code != http.StatusUnauthorized {
		t.Fatalf("a request not on a link = %d", got.Code)
	}
	if got := call("work-mac", http.MethodPost, superagent.WorkerRelayPrefix+ids["dev-box"]+"/api/v1/channels", `{}`); got.Code != http.StatusForbidden {
		t.Fatalf("a non-worker path = %d", got.Code)
	}
	if got := call("work-mac", http.MethodGet, superagent.WorkerRelayPrefix+ids["someone-elses"]+"/api/v1/agents", ""); got.Code != http.StatusNotFound {
		t.Fatalf("another owner's machine = %d", got.Code)
	}

	listed := call("work-mac", http.MethodGet, superagent.WorkerRelayDevicesPath, "")
	var devices []superagent.RelayDevice
	if err := json.Unmarshal(listed.Body.Bytes(), &devices); err != nil || len(devices) != 1 || devices[0].Name != "dev-box" {
		t.Fatalf("relay devices = %s", listed.Body)
	}
}
