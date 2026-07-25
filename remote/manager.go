package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

type cachedClient struct {
	host   Host
	client *ssh.Client
}

// Manager owns persisted profiles and reusable SSH connections.
type Manager struct {
	store   *Store
	log     *slog.Logger
	timeout time.Duration
	mu      sync.Mutex
	clients map[string]*cachedClient
}

type TestResult struct {
	OK                 bool           `json:"ok"`
	Name               string         `json:"name"`
	LatencyMS          int64          `json:"latency_ms"`
	HostKeyFingerprint string         `json:"host_key_fingerprint"`
	Status             map[string]any `json:"status,omitempty"`
}

type UnknownHostKeyError struct {
	Fingerprint string
}

func (e *UnknownHostKeyError) Error() string {
	return "SSH host key is not trusted; confirm fingerprint " + e.Fingerprint
}

func NewManager(hostsFile string, timeout time.Duration, log *slog.Logger) (*Manager, error) {
	store, err := NewStore(hostsFile)
	if err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Manager{
		store: store, log: log, timeout: timeout, clients: map[string]*cachedClient{},
	}, nil
}

func (m *Manager) List() []HostView {
	hosts := m.store.List()
	out := make([]HostView, 0, len(hosts))
	for _, host := range hosts {
		out = append(out, host.View())
	}
	return out
}

func (m *Manager) Get(id string) (Host, bool) { return m.store.Get(id) }

func (m *Manager) Upsert(host Host, clearAPIToken bool) (HostView, error) {
	saved, err := m.store.Upsert(host, clearAPIToken)
	if err != nil {
		return HostView{}, err
	}
	m.invalidate(saved.ID)
	return saved.View(), nil
}

func (m *Manager) Delete(id string) error {
	m.invalidate(id)
	return m.store.Delete(id)
}

func (m *Manager) Test(ctx context.Context, id string, trustOnFirstUse bool) (TestResult, error) {
	host, ok := m.store.Get(id)
	if !ok {
		return TestResult{}, os.ErrNotExist
	}
	started := time.Now()
	client, fingerprint, err := m.dial(ctx, host, trustOnFirstUse)
	if err != nil {
		return TestResult{}, err
	}
	keepClient := false
	defer func() {
		if !keepClient {
			_ = client.Close()
		}
	}()
	if host.HostKeyFingerprint == "" {
		if !trustOnFirstUse {
			return TestResult{}, &UnknownHostKeyError{Fingerprint: fingerprint}
		}
		host, err = m.store.SetHostKeyFingerprint(id, fingerprint)
		if err != nil {
			return TestResult{}, err
		}
	}
	status, err := requestStatus(ctx, client, host)
	if err != nil {
		return TestResult{}, err
	}
	m.mu.Lock()
	if current := m.clients[id]; current != nil {
		_ = current.client.Close()
	}
	m.clients[id] = &cachedClient{host: host, client: client}
	m.mu.Unlock()
	keepClient = true
	return TestResult{
		OK: true, Name: host.Name, LatencyMS: time.Since(started).Milliseconds(),
		HostKeyFingerprint: host.HostKeyFingerprint, Status: status,
	}, nil
}

func requestStatus(ctx context.Context, client *ssh.Client, host Host) (map[string]any, error) {
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return client.DialContext(ctx, network, host.RemoteAddr)
		},
		DisableKeepAlives: true,
	}
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+host.RemoteAddr+"/api/v1/status", nil)
	if err != nil {
		return nil, err
	}
	if host.APIToken != "" {
		req.Header.Set("Authorization", "Bearer "+host.APIToken)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("reach remote AgentMux: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("remote AgentMux returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var status map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&status); err != nil {
		return nil, fmt.Errorf("decode remote status: %w", err)
	}
	return status, nil
}

// DialContext opens a TCP connection from the SSH host to the configured
// AgentMux loopback address. The caller-supplied address is intentionally
// ignored to keep the proxy scoped to that one service.
func (m *Manager) DialContext(ctx context.Context, id, network string) (net.Conn, error) {
	host, ok := m.store.Get(id)
	if !ok {
		return nil, os.ErrNotExist
	}
	for attempt := 0; attempt < 2; attempt++ {
		client, err := m.client(ctx, host)
		if err != nil {
			return nil, err
		}
		conn, err := client.DialContext(ctx, network, host.RemoteAddr)
		if err == nil {
			return conn, nil
		}
		m.invalidate(id)
		if attempt == 1 {
			return nil, fmt.Errorf("open SSH tunnel to %s: %w", host.RemoteAddr, err)
		}
	}
	return nil, errors.New("open SSH tunnel failed")
}

func (m *Manager) client(ctx context.Context, host Host) (*ssh.Client, error) {
	m.mu.Lock()
	if cached := m.clients[host.ID]; cached != nil &&
		cached.host.Host == host.Host && cached.host.Port == host.Port &&
		cached.host.User == host.User && cached.host.KeyPath == host.KeyPath &&
		cached.host.HostKeyFingerprint == host.HostKeyFingerprint {
		client := cached.client
		m.mu.Unlock()
		return client, nil
	}
	m.mu.Unlock()
	if host.HostKeyFingerprint == "" {
		return nil, errors.New("SSH host key is not trusted; test the connection first")
	}
	client, _, err := m.dial(ctx, host, false)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if current := m.clients[host.ID]; current != nil {
		_ = current.client.Close()
	}
	m.clients[host.ID] = &cachedClient{host: host, client: client}
	m.mu.Unlock()
	return client, nil
}

func (m *Manager) dial(ctx context.Context, host Host, trustOnFirstUse bool) (*ssh.Client, string, error) {
	auth, cleanup, err := authMethods(host)
	if err != nil {
		return nil, "", err
	}
	defer cleanup()
	var observedFingerprint string
	callback := func(_ string, _ net.Addr, key ssh.PublicKey) error {
		observedFingerprint = ssh.FingerprintSHA256(key)
		if host.HostKeyFingerprint == "" {
			if trustOnFirstUse {
				return nil
			}
			return &UnknownHostKeyError{Fingerprint: observedFingerprint}
		}
		if observedFingerprint != host.HostKeyFingerprint {
			return fmt.Errorf("SSH host key changed: expected %s, got %s", host.HostKeyFingerprint, observedFingerprint)
		}
		return nil
	}
	address := net.JoinHostPort(host.Host, fmt.Sprintf("%d", host.Port))
	dialer := net.Dialer{Timeout: m.timeout}
	raw, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, "", fmt.Errorf("connect SSH %s: %w", address, err)
	}
	deadline := time.Now().Add(m.timeout)
	_ = raw.SetDeadline(deadline)
	conn, chans, reqs, err := ssh.NewClientConn(raw, address, &ssh.ClientConfig{
		User: host.User, Auth: auth, HostKeyCallback: callback, Timeout: m.timeout,
	})
	_ = raw.SetDeadline(time.Time{})
	if err != nil {
		_ = raw.Close()
		return nil, observedFingerprint, fmt.Errorf("SSH handshake %s: %w", address, err)
	}
	return ssh.NewClient(conn, chans, reqs), observedFingerprint, nil
}

func authMethods(host Host) ([]ssh.AuthMethod, func(), error) {
	var methods []ssh.AuthMethod
	var closers []io.Closer
	cleanup := func() {
		for _, closer := range closers {
			_ = closer.Close()
		}
	}
	addKey := func(path string, required bool) error {
		raw, err := os.ReadFile(path)
		if err != nil {
			if !required && errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("read SSH key %s: %w", path, err)
		}
		signer, err := ssh.ParsePrivateKey(raw)
		if err != nil {
			return fmt.Errorf("parse SSH key %s (encrypted keys require ssh-agent): %w", path, err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
		return nil
	}
	if host.KeyPath != "" {
		if err := addKey(host.KeyPath, true); err != nil {
			return nil, cleanup, err
		}
	}
	if socket := strings.TrimSpace(os.Getenv("SSH_AUTH_SOCK")); socket != "" {
		conn, err := net.Dial("unix", socket)
		if err == nil {
			closers = append(closers, conn)
			methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
		}
	}
	if host.KeyPath == "" {
		if home, err := os.UserHomeDir(); err == nil {
			for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
				_ = addKey(filepath.Join(home, ".ssh", name), false)
			}
		}
	}
	if len(methods) == 0 {
		cleanup()
		return nil, func() {}, errors.New("no SSH key available; set key_path or load a key into ssh-agent")
	}
	return methods, cleanup, nil
}

func (m *Manager) invalidate(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cached := m.clients[id]; cached != nil {
		_ = cached.client.Close()
		delete(m.clients, id)
	}
}

func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, cached := range m.clients {
		_ = cached.client.Close()
		delete(m.clients, id)
	}
	if m.log != nil {
		m.log.Debug("remote SSH connections closed")
	}
}
