package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/remote"
)

// A machine linked to a hub reaches the other machines linked to it through
// the hub: the hub opens the stream to the other machine, as it does for its
// own Console. Only the worker API travels this way, and only between machines
// with the same owner; the hub enforces both.
const (
	WorkerRelayPrefix      = "/uplink/v1/relay/"
	WorkerRelayDevicesPath = "/uplink/v1/relay-devices"

	relayDevicesTTL = 30 * time.Second
)

// HubRelay is this machine's link to its hub.
type HubRelay interface {
	// Uplink returns a transport for requests up the link, or nil while this
	// machine has no hub.
	Uplink() http.RoundTripper
}

// RelayDevice is another machine the hub can relay to.
type RelayDevice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// RelayAllowed reports whether a worker API call may travel through the hub.
func RelayAllowed(method, path string) bool {
	switch method + " " + path {
	case "GET /api/v1/agents",
		"GET /api/v1/agent-instances",
		"GET " + workerToolsPath,
		"POST " + hostedWorkersPath,
		"POST " + hostedWorkersRemovePath,
		"POST " + steerPath,
		"POST /api/v1/invocations/stream":
		return true
	}
	return false
}

type relayCache struct {
	mu      sync.Mutex
	devices map[string]string
	at      time.Time
}

// relayDevices lists the machines the hub would relay to, cached briefly.
func (f *Fleet) relayDevices() map[string]string {
	if f.Relay == nil {
		return nil
	}
	f.relay.mu.Lock()
	defer f.relay.mu.Unlock()
	if f.relay.devices != nil && time.Since(f.relay.at) < relayDevicesTTL {
		return f.relay.devices
	}
	transport := f.Relay.Uplink()
	if transport == nil {
		return f.relay.devices
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://hub"+WorkerRelayDevicesPath, nil)
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		return f.relay.devices
	}
	defer response.Body.Close()
	var listed []RelayDevice
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&listed) != nil {
		return f.relay.devices
	}
	devices := make(map[string]string, len(listed))
	for _, device := range listed {
		devices[device.ID] = device.Name
	}
	f.relay.devices, f.relay.at = devices, time.Now()
	return devices
}

// relayTransport sends a request for a machine to the hub's relay instead.
type relayTransport struct {
	hostID string
	uplink http.RoundTripper
}

func (t relayTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if !RelayAllowed(request.Method, request.URL.Path) {
		return nil, errors.New("this call cannot be relayed through the hub")
	}
	out := request.Clone(request.Context())
	out.URL = &url.URL{Scheme: "http", Host: "hub", Path: WorkerRelayPrefix + url.PathEscape(t.hostID) + request.URL.Path, RawQuery: request.URL.RawQuery}
	out.Host = "hub"
	out.Header.Del("Authorization")
	if request.GetBody != nil {
		body, err := request.GetBody()
		if err != nil {
			return nil, err
		}
		out.Body = body
	}
	return t.uplink.RoundTrip(out)
}

// dialFailure marks an error from opening the connection, before anything
// was sent, so the request can safely go another way.
type dialFailure struct{ err error }

func (d dialFailure) Error() string { return d.err.Error() }
func (d dialFailure) Unwrap() error { return d.err }

// fallbackTransport tries the direct way first and the hub when it cannot
// connect, as for a machine linked to the hub but not to this one.
type fallbackTransport struct {
	direct http.RoundTripper
	relay  http.RoundTripper
}

func (t fallbackTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.direct.RoundTrip(request)
	var failed dialFailure
	if err == nil || !errors.As(err, &failed) || (request.Body != nil && request.GetBody == nil) || !RelayAllowed(request.Method, request.URL.Path) {
		return response, err
	}
	return t.relay.RoundTrip(request)
}

// directTransport reaches a machine through this machine's own connection.
func (f *Fleet) directTransport(hostID string) *http.Transport {
	return &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			conn, err := f.Hosts.DialContext(ctx, hostID, network)
			if err != nil {
				return nil, dialFailure{err}
			}
			return conn, nil
		},
		DisableKeepAlives: true,
	}
}

// relayedHost stands for a machine reached only through the hub.
func relayedHost(id, name string) remote.Host {
	return remote.Host{ID: id, Name: firstNonEmpty(strings.TrimSpace(name), id), Kind: remote.KindLink, RemoteAddr: "relayed-device.agentmux:80"}
}
