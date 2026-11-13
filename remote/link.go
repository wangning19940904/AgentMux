package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Host kinds. An SSH host is reached by this machine dialing out to it. A
// linked device dials this machine instead and stays connected, so it needs
// no address, key or API token here.
const (
	KindSSH  = "ssh"
	KindLink = "link"
)

// linkedRemoteAddr stands in for an address in URLs built for a linked
// device. Nothing resolves it: the connection is a stream on the device's
// link, whatever the URL says.
const linkedRemoteAddr = "linked-device.agentmux:80"

// ErrLinkedDevice is returned by operations that only make sense over SSH.
var ErrLinkedDevice = errors.New("this machine connects to the hub itself; SSH operations do not apply to it")

// LinkedDevice is a paired device as the Manager needs it.
type LinkedDevice struct {
	ID         string
	Name       string
	Version    string
	Online     bool
	LastSeenAt *time.Time
	// ReportedAt is when the device last sent the hub its data.
	ReportedAt *time.Time
}

// LinkBackend supplies the devices paired with this hub.
type LinkBackend interface {
	Devices(ctx context.Context) ([]LinkedDevice, error)
	Device(ctx context.Context, id string) (LinkedDevice, bool)
	Dial(ctx context.Context, id string) (net.Conn, error)
	Remove(ctx context.Context, id string) error
}

// SetLinkBackend makes paired devices appear beside the SSH hosts. Everything
// that reaches a host through the Manager then reaches them too.
func (m *Manager) SetLinkBackend(backend LinkBackend) {
	m.mu.Lock()
	m.links = backend
	m.mu.Unlock()
}

func (m *Manager) linkBackend() LinkBackend {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.links
}

func (d LinkedDevice) host() Host {
	return Host{ID: d.ID, Name: d.Name, Kind: KindLink, RemoteAddr: linkedRemoteAddr}
}

func (d LinkedDevice) view() HostView {
	online := d.Online
	return HostView{
		ID: d.ID, Name: d.Name, Kind: KindLink, RemoteAddr: linkedRemoteAddr,
		Trusted: true, Online: &online, Version: d.Version, LastSeenAt: d.LastSeenAt, ReportedAt: d.ReportedAt,
	}
}

// linked returns the paired device with this id. SSH hosts win a clash of
// ids, which their different id formats rule out in practice.
func (m *Manager) linked(ctx context.Context, id string) (LinkedDevice, bool) {
	backend := m.linkBackend()
	if backend == nil {
		return LinkedDevice{}, false
	}
	return backend.Device(ctx, id)
}

func (m *Manager) dialLinked(ctx context.Context, device LinkedDevice) (net.Conn, error) {
	backend := m.linkBackend()
	if backend == nil {
		return nil, os.ErrNotExist
	}
	// Callers name the machine in their own errors.
	return backend.Dial(ctx, device.ID)
}

func (m *Manager) linkedTransport(device LinkedDevice) *http.Transport {
	return &http.Transport{
		Proxy: nil, DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return m.dialLinked(ctx, device)
		},
	}
}

func (m *Manager) linkedStatus(ctx context.Context, device LinkedDevice) (TestResult, error) {
	started := time.Now()
	transport := m.linkedTransport(device)
	defer transport.CloseIdleConnections()
	status, err := requestStatusVia(ctx, transport, device.host())
	if err != nil {
		return TestResult{}, err
	}
	return TestResult{OK: true, Name: device.Name, LatencyMS: time.Since(started).Milliseconds(), Status: status}, nil
}

// linkedJSON calls the device's own API. A linked device runs AgentMux by
// definition, so what SSH does with shell commands goes through its API.
func (m *Manager) linkedJSON(ctx context.Context, device LinkedDevice, method, path string, body, output any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://"+linkedRemoteAddr+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	status, data, err := m.DoHTTP(device.ID, request, 4<<20)
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		var envelope struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &envelope) == nil && envelope.Error != "" {
			return errors.New(envelope.Error)
		}
		return fmt.Errorf("%s answered HTTP %d", device.Name, status)
	}
	return json.Unmarshal(data, output)
}

func (m *Manager) linkedDirectories(ctx context.Context, device LinkedDevice, path string) (DirectoryListing, error) {
	var listing DirectoryListing
	err := m.linkedJSON(ctx, device, http.MethodGet, "/api/v1/system/directories?path="+url.QueryEscape(path), nil, &listing)
	if listing.Entries == nil {
		listing.Entries = []DirectoryEntry{}
	}
	return listing, err
}

func (m *Manager) linkedEnsureDirectory(ctx context.Context, device LinkedDevice, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("directory path is required")
	}
	var created struct {
		Path string `json:"path"`
	}
	err := m.linkedJSON(ctx, device, http.MethodPost, "/api/v1/system/directories", map[string]string{"path": path}, &created)
	return created.Path, err
}
