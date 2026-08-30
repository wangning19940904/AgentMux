package remote

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestManagerSyncSSHConfigRefreshesAliasesAndPreservesSecrets(t *testing.T) {
	manager, err := NewManager(filepath.Join(t.TempDir(), "hosts.json"), time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)

	ecs, err := manager.Upsert(Host{
		Name: "aliyun-ecs", Host: "101.200.234.220", Port: 22, User: "root",
		KeyPath: "/keys/ecs", RemoteAddr: defaultRemoteAddr, APIToken: "secret-ecs",
		HostKeyFingerprint: "SHA256:ecs",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	sg, err := manager.Upsert(Host{
		Name: "aliyun-sg", Host: "47.236.247.144", Port: 22, User: "root",
		SSHAlias: "aliyun-sg", RemoteAddr: defaultRemoteAddr, APIToken: "secret-sg",
		HostKeyFingerprint: "SHA256:sg",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Upsert(Host{
		Name: "unmatched", Host: "10.0.0.9", Port: 22, User: "ops", RemoteAddr: defaultRemoteAddr,
	}, false); err != nil {
		t.Fatal(err)
	}

	result, err := manager.SyncSSHConfig([]DiscoveredHost{
		{Name: "aliyun-ecs-bj", SSHAlias: "aliyun-ecs-bj", Host: "101.200.234.220", Port: 22, User: "root", KeyPath: "/keys/ecs-new"},
		{Name: "aliyun-swas-sg", SSHAlias: "aliyun-swas-sg", Host: "47.236.247.144", Port: 22, User: "root", KeyPath: "/keys/sg"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Updated != 2 || result.Unchanged != 0 || result.Unmatched != 1 || result.Ambiguous != 0 {
		t.Fatalf("sync result = %+v", result)
	}

	updatedECS, ok := manager.Get(ecs.ID)
	if !ok || updatedECS.Name != "aliyun-ecs-bj" || updatedECS.SSHAlias != "aliyun-ecs-bj" ||
		updatedECS.KeyPath != "/keys/ecs-new" || updatedECS.APIToken != "secret-ecs" ||
		updatedECS.HostKeyFingerprint != "SHA256:ecs" {
		t.Fatalf("updated ECS host = %+v, ok = %v", updatedECS, ok)
	}
	updatedSG, ok := manager.Get(sg.ID)
	if !ok || updatedSG.Name != "aliyun-swas-sg" || updatedSG.SSHAlias != "aliyun-swas-sg" ||
		updatedSG.APIToken != "secret-sg" || updatedSG.HostKeyFingerprint != "SHA256:sg" {
		t.Fatalf("updated SG host = %+v, ok = %v", updatedSG, ok)
	}
}

func TestManagerSyncSSHConfigSkipsAmbiguousTargets(t *testing.T) {
	manager, err := NewManager(filepath.Join(t.TempDir(), "hosts.json"), time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	saved, err := manager.Upsert(Host{
		Name: "custom-name", Host: "10.0.0.8", Port: 22, User: "deploy", RemoteAddr: defaultRemoteAddr,
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	result, err := manager.SyncSSHConfig([]DiscoveredHost{
		{Name: "build-a", SSHAlias: "build-a", Host: "10.0.0.8", Port: 22, User: "deploy"},
		{Name: "build-b", SSHAlias: "build-b", Host: "10.0.0.8", Port: 22, User: "deploy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Updated != 0 || result.Ambiguous != 1 {
		t.Fatalf("sync result = %+v", result)
	}
	host, ok := manager.Get(saved.ID)
	if !ok || host.Name != "custom-name" || host.SSHAlias != "" {
		t.Fatalf("ambiguous host changed: %+v, ok = %v", host, ok)
	}
}

func TestManagerTrustsHostKeyAndReachesRemoteAgentMux(t *testing.T) {
	_, clientPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientSigner, err := ssh.NewSignerFromKey(clientPrivate)
	if err != nil {
		t.Fatal(err)
	}
	keyBlock, err := ssh.MarshalPrivateKey(clientPrivate, "")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(keyBlock), 0o600); err != nil {
		t.Fatal(err)
	}

	sshHost, sshPort := startSSHForwarder(t, clientSigner.PublicKey())
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/status" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer bridge-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"projects":2,"version":"test"}`)
	}))
	t.Cleanup(apiServer.Close)

	manager, err := NewManager(filepath.Join(t.TempDir(), "hosts.json"), 2*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	saved, err := manager.Upsert(Host{
		Name: "test-box", Host: sshHost, Port: sshPort, User: "tester",
		KeyPath: keyPath, RemoteAddr: apiServer.Listener.Addr().String(),
		APIToken: "bridge-secret",
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err = manager.Test(ctx, saved.ID, false)
	var unknown *UnknownHostKeyError
	if !errors.As(err, &unknown) || unknown.Fingerprint == "" {
		t.Fatalf("first test error = %v, want untrusted host fingerprint", err)
	}

	result, err := manager.Test(ctx, saved.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Status["version"] != "test" {
		t.Fatalf("test result = %+v", result)
	}
	host, ok := manager.Get(saved.ID)
	if !ok || host.HostKeyFingerprint != result.HostKeyFingerprint {
		t.Fatalf("saved host trust = %+v, ok = %v", host, ok)
	}
	observed, err := manager.Status(ctx, saved.ID)
	if err != nil || observed.Status["version"] != "test" {
		t.Fatalf("status result = %+v, err = %v", observed, err)
	}
	manager.mu.Lock()
	stale := manager.clients[saved.ID]
	if stale == nil {
		manager.mu.Unlock()
		t.Fatal("trusted SSH client was not cached")
	}
	_ = stale.client.Close()
	manager.mu.Unlock()
	observed, err = manager.Status(ctx, saved.ID)
	if err != nil || observed.Status["version"] != "test" {
		t.Fatalf("status did not reconnect after stale cached SSH client: result = %+v, err = %v", observed, err)
	}
	manager.update = func(context.Context, remoteClient, Host) (remoteUpdateArtifact, error) {
		return remoteUpdateArtifact{
			Platform: "linux", Arch: "amd64", Version: "test-build", SHA256: "abc123",
			DataPath:    "/home/tester/.agentnexus/agentnexus.db",
			DatabaseURL: remoteLinuxPostgresURL,
			BackupPath:  "/home/tester/.agentmux/backups/pre-update.db",
		}, nil
	}
	updated, err := manager.Update(ctx, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.OK || updated.PreviousVersion != "test" || updated.Version != "test-build" ||
		updated.DataPath != "/home/tester/.agentnexus/agentnexus.db" ||
		updated.DatabaseURL != remoteLinuxPostgresURL || updated.Status["version"] != "test" {
		t.Fatalf("update result = %+v", updated)
	}

	if err := manager.Delete(saved.ID); err != nil {
		t.Fatal(err)
	}
	candidate := Host{
		Name: "imported-box", Host: sshHost, Port: sshPort, User: "tester",
		KeyPath: keyPath, RemoteAddr: apiServer.Listener.Addr().String(),
		APIToken: "bridge-secret",
	}
	_, err = manager.Import(ctx, candidate, false)
	if !errors.As(err, &unknown) || len(manager.List()) != 0 {
		t.Fatalf("untrusted import error = %v, hosts = %+v", err, manager.List())
	}
	imported, err := manager.Import(ctx, candidate, true)
	if err != nil {
		t.Fatal(err)
	}
	if !imported.Host.Trusted || imported.Installed || imported.Status["version"] != "test" {
		t.Fatalf("import result = %+v", imported)
	}

	// If SSH remains healthy but the loopback API disappears, Test invokes the
	// installer and waits for the replacement service to become ready.
	apiAddress := apiServer.Listener.Addr().String()
	apiServer.Close()
	manager.install = func(_ context.Context, _ remoteClient, _ Host) error {
		listener, listenErr := net.Listen("tcp", apiAddress)
		if listenErr != nil {
			return listenErr
		}
		t.Cleanup(func() { _ = listener.Close() })
		go func() {
			_ = http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"ok":true,"version":"installed"}`)
			}))
		}()
		return nil
	}
	result, err = manager.Test(ctx, imported.Host.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Installed || result.Status["version"] != "installed" {
		t.Fatalf("post-install test result = %+v", result)
	}
}

func TestManagerFallsBackToSystemOpenSSHAuthentication(t *testing.T) {
	_, rejectedPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyBlock, err := ssh.MarshalPrivateKey(rejectedPrivate, "")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(keyBlock), 0o600); err != nil {
		t.Fatal(err)
	}

	_, allowedPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	allowedSigner, err := ssh.NewSignerFromKey(allowedPrivate)
	if err != nil {
		t.Fatal(err)
	}
	sshHost, sshPort := startSSHForwarder(t, allowedSigner.PublicKey())
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"version":"fallback"}`)
	}))
	t.Cleanup(apiServer.Close)

	originalFactory := newOpenSSHRemoteClient
	t.Cleanup(func() { newOpenSSHRemoteClient = originalFactory })
	fallbackUsed := false
	newOpenSSHRemoteClient = func(host Host, key ssh.PublicKey, _ time.Duration) (remoteClient, error) {
		fallbackUsed = true
		if host.SSHAlias != "ecs-cn" {
			t.Fatalf("SSH alias = %q, want ecs-cn", host.SSHAlias)
		}
		if key == nil || ssh.FingerprintSHA256(key) == "" {
			t.Fatal("fallback did not receive the verified server host key")
		}
		return &fallbackRemoteClient{}, nil
	}

	manager, err := NewManager(filepath.Join(t.TempDir(), "hosts.json"), 2*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	saved, err := manager.Upsert(Host{
		Name: "ecs-cn", Host: sshHost, Port: sshPort, User: "tester",
		KeyPath: keyPath, SSHAlias: "ecs-cn", RemoteAddr: apiServer.Listener.Addr().String(),
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := manager.Test(ctx, saved.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !fallbackUsed || result.Status["version"] != "fallback" {
		t.Fatalf("fallback used = %v, result = %+v", fallbackUsed, result)
	}
}

func TestManagerFallsBackToSystemOpenSSHWhenNativeChannelOpenIsMalformed(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := ssh.NewPublicKey(privateKey.Public())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(filepath.Join(t.TempDir(), "hosts.json"), 2*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	saved, err := manager.Upsert(Host{
		Name: "channel-fallback", Host: "192.0.2.10", Port: 22, User: "tester",
		RemoteAddr: defaultRemoteAddr, HostKeyFingerprint: ssh.FingerprintSHA256(publicKey),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	native := &malformedChannelRemoteClient{key: publicKey}
	manager.clients[saved.ID] = &cachedClient{host: mustStoredHost(t, manager, saved.ID), client: native}

	originalFactory := newOpenSSHRemoteClient
	t.Cleanup(func() { newOpenSSHRemoteClient = originalFactory })
	fallback := &pipeFallbackRemoteClient{}
	newOpenSSHRemoteClient = func(_ Host, key ssh.PublicKey, _ time.Duration) (remoteClient, error) {
		if ssh.FingerprintSHA256(key) != ssh.FingerprintSHA256(publicKey) {
			t.Fatal("fallback did not receive the verified host key")
		}
		return fallback, nil
	}

	conn, err := manager.DialContext(context.Background(), saved.ID, "tcp")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if fallback.runs != 1 || fallback.dials != 1 {
		t.Fatalf("fallback runs = %d, dials = %d", fallback.runs, fallback.dials)
	}
}

func TestManagerSharesSystemOpenSSHFallbackAcrossConcurrentDials(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := ssh.NewPublicKey(privateKey.Public())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(filepath.Join(t.TempDir(), "hosts.json"), 2*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	saved, err := manager.Upsert(Host{
		Name: "concurrent-fallback", Host: "192.0.2.11", Port: 22, User: "tester",
		RemoteAddr: defaultRemoteAddr, HostKeyFingerprint: ssh.FingerprintSHA256(publicKey),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	native := &malformedChannelRemoteClient{key: publicKey}
	manager.clients[saved.ID] = &cachedClient{host: mustStoredHost(t, manager, saved.ID), client: native}

	originalFactory := newOpenSSHRemoteClient
	t.Cleanup(func() { newOpenSSHRemoteClient = originalFactory })
	fallback := &concurrentFallbackRemoteClient{}
	var creations atomic.Int32
	newOpenSSHRemoteClient = func(_ Host, _ ssh.PublicKey, _ time.Duration) (remoteClient, error) {
		creations.Add(1)
		return fallback, nil
	}

	const callers = 12
	start := make(chan struct{})
	results := make(chan error, callers)
	for range callers {
		go func() {
			<-start
			conn, dialErr := manager.DialContext(context.Background(), saved.ID, "tcp")
			if conn != nil {
				_ = conn.Close()
			}
			results <- dialErr
		}()
	}
	close(start)
	for range callers {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if creations.Load() != 1 || fallback.runs.Load() != 1 || fallback.dials.Load() != callers {
		t.Fatalf("creations = %d, runs = %d, dials = %d", creations.Load(), fallback.runs.Load(), fallback.dials.Load())
	}
}

func TestManagerStatusPreservesClientPromotedWhileStaleRequestFails(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"version":"replacement"}`)
	}))
	t.Cleanup(apiServer.Close)
	manager, err := NewManager(filepath.Join(t.TempDir(), "hosts.json"), 2*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	saved, err := manager.Upsert(Host{
		Name: "conditional-invalidation", Host: "192.0.2.12", Port: 22, User: "tester",
		RemoteAddr: apiServer.Listener.Addr().String(), HostKeyFingerprint: "SHA256:trusted",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	host := mustStoredHost(t, manager, saved.ID)
	stale := &blockingFailedRemoteClient{started: make(chan struct{}), release: make(chan struct{})}
	manager.clients[saved.ID] = &cachedClient{host: host, client: stale}

	resultCh := make(chan TestResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, statusErr := manager.Status(context.Background(), saved.ID)
		resultCh <- result
		errCh <- statusErr
	}()
	<-stale.started
	manager.cache(host, &fallbackRemoteClient{})
	close(stale.release)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	result := <-resultCh
	if result.Status["version"] != "replacement" {
		t.Fatalf("status = %+v", result.Status)
	}
}

func mustStoredHost(t *testing.T, manager *Manager, id string) Host {
	t.Helper()
	host, ok := manager.Get(id)
	if !ok {
		t.Fatalf("host %s was not stored", id)
	}
	return host
}

func TestOpenSSHClientRunsCommandsAndPinsHostKey(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("system OpenSSH is not installed")
	}
	_, clientPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientSigner, err := ssh.NewSignerFromKey(clientPrivate)
	if err != nil {
		t.Fatal(err)
	}
	keyBlock, err := ssh.MarshalPrivateKey(clientPrivate, "")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(keyBlock), 0o600); err != nil {
		t.Fatal(err)
	}
	sshHost, sshPort, hostKey := startSSHForwarderWithHostKey(t, clientSigner.PublicKey())
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"version":"openssh"}`)
	}))
	t.Cleanup(apiServer.Close)
	host := Host{
		Name: "openssh", Host: sshHost, Port: sshPort, User: "tester",
		KeyPath: keyPath, RemoteAddr: apiServer.Listener.Addr().String(),
	}
	client, err := newOpenSSHClient(host, hostKey, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if _, err := client.Run(ctx, "true", nil); err != nil {
		t.Fatalf("run through system OpenSSH: %v", err)
	}
	status, err := requestStatus(ctx, client, host)
	if err != nil {
		t.Fatalf("tunnel through system OpenSSH: %v", err)
	}
	if status["version"] != "openssh" {
		t.Fatalf("status = %+v", status)
	}

	_, wrongPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrongKey, err := ssh.NewPublicKey(wrongPrivate.Public())
	if err != nil {
		t.Fatal(err)
	}
	wrongClient, err := newOpenSSHClient(host, wrongKey, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wrongClient.Close() })
	if _, err := wrongClient.Run(ctx, "true", nil); err == nil {
		t.Fatal("system OpenSSH accepted a server key different from the pinned key")
	}
}

func TestOpenSSHClientUsesVerifiedKeyWhenServerOffersAnotherPreferredKey(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("system OpenSSH is not installed")
	}
	_, clientPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientSigner, err := ssh.NewSignerFromKey(clientPrivate)
	if err != nil {
		t.Fatal(err)
	}
	keyBlock, err := ssh.MarshalPrivateKey(clientPrivate, "")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(keyBlock), 0o600); err != nil {
		t.Fatal(err)
	}

	verifiedPrivate, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifiedSigner, err := ssh.NewSignerFromKey(verifiedPrivate)
	if err != nil {
		t.Fatal(err)
	}
	_, preferredPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	preferredSigner, err := ssh.NewSignerFromKey(preferredPrivate)
	if err != nil {
		t.Fatal(err)
	}
	sshHost, sshPort := startSSHForwarderWithHostSigners(
		t, clientSigner.PublicKey(), verifiedSigner, preferredSigner,
	)
	host := Host{
		Name: "multi-key", Host: sshHost, Port: sshPort, User: "tester",
		KeyPath: keyPath, RemoteAddr: defaultRemoteAddr,
	}

	// Reproduce a conflicting SSH Config preference: OpenSSH is told to use
	// the server's Ed25519 key first, while the isolated known_hosts file only
	// contains the ECDSA key already verified by the native Go SSH connection.
	unpinned, err := newOpenSSHClient(host, verifiedSigner.PublicKey(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	unpinned.hostKeyAlgos = ssh.KeyAlgoED25519 + "," + ssh.KeyAlgoECDSA256
	if _, err := unpinned.Run(context.Background(), "true", nil); err == nil {
		_ = unpinned.Close()
		t.Fatal("OpenSSH unexpectedly accepted a different offered host key without algorithm pinning")
	}
	_ = unpinned.Close()

	pinned, err := newOpenSSHClient(host, verifiedSigner.PublicKey(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pinned.Close() })
	if _, err := pinned.Run(context.Background(), "true", nil); err != nil {
		t.Fatalf("OpenSSH did not use the already verified host key: %v", err)
	}
}

type fallbackRemoteClient struct{}

func (*fallbackRemoteClient) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, address)
}

func (*fallbackRemoteClient) Run(context.Context, string, io.Reader) ([]byte, error) {
	return nil, nil
}

func (*fallbackRemoteClient) Close() error { return nil }

type malformedChannelRemoteClient struct {
	key ssh.PublicKey
}

func (c *malformedChannelRemoteClient) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("ssh: unexpected packet in response to channel open: <nil>")
}
func (c *malformedChannelRemoteClient) Run(context.Context, string, io.Reader) ([]byte, error) {
	return nil, nil
}
func (c *malformedChannelRemoteClient) Close() error                   { return nil }
func (c *malformedChannelRemoteClient) VerifiedHostKey() ssh.PublicKey { return c.key }

type pipeFallbackRemoteClient struct {
	runs  int
	dials int
}

func (c *pipeFallbackRemoteClient) DialContext(context.Context, string, string) (net.Conn, error) {
	c.dials++
	left, right := net.Pipe()
	go func() { _ = right.Close() }()
	return left, nil
}
func (c *pipeFallbackRemoteClient) Run(context.Context, string, io.Reader) ([]byte, error) {
	c.runs++
	return nil, nil
}
func (*pipeFallbackRemoteClient) Close() error { return nil }

type concurrentFallbackRemoteClient struct {
	runs  atomic.Int32
	dials atomic.Int32
}

func (c *concurrentFallbackRemoteClient) DialContext(context.Context, string, string) (net.Conn, error) {
	c.dials.Add(1)
	left, right := net.Pipe()
	go func() { _ = right.Close() }()
	return left, nil
}
func (c *concurrentFallbackRemoteClient) Run(context.Context, string, io.Reader) ([]byte, error) {
	c.runs.Add(1)
	return nil, nil
}
func (*concurrentFallbackRemoteClient) Close() error { return nil }

type blockingFailedRemoteClient struct {
	started chan struct{}
	release chan struct{}
}

func (c *blockingFailedRemoteClient) DialContext(context.Context, string, string) (net.Conn, error) {
	close(c.started)
	<-c.release
	return nil, errors.New("stale tunnel failed")
}
func (*blockingFailedRemoteClient) Run(context.Context, string, io.Reader) ([]byte, error) {
	return nil, nil
}
func (*blockingFailedRemoteClient) Close() error { return nil }

func startSSHForwarder(t *testing.T, allowedKey ssh.PublicKey) (string, int) {
	host, port, _ := startSSHForwarderWithHostKey(t, allowedKey)
	return host, port
}

func startSSHForwarderWithHostKey(t *testing.T, allowedKey ssh.PublicKey) (string, int, ssh.PublicKey) {
	t.Helper()
	_, hostPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPrivate)
	if err != nil {
		t.Fatal(err)
	}
	host, port := startSSHForwarderWithHostSigners(t, allowedKey, hostSigner)
	return host, port, hostSigner.PublicKey()
}

func startSSHForwarderWithHostSigners(t *testing.T, allowedKey ssh.PublicKey, hostSigners ...ssh.Signer) (string, int) {
	t.Helper()
	if len(hostSigners) == 0 {
		t.Fatal("at least one SSH host key is required")
	}
	config := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) != string(allowedKey.Marshal()) {
				return nil, errors.New("public key rejected")
			}
			return nil, nil
		},
	}
	for _, hostSigner := range hostSigners {
		config.AddHostKey(hostSigner)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			go serveSSHConnection(raw, config)
		}
	}()
	host, portRaw, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}

func serveSSHConnection(raw net.Conn, config *ssh.ServerConfig) {
	conn, channels, requests, err := ssh.NewServerConn(raw, config)
	if err != nil {
		_ = raw.Close()
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(requests)
	for request := range channels {
		if request.ChannelType() == "session" {
			channel, channelRequests, acceptErr := request.Accept()
			if acceptErr != nil {
				continue
			}
			go func() {
				defer channel.Close()
				for channelRequest := range channelRequests {
					if channelRequest.Type != "exec" {
						_ = channelRequest.Reply(false, nil)
						continue
					}
					_ = channelRequest.Reply(true, nil)
					_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
					return
				}
			}()
			continue
		}
		if request.ChannelType() != "direct-tcpip" {
			_ = request.Reject(ssh.UnknownChannelType, "only direct-tcpip is supported")
			continue
		}
		var target struct {
			Host       string
			Port       uint32
			OriginHost string
			OriginPort uint32
		}
		if err := ssh.Unmarshal(request.ExtraData(), &target); err != nil {
			_ = request.Reject(ssh.ConnectionFailed, "invalid target")
			continue
		}
		upstream, err := net.DialTimeout(
			"tcp",
			net.JoinHostPort(target.Host, strconv.Itoa(int(target.Port))),
			2*time.Second,
		)
		if err != nil {
			_ = request.Reject(ssh.ConnectionFailed, err.Error())
			continue
		}
		channel, channelRequests, err := request.Accept()
		if err != nil {
			_ = upstream.Close()
			continue
		}
		go ssh.DiscardRequests(channelRequests)
		go func() {
			defer channel.Close()
			defer upstream.Close()
			done := make(chan struct{}, 1)
			go func() {
				_, _ = io.Copy(channel, upstream)
				_ = channel.CloseWrite()
				done <- struct{}{}
			}()
			_, _ = io.Copy(upstream, channel)
			<-done
		}()
	}
}
