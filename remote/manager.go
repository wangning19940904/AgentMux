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
	client remoteClient
}

// Manager owns persisted profiles and reusable SSH connections.
type Manager struct {
	store   *Store
	log     *slog.Logger
	timeout time.Duration
	install func(context.Context, remoteClient, Host) error
	mu      sync.Mutex
	clients map[string]*cachedClient
}

type TestResult struct {
	OK                 bool           `json:"ok"`
	Name               string         `json:"name"`
	LatencyMS          int64          `json:"latency_ms"`
	HostKeyFingerprint string         `json:"host_key_fingerprint"`
	Status             map[string]any `json:"status,omitempty"`
	Installed          bool           `json:"installed,omitempty"`
}

type ImportResult struct {
	Host HostView `json:"host"`
	TestResult
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
		store: store, log: log, timeout: timeout, install: installRemoteAgentMux,
		clients: map[string]*cachedClient{},
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

// Import verifies a discovered SSH target before persisting it. A successful
// import always leaves the host ready for the Console: SSH works, the host key
// is pinned, and the remote AgentMux status endpoint is reachable. When SSH is
// healthy but the loopback service is absent, the matching bundled CLI is
// installed and started automatically.
func (m *Manager) Import(ctx context.Context, candidate Host, trustOnFirstUse bool) (ImportResult, error) {
	if candidate.ID == "" {
		candidate.ID = "pending-import"
	}
	host, err := normalizeHost(candidate)
	if err != nil {
		return ImportResult{}, err
	}
	existing, found := m.find(host)
	if found {
		host.ID = existing.ID
		if host.APIToken == "" {
			host.APIToken = existing.APIToken
		}
		host.HostKeyFingerprint = existing.HostKeyFingerprint
	}
	started := time.Now()
	client, fingerprint, err := m.dial(ctx, host, trustOnFirstUse)
	if err != nil {
		return ImportResult{}, err
	}
	keepClient := false
	defer func() {
		if !keepClient {
			_ = client.Close()
		}
	}()
	if host.HostKeyFingerprint == "" {
		if !trustOnFirstUse {
			return ImportResult{}, &UnknownHostKeyError{Fingerprint: fingerprint}
		}
		host.HostKeyFingerprint = fingerprint
	}

	status, installed, err := m.ensureService(ctx, client, host)
	if err != nil {
		return ImportResult{}, err
	}
	if !found {
		host.ID = ""
	}
	saved, err := m.store.Upsert(host, false)
	if err != nil {
		return ImportResult{}, err
	}
	m.cache(saved, client)
	keepClient = true
	result := TestResult{
		OK: true, Name: saved.Name, LatencyMS: time.Since(started).Milliseconds(),
		HostKeyFingerprint: saved.HostKeyFingerprint, Status: status, Installed: installed,
	}
	return ImportResult{Host: saved.View(), TestResult: result}, nil
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
	status, installed, err := m.ensureService(ctx, client, host)
	if err != nil {
		return TestResult{}, err
	}
	m.cache(host, client)
	keepClient = true
	return TestResult{
		OK: true, Name: host.Name, LatencyMS: time.Since(started).Milliseconds(),
		HostKeyFingerprint: host.HostKeyFingerprint, Status: status, Installed: installed,
	}, nil
}

func (m *Manager) ensureService(ctx context.Context, client remoteClient, host Host) (map[string]any, bool, error) {
	status, err := requestStatus(ctx, client, host)
	if err == nil {
		return status, false, nil
	}
	var unavailable *ServiceUnavailableError
	if !errors.As(err, &unavailable) {
		return nil, false, err
	}
	if m.install == nil {
		return nil, false, err
	}
	if m.log != nil {
		m.log.Info("remote AgentMux is not running; installing it", "remote", host.Name)
	}
	if installErr := m.install(ctx, client, host); installErr != nil {
		return nil, false, fmt.Errorf("install remote AgentMux: %w", installErr)
	}
	deadline := time.Now().Add(12 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		status, lastErr = requestStatus(ctx, client, host)
		if lastErr == nil {
			return status, true, nil
		}
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	logText, _ := remoteAgentMuxLog(ctx, client)
	if logText != "" {
		return nil, false, fmt.Errorf("start remote AgentMux: %v; remote log: %s", lastErr, logText)
	}
	return nil, false, fmt.Errorf("start remote AgentMux: %w", lastErr)
}

// ServiceUnavailableError means the SSH connection succeeded but nothing
// accepted the configured AgentMux loopback connection. HTTP errors are not
// classified this way because they usually indicate an already-running
// service with an authentication or configuration problem.
type ServiceUnavailableError struct{ Err error }

func (e *ServiceUnavailableError) Error() string { return "reach remote AgentMux: " + e.Err.Error() }
func (e *ServiceUnavailableError) Unwrap() error { return e.Err }

func requestStatus(ctx context.Context, client remoteClient, host Host) (map[string]any, error) {
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
		return nil, &ServiceUnavailableError{Err: err}
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

func (m *Manager) find(candidate Host) (Host, bool) {
	for _, host := range m.store.List() {
		if strings.EqualFold(strings.TrimSpace(host.Host), strings.TrimSpace(candidate.Host)) &&
			host.Port == candidate.Port && strings.TrimSpace(host.User) == strings.TrimSpace(candidate.User) {
			return host, true
		}
	}
	return Host{}, false
}

func (m *Manager) cache(host Host, client remoteClient) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if current := m.clients[host.ID]; current != nil && current.client != client {
		_ = current.client.Close()
	}
	m.clients[host.ID] = &cachedClient{host: host, client: client}
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

func (m *Manager) client(ctx context.Context, host Host) (remoteClient, error) {
	m.mu.Lock()
	if cached := m.clients[host.ID]; cached != nil &&
		cached.host.Host == host.Host && cached.host.Port == host.Port &&
		cached.host.User == host.User && cached.host.KeyPath == host.KeyPath &&
		cached.host.SSHAlias == host.SSHAlias &&
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

func (m *Manager) dial(ctx context.Context, host Host, trustOnFirstUse bool) (remoteClient, string, error) {
	auth, cleanup, authErr := authMethods(host)
	defer cleanup()
	var observedFingerprint string
	var observedKey ssh.PublicKey
	callback := func(_ string, _ net.Addr, key ssh.PublicKey) error {
		observedKey = key
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
	if err == nil {
		return &nativeSSHClient{client: ssh.NewClient(conn, chans, reqs)}, observedFingerprint, nil
	}
	_ = raw.Close()

	// OpenSSH can provide non-interactive authentication methods that the Go
	// client cannot, notably GSSAPI on enterprise SSH hosts and credentials
	// supplied by platform integrations. Only fall back after the native key
	// exchange has verified (or collected) the exact server host key.
	if observedKey != nil && isSSHAuthenticationError(err) {
		fallback, fallbackErr := newOpenSSHRemoteClient(host, observedKey, m.timeout)
		if fallbackErr == nil {
			if _, fallbackErr = fallback.Run(ctx, "true", nil); fallbackErr == nil {
				return fallback, observedFingerprint, nil
			}
			_ = fallback.Close()
		}
		if authErr != nil {
			err = authErr
		}
		if fallbackErr != nil {
			return nil, observedFingerprint, fmt.Errorf(
				"SSH authentication %s: native client: %v; system OpenSSH: %w",
				address, err, fallbackErr,
			)
		}
	}
	if authErr != nil && isSSHAuthenticationError(err) {
		return nil, observedFingerprint, authErr
	}
	return nil, observedFingerprint, fmt.Errorf("SSH handshake %s: %w", address, err)
}

func isSSHAuthenticationError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unable to authenticate")
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
			return methods, cleanup, err
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
		return methods, cleanup, errors.New("no SSH key available; set key_path, load a key into ssh-agent, or use an SSH Config alias supported by system OpenSSH")
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
