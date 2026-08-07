package remote

import (
	"context"
	"crypto/ed25519"
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
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

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

type fallbackRemoteClient struct{}

func (*fallbackRemoteClient) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, address)
}

func (*fallbackRemoteClient) Run(context.Context, string, io.Reader) ([]byte, error) {
	return nil, nil
}

func (*fallbackRemoteClient) Close() error { return nil }

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
	config := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) != string(allowedKey.Marshal()) {
				return nil, errors.New("public key rejected")
			}
			return nil, nil
		},
	}
	config.AddHostKey(hostSigner)
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
	return host, port, hostSigner.PublicKey()
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
