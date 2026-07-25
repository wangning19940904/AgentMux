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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
}

func startSSHForwarder(t *testing.T, allowedKey ssh.PublicKey) (string, int) {
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
