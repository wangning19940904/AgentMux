package remote

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// TestFakeSSHProcess is the body of the fake ssh(1) from installFakeSSH: -G
// prints the configured ProxyJump, and -W bridges stdio to the requested target.
func TestFakeSSHProcess(t *testing.T) {
	if os.Getenv("AGENTMUX_FAKE_SSH") != "1" {
		t.Skip("helper process")
	}
	args := os.Args
	for i, arg := range args {
		if arg == "-G" {
			if jump := os.Getenv("AGENTMUX_FAKE_SSH_JUMP"); jump != "" {
				fmt.Printf("hostname example\nproxyjump %s\n", jump)
			}
			os.Exit(0)
		}
		if arg != "-W" {
			continue
		}
		_ = os.WriteFile(os.Getenv("AGENTMUX_FAKE_SSH_LOG"), []byte(strings.Join(args, " ")), 0o600)
		if message := os.Getenv("AGENTMUX_FAKE_SSH_FAIL"); message != "" {
			fmt.Fprintln(os.Stderr, message)
			os.Exit(255)
		}
		conn, err := net.Dial("tcp", args[i+1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(255)
		}
		go func() {
			_, _ = io.Copy(conn, os.Stdin)
			os.Exit(0)
		}()
		_, _ = io.Copy(os.Stdout, conn)
		os.Exit(0)
	}
	os.Exit(2)
}

func installFakeSSH(t *testing.T, jump string) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "jump.log")
	script := fmt.Sprintf("#!/bin/sh\nexec %q -test.run='^TestFakeSSHProcess$' -- \"$@\"\n", os.Args[0])
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("AGENTMUX_FAKE_SSH", "1")
	t.Setenv("AGENTMUX_FAKE_SSH_JUMP", jump)
	t.Setenv("AGENTMUX_FAKE_SSH_LOG", logPath)
	return logPath
}

func proxyJumpTestKey(t *testing.T) (string, ssh.PublicKey) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, signer.PublicKey()
}

func TestProxyJumpArgs(t *testing.T) {
	base := []string{
		"-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=8",
		"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3",
		"-o", "ClearAllForwardings=yes", "-o", "ControlMaster=no", "-o", "ControlPath=none",
	}
	for _, tc := range []struct {
		jump string
		want []string
	}{
		{"relay", []string{"-W", "127.0.0.1:22", "--", "relay"}},
		{"me@relay:2222", []string{"-W", "127.0.0.1:22", "--", "ssh://me@relay:2222"}},
		{"ssh://me@relay:2222", []string{"-W", "127.0.0.1:22", "--", "ssh://me@relay:2222"}},
		{"[::1]:2222", []string{"-W", "127.0.0.1:22", "--", "ssh://[::1]:2222"}},
		{"first,second,last", []string{"-J", "first,second", "-W", "127.0.0.1:22", "--", "last"}},
	} {
		got, err := proxyJumpArgs(tc.jump, "127.0.0.1:22", 8*time.Second)
		if err != nil {
			t.Fatalf("%s: %v", tc.jump, err)
		}
		if want := append(append([]string{}, base...), tc.want...); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: args = %v, want %v", tc.jump, got, want)
		}
	}
	for _, jump := range []string{"", "relay,", "-oProxyCommand=evil"} {
		if _, err := proxyJumpArgs(jump, "127.0.0.1:22", time.Second); err == nil {
			t.Fatalf("%q: expected an error", jump)
		}
	}
}

func TestLookupSSHProxyJump(t *testing.T) {
	installFakeSSH(t, "relay")
	if got := lookupSSHProxyJump(context.Background(), "macmini"); got != "relay" {
		t.Fatalf("jump = %q, want relay", got)
	}
	if got := lookupSSHProxyJump(context.Background(), ""); got != "" {
		t.Fatalf("host without an alias resolved jump %q", got)
	}
	installFakeSSH(t, "")
	if got := lookupSSHProxyJump(context.Background(), "direct"); got != "" {
		t.Fatalf("jump = %q, want none", got)
	}
}

func TestManagerConnectsThroughProxyJumpAndKeepsHostKeyPin(t *testing.T) {
	keyPath, publicKey := proxyJumpTestKey(t)
	sshHost, sshPort, hostKey := startSSHForwarderWithHostKey(t, publicKey)
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true,"version":"through-jump"}`)
	}))
	t.Cleanup(apiServer.Close)
	logPath := installFakeSSH(t, "relay")

	manager, err := NewManager(filepath.Join(t.TempDir(), "hosts.json"), 5*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	saved, err := manager.Upsert(Host{
		Name: "macmini", Host: sshHost, Port: sshPort, User: "tester", KeyPath: keyPath,
		SSHAlias: "macmini", RemoteAddr: apiServer.Listener.Addr().String(),
		HostKeyFingerprint: ssh.FingerprintSHA256(hostKey),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Status(ctx, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status["version"] != "through-jump" {
		t.Fatalf("status = %+v", result)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("connection did not use the jump: %v", err)
	}
	if want := fmt.Sprintf("-W %s:%d -- relay", sshHost, sshPort); !strings.Contains(string(logged), want) {
		t.Fatalf("jump args = %q, want %q", logged, want)
	}

	// A jump must not weaken trust: the target's own key is still what is pinned.
	wrong, err := manager.Upsert(Host{
		Name: "impostor", Host: sshHost, Port: sshPort, User: "other", KeyPath: keyPath,
		SSHAlias: "impostor", RemoteAddr: apiServer.Listener.Addr().String(),
		HostKeyFingerprint: "SHA256:not-the-target-key",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Status(ctx, wrong.ID); err == nil || !strings.Contains(err.Error(), "host key changed") {
		t.Fatalf("status with wrong pin: %v", err)
	}
}

func TestProxyJumpFailureReportsSSHError(t *testing.T) {
	installFakeSSH(t, "relay")
	t.Setenv("AGENTMUX_FAKE_SSH_FAIL", "channel 0: open failed: connect failed: Connection refused")
	conn, err := dialViaProxyJump("relay", "127.0.0.1:22022", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, err = conn.Read(make([]byte, 1))
	if err == nil || !strings.Contains(err.Error(), "Connection refused") {
		t.Fatalf("read error = %v", err)
	}
	if !retryableSSHError(err) {
		t.Fatal("a refused jump forward should be retried as a transient failure")
	}
}
