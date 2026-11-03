package remote

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/config"
)

// Uses only a temporary controller profile and read-only remote operations.
// The expected fingerprint must be independently verified before opting in.
func TestLiveSSHAutomaticTokenImport(t *testing.T) {
	alias := os.Getenv("AGENTMUX_TEST_AUTO_TOKEN_ALIAS")
	fingerprint := os.Getenv("AGENTMUX_TEST_AUTO_TOKEN_FINGERPRINT")
	if alias == "" || fingerprint == "" {
		t.Skip("opt-in authenticated remote import probe")
	}
	hosts, err := DiscoverSSHHosts("")
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(t.TempDir()+"/hosts.json", 20*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	manager.install = nil
	for _, discovered := range hosts {
		if discovered.SSHAlias != alias {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		result, err := manager.Import(ctx, Host{
			Name: discovered.Name, Host: discovered.Host, Port: discovered.Port,
			User: discovered.User, KeyPath: discovered.KeyPath, SSHAlias: discovered.SSHAlias,
			RemoteAddr: defaultRemoteAddr, HostKeyFingerprint: fingerprint,
		}, false)
		if err != nil {
			t.Fatal(err)
		}
		if !result.OK || !result.Host.APITokenSet || result.Installed {
			t.Fatal("expected an authenticated import without installation")
		}
		if _, err := manager.Status(ctx, result.Host.ID); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: automatic token import and authenticated status succeeded", alias)
		return
	}
	t.Fatalf("SSH alias %q not found", alias)
}

// Read-only opt-in probe: it stops at the target key callback, before target
// authentication, service installation, or any persistent trust changes.
func TestLiveSSHProxyHostKeyProbe(t *testing.T) {
	alias := os.Getenv("AGENTMUX_TEST_SSH_PROXY_ALIAS")
	if alias == "" {
		t.Skip("opt-in SSH proxy host-key probe")
	}
	hosts, err := DiscoverSSHHosts("")
	if err != nil {
		t.Fatal(err)
	}
	for _, discovered := range hosts {
		if discovered.SSHAlias != alias {
			continue
		}
		manager, err := NewManager(t.TempDir()+"/hosts.json", 15*time.Second, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer manager.Close()
		_, _, err = manager.dial(context.Background(), Host{
			Host: discovered.Host, Port: discovered.Port, User: discovered.User,
			KeyPath: discovered.KeyPath, SSHAlias: discovered.SSHAlias,
		}, false)
		var unknown *UnknownHostKeyError
		if !errors.As(err, &unknown) {
			t.Fatalf("proxy key probe: %v", err)
		}
		t.Logf("%s: target key reached through SSH proxy (%s)", alias, unknown.Fingerprint)
		return
	}
	t.Fatalf("SSH alias %q not found", alias)
}

// Explicit opt-in only; ordinary tests never read the operator's SSH profiles.
func TestLiveSSHConcurrentRequestsWithIdleTunnel(t *testing.T) {
	alias := os.Getenv("AGENTMUX_TEST_SSH_ALIAS")
	if alias == "" {
		t.Skip("opt-in configured SSH integration test")
	}
	cfg, _, err := config.LoadResolved("")
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(cfg.Remote.HostsFile, time.Duration(cfg.Remote.ConnectTimeoutSeconds)*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	var host Host
	for _, view := range manager.List() {
		if view.Name == alias || view.SSHAlias == alias {
			host, _ = manager.Get(view.ID)
			break
		}
	}
	if host.ID == "" {
		t.Fatalf("SSH alias %q is not registered", alias)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	idle, err := manager.DialContext(ctx, host.ID, "tcp")
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	results := make(chan error, 6)
	for index := 0; index < 6; index++ {
		path := "/api/v1/status"
		if index%2 == 1 {
			path = "/api/v1/frameworks"
		}
		go func(path string) {
			started := time.Now()
			req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+host.RemoteAddr+path, nil)
			if host.APIToken != "" {
				req.Header.Set("Authorization", "Bearer "+host.APIToken)
			}
			status, _, err := manager.DoHTTP(host.ID, req, 1<<20)
			if err == nil && status != http.StatusOK {
				err = fmt.Errorf("HTTP %d", status)
			}
			t.Logf("%s: %s, error=%v", path, time.Since(started).Round(time.Millisecond), err)
			results <- err
		}(path)
	}
	for index := 0; index < 6; index++ {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
}
