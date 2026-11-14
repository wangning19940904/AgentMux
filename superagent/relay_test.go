package superagent

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/remote"
)

// handlerTransport answers requests with a handler, as the hub would.
type handlerTransport struct{ handler http.Handler }

func (h handlerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	h.handler.ServeHTTP(recorder, request)
	return recorder.Result(), nil
}

type fakeRelay struct{ transport http.RoundTripper }

func (f fakeRelay) Uplink() http.RoundTripper { return f.transport }

// unreachableHosts knows its machines but cannot connect to any of them.
type unreachableHosts map[string]remote.Host

func (u unreachableHosts) Get(id string) (remote.Host, bool) {
	host, ok := u[id]
	return host, ok
}

func (u unreachableHosts) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("device is offline")
}

func fakeHub(t *testing.T, paths *[]string) http.RoundTripper {
	return handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("a credential went up the link: %v", r.Header)
		}
		if r.URL.Path == WorkerRelayDevicesPath {
			_, _ = w.Write([]byte(`[{"id":"dev_box","name":"ecs"}]`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		*paths = append(*paths, r.Method+" "+r.URL.Path+" "+string(body))
		_, _ = w.Write([]byte(`["codex"]`))
	})}
}

func TestAMachineThisOneDoesNotKnowIsReachedThroughTheHub(t *testing.T) {
	var paths []string
	fleet := &Fleet{Relay: fakeRelay{fakeHub(t, &paths)}}
	if name, ok := fleet.MachineName("dev_box"); !ok || name != "ecs" {
		t.Fatalf("name = %q, %v", name, ok)
	}
	runtimes, err := fleet.Runtimes(context.Background(), "dev_box")
	if err != nil || len(runtimes) != 1 || runtimes[0] != "codex" {
		t.Fatalf("runtimes = %v, %v", runtimes, err)
	}
	if len(paths) != 1 || paths[0] != "GET "+WorkerRelayPrefix+"dev_box/api/v1/agents " {
		t.Fatalf("hub saw %q", paths)
	}
	if _, ok := fleet.MachineName("dev_unknown"); ok {
		t.Fatal("a machine the hub does not list resolved")
	}
}

func TestAKnownMachineThisOneCannotReachFallsBackToTheHub(t *testing.T) {
	var paths []string
	fleet := &Fleet{
		Hosts: unreachableHosts{"dev_box": {ID: "dev_box", Name: "ecs", Kind: remote.KindLink, RemoteAddr: "linked-device.agentmux:80"}},
		Relay: fakeRelay{fakeHub(t, &paths)},
	}
	if _, err := fleet.remoteJSON(context.Background(), "dev_box", http.MethodPost, hostedWorkersRemovePath, map[string]string{"owner_id": "sa_1"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || !strings.HasPrefix(paths[0], "POST "+WorkerRelayPrefix+"dev_box"+hostedWorkersRemovePath) || !strings.Contains(paths[0], `"owner_id":"sa_1"`) {
		t.Fatalf("hub saw %q", paths)
	}
}

func TestOnlyTheWorkerAPIIsRelayed(t *testing.T) {
	var paths []string
	fleet := &Fleet{Relay: fakeRelay{fakeHub(t, &paths)}}
	_, err := fleet.remoteJSON(context.Background(), "dev_box", http.MethodGet, "/api/v1/channels", nil, nil)
	if err == nil || len(paths) != 0 {
		t.Fatalf("err = %v, hub saw %q", err, paths)
	}
}

func TestWithoutAHubAnUnknownMachineStaysUnknown(t *testing.T) {
	fleet := &Fleet{Relay: fakeRelay{nil}}
	if _, _, err := fleet.remoteClient("dev_box"); err == nil {
		t.Fatal("expected an error")
	}
}
