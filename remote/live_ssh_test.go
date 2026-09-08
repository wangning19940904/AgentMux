package remote

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/config"
)

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
