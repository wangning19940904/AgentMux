package bootstrap

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/provider"
	"github.com/wangning19940904/AgentMux/store"
)

func newProxyRestoreRuntime(t *testing.T) (*Runtime, *store.Store, net.Listener) {
	t.Helper()
	st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "proxy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = occupied.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	runtime := &Runtime{
		ctx: ctx, cancel: cancel, log: log,
		Provider: provider.NewService(log, st, occupied.Addr().String()),
	}
	t.Cleanup(runtime.Stop)
	if err := st.SetProxyToolConfig(ctx, store.ProxyToolConfig{Tool: "claude-desktop", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	return runtime, st, occupied
}

func waitForProxyRestore(t *testing.T, runtime *Runtime) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		runtime.proxyWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("proxy restore did not finish")
	}
}

func TestProxyRestoreRetriesAfterPortConflict(t *testing.T) {
	runtime, _, occupied := newProxyRestoreRuntime(t)
	runtime.startProxyRestore(5 * time.Millisecond)
	if runtime.Provider.Proxy().Running() {
		t.Fatal("proxy started while the port was occupied")
	}
	if err := occupied.Close(); err != nil {
		t.Fatal(err)
	}
	waitForProxyRestore(t, runtime)
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get(runtime.Provider.Proxy().BaseURL() + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("restored proxy health = %d", response.StatusCode)
	}
}

func TestProxyRestoreHonorsDisabledTakeover(t *testing.T) {
	runtime, st, occupied := newProxyRestoreRuntime(t)
	runtime.startProxyRestore(20 * time.Millisecond)
	if err := st.SetProxyToolConfig(runtime.ctx, store.ProxyToolConfig{Tool: "claude-desktop", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if err := occupied.Close(); err != nil {
		t.Fatal(err)
	}
	waitForProxyRestore(t, runtime)
	if runtime.Provider.Proxy().Running() {
		t.Fatal("retry started a proxy after takeover was disabled")
	}
}

func TestProxyRestoreStopsRetryingOnShutdown(t *testing.T) {
	runtime, _, occupied := newProxyRestoreRuntime(t)
	runtime.startProxyRestore(time.Hour)
	runtime.Stop()
	waitForProxyRestore(t, runtime)
	if err := occupied.Close(); err != nil {
		t.Fatal(err)
	}
	if runtime.Provider.Proxy().Running() {
		t.Fatal("proxy is still running after shutdown")
	}
}
