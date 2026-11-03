package remote

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// A real stream proxy, run in a subprocess so tests need neither nc nor any
// operator SSH profiles. The reserved .invalid target only resolves here.
func TestSSHProxyTCPHelper(t *testing.T) {
	if os.Getenv("AGENTMUX_PROXY_TEST_HELPER") != "1" || len(os.Args) < 4 || os.Args[len(os.Args)-3] != "--" {
		return
	}
	host, port := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	if host == "private.invalid" {
		host = "127.0.0.1"
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	go func() {
		_, _ = io.Copy(conn, os.Stdin)
		_ = conn.(*net.TCPConn).CloseWrite()
	}()
	_, _ = io.Copy(os.Stdout, conn)
	_ = conn.Close()
	os.Exit(0)
}

func proxyTestKey(t *testing.T) (ssh.Signer, string) {
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
	path := filepath.Join(t.TempDir(), "key with spaces")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return signer, path
}

func useTestSSHConfig(t *testing.T, config string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX test wrapper")
	}
	executable, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("system OpenSSH is not installed")
	}
	dir := t.TempDir()
	configPath := filepath.Join(dir, "ssh_config")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nexec " + shellQuote(executable) + " -F " + shellQuote(configPath) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SSH_AUTH_SOCK", "")
}

func TestManagerImportsThroughSSHProxies(t *testing.T) {
	for _, mode := range []string{"ProxyCommand", "ProxyJump", "ProxyJumpChain"} {
		t.Run(mode, func(t *testing.T) {
			clientKey, keyPath := proxyTestKey(t)
			jumpKey, jumpKeyPath := proxyTestKey(t)
			_, port, targetKey := startSSHForwarderWithHostKey(t, clientKey.PublicKey())
			jumpHost, jumpPort, jumpHostKey := startSSHForwarderWithHostKey(t, jumpKey.PublicKey())
			knownHosts := filepath.Join(t.TempDir(), "known_hosts")
			line := fmt.Sprintf("[%s]:%d %s", jumpHost, jumpPort, ssh.MarshalAuthorizedKey(jumpHostKey))
			if err := os.WriteFile(knownHosts, []byte(line), 0o600); err != nil {
				t.Fatal(err)
			}
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("AGENTMUX_PROXY_TEST_HELPER", "1")
			proxy := "ProxyCommand " + shellQuote(binary) + " -test.run=^TestSSHProxyTCPHelper$ -- %h %p"
			hostname := "private.invalid"
			if mode != "ProxyCommand" {
				hostname = "127.0.0.1"
				proxy = "ProxyJump jumper@jump-b"
				if mode == "ProxyJumpChain" {
					proxy = "ProxyJump jump-a,jumper@jump-b"
				}
			}
			// Match/all and Include exercise effective config resolution. The
			// persisted address is unreachable directly for all three cases.
			included := filepath.Join(t.TempDir(), "included config")
			if err := os.WriteFile(included, []byte("Match host proxied-*\n  "+proxy+"\nMatch all\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			config := fmt.Sprintf(`Include %q
Host proxied-target
  HostName %s
  IdentityFile %q
Host jump-a jump-b
  HostName %s
  Port %d
  User jumper
  IdentityFile %q
Host *
  BatchMode yes
  IdentitiesOnly yes
  StrictHostKeyChecking yes
  UserKnownHostsFile %q
  GlobalKnownHostsFile /dev/null
`, included, hostname, keyPath, jumpHost, jumpPort, jumpKeyPath, knownHosts)
			useTestSSHConfig(t, config)
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"ok":true,"version":"proxied"}`)
			}))
			t.Cleanup(api.Close)
			manager, err := NewManager(filepath.Join(t.TempDir(), "hosts.json"), 5*time.Second, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(manager.Close)
			candidate := Host{Name: "proxied-target", SSHAlias: "proxied-target", Host: "private.invalid", Port: port, User: "tester", KeyPath: keyPath, RemoteAddr: api.Listener.Addr().String()}
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			_, err = manager.Import(ctx, candidate, false)
			var unknown *UnknownHostKeyError
			if !errors.As(err, &unknown) || unknown.Fingerprint != ssh.FingerprintSHA256(targetKey) || len(manager.List()) != 0 {
				t.Fatalf("first import must request target trust without persisting: %v, hosts=%v", err, manager.List())
			}
			result, err := manager.Import(ctx, candidate, true)
			if err != nil || !result.OK || result.Status["version"] != "proxied" || result.Host.HostKeyFingerprint != unknown.Fingerprint {
				t.Fatalf("trusted import: %+v, error=%v", result, err)
			}
			manager.invalidate(result.Host.ID)
			if _, err := manager.Status(ctx, result.Host.ID); err != nil {
				t.Fatalf("reconnect through proxy: %v", err)
			}
			candidate.HostKeyFingerprint = "SHA256:changed"
			if _, _, err := manager.dial(ctx, candidate, true); err == nil || !strings.Contains(err.Error(), "host key changed") {
				t.Fatalf("changed target key must be rejected, including with TOFU: %v", err)
			}
			// Force native auth to fail; OpenSSH must still use the proxy and
			// the verified target key when reading its own IdentityFile.
			_, wrongKeyPath := proxyTestKey(t)
			candidate.KeyPath = wrongKeyPath
			candidate.HostKeyFingerprint = unknown.Fingerprint
			fallback, _, err := manager.dial(ctx, candidate, false)
			if err != nil {
				t.Fatalf("OpenSSH authentication through proxy: %v", err)
			}
			defer fallback.Close()
			if _, ok := fallback.(*openSSHClient); !ok {
				t.Fatalf("expected OpenSSH authentication fallback, got %T", fallback)
			}
			if _, err := requestStatus(ctx, fallback, candidate); err != nil {
				t.Fatalf("OpenSSH API tunnel through proxy: %v", err)
			}
		})
	}
}

func TestSSHProxyFailureAndCancellation(t *testing.T) {
	for _, mode := range []string{"failure", "timeout", "cancel", "fdpass"} {
		t.Run(mode, func(t *testing.T) {
			proxy := `ProxyCommand sh -c 'echo proxy-unavailable >&2; exit 23'`
			if mode == "timeout" || mode == "cancel" {
				proxy = `ProxyCommand sh -c 'sleep 30'`
			}
			if mode == "fdpass" {
				proxy += "\nProxyUseFdpass yes"
			}
			useTestSSHConfig(t, "Host broken\n"+proxy+"\n")
			key, path := proxyTestKey(t)
			directHost, directPort := startSSHForwarder(t, key.PublicKey())
			manager, err := NewManager(filepath.Join(t.TempDir(), "hosts.json"), time.Second, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(manager.Close)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel" {
				timer := time.AfterFunc(500*time.Millisecond, cancel)
				defer timer.Stop()
			}
			started := time.Now()
			// Direct connection would succeed: a broken proxy must not bypass it.
			_, _, err = manager.dial(ctx, Host{SSHAlias: "broken", Host: directHost, Port: directPort, User: "tester", KeyPath: path}, true)
			if err == nil {
				t.Fatal("broken proxy unexpectedly connected directly")
			}
			switch mode {
			case "failure":
				if !strings.Contains(err.Error(), "proxy-unavailable") {
					t.Fatalf("missing proxy diagnostics: %v", err)
				}
			case "timeout", "cancel":
				if time.Since(started) > 3*time.Second || !(errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
					t.Fatalf("proxy cancellation took %s: %v", time.Since(started), err)
				}
			case "fdpass":
				if !strings.Contains(err.Error(), "ProxyUseFdpass") {
					t.Fatalf("missing fdpass diagnostic: %v", err)
				}
			}
		})
	}
}

func TestSSHProxyTokenExpansion(t *testing.T) {
	host := Host{Host: "::1", Port: 2222, User: "tester", SSHAlias: "my-box"}
	got, err := expandSSHProxyTokens(`nc '%h' %p # %r %n %%h`, host)
	if err != nil || got != `nc '::1' 2222 # tester my-box %h` {
		t.Fatalf("token expansion = %q, %v", got, err)
	}
	for _, input := range []string{"nc %x", "nc %"} {
		if _, err := expandSSHProxyTokens(input, host); err == nil {
			t.Fatalf("accepted invalid token in %q", input)
		}
	}
	host.User = "$(touch /tmp/unwanted)"
	if _, err := expandSSHProxyTokens("helper %r", host); err == nil {
		t.Fatal("accepted shell syntax in username")
	}
}
