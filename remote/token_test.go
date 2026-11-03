package remote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Run the actual discovery script with an isolated remote home/environment.
type tokenScriptClient struct {
	dir  string
	env  []string
	runs atomic.Int32
}

func (c *tokenScriptClient) Run(ctx context.Context, command string, stdin io.Reader) ([]byte, error) {
	c.runs.Add(1)
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir, cmd.Env, cmd.Stdin = c.dir, c.env, stdin
	return cmd.CombinedOutput()
}

func (*tokenScriptClient) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, addr)
}

func (*tokenScriptClient) Close() error { return nil }

func tokenFixture(t *testing.T) (*tokenScriptClient, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("remote credential discovery targets POSIX hosts")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "remote config.toml")
	return &tokenScriptClient{dir: dir, env: []string{"HOME=" + dir, "PATH=/usr/bin:/bin", "AMUX_CONFIG=" + path}}, path
}

func TestReadRemoteAPITokens(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		env          []string
		want         []string
	}{
		{"toml syntax", "[bridge]\nenabled = true\ntoken = 'a # quote\" $dollar' # comment\n", nil, []string{`a # quote" $dollar`}},
		{"remote environment", "[bridge]\ntoken = '${AUTH_SECRET}'\n", []string{"AUTH_SECRET=remote-secret"}, []string{"remote-secret"}},
		{"environment override", "[bridge]\ntoken = 'config-secret'\n", []string{"AGENTMUX_BRIDGE_TOKEN=service-secret"}, []string{"service-secret", "config-secret"}},
		{"deduplicate", "[bridge]\ntoken = 'same-secret'\n", []string{"AGENTMUX_BRIDGE_TOKEN=same-secret"}, []string{"same-secret"}},
		{"no token", "[bridge]\nenabled = false\n", nil, nil},
		{"unset environment", "[bridge]\ntoken = '${AUTH_SECRET}'\n", nil, nil},
		{"no shell evaluation", "[bridge]\ntoken = '$(touch injected)'\n", nil, []string{"$(touch injected)"}},
		{"reject control characters", "[bridge]\ntoken = \"secret\\nheader\"\n", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, path := tokenFixture(t)
			t.Setenv("AUTH_SECRET", "controller-secret-must-not-be-used")
			client.env = append(client.env, tc.env...)
			if err := os.WriteFile(path, []byte(tc.config), 0o600); err != nil {
				t.Fatal(err)
			}
			tokens, err := readRemoteAPITokens(context.Background(), client)
			if err != nil || !reflect.DeepEqual(tokens, tc.want) {
				t.Fatalf("unexpected credential discovery result: count=%d, err=%v", len(tokens), err)
			}
			if _, err := os.Stat(filepath.Join(client.dir, "injected")); !os.IsNotExist(err) {
				t.Fatal("config was evaluated as shell")
			}
		})
	}
}

func TestRemoteTokenConfigPaths(t *testing.T) {
	for _, location := range []string{"default", "xdg", "tilde"} {
		t.Run(location, func(t *testing.T) {
			client, _ := tokenFixture(t)
			client.env = client.env[:2]
			path := filepath.Join(client.dir, ".config", "agentmux", "config.toml")
			if runtime.GOOS == "darwin" {
				path = filepath.Join(client.dir, "Library", "Application Support", "agentmux", "config.toml")
			}
			if location == "xdg" {
				xdg := filepath.Join(client.dir, "xdg config")
				client.env = append(client.env, "XDG_CONFIG_HOME="+xdg)
				path = filepath.Join(xdg, "agentmux", "config.toml")
			} else if location == "tilde" {
				client.env = append(client.env, "AMUX_CONFIG=~/custom/config.toml")
				path = filepath.Join(client.dir, "custom", "config.toml")
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("[bridge]\ntoken = 'path-secret'\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			tokens, err := readRemoteAPITokens(context.Background(), client)
			if err != nil || len(tokens) != 1 || tokens[0] != "path-secret" {
				t.Fatalf("config path discovery failed: %v", err)
			}
		})
	}
}

func TestRemoteTokenLaunchAgent(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd fixture")
	}
	client, _ := tokenFixture(t)
	dir := filepath.Join(client.dir, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0"?><plist version="1.0"><dict><key>EnvironmentVariables</key><dict><key>AGENTMUX_BRIDGE_TOKEN</key><string>launch&lt;&amp;secret</string></dict></dict></plist>`
	if err := os.WriteFile(filepath.Join(dir, "com.agentmux.client.plist"), []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}
	tokens, err := readRemoteAPITokens(context.Background(), client)
	if err != nil || len(tokens) != 1 || tokens[0] != "launch<&secret" {
		t.Fatalf("launchd token discovery failed: %v", err)
	}
}

type failedTokenClient struct{ tokenScriptClient }

func (*failedTokenClient) Run(context.Context, string, io.Reader) ([]byte, error) {
	return []byte("secret-output"), errors.New("SSH failed: secret-output")
}

func TestRemoteTokenErrorsDoNotExposeSecrets(t *testing.T) {
	_, err := readRemoteAPITokens(context.Background(), &failedTokenClient{})
	if err == nil || strings.Contains(err.Error(), "secret-output") {
		t.Fatal("SSH error exposed credential output")
	}
	client, path := tokenFixture(t)
	if err := os.WriteFile(path, []byte("[bridge]\ntoken = secret-output\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = readRemoteAPITokens(context.Background(), client)
	if err == nil || strings.Contains(err.Error(), "secret-output") {
		t.Fatal("TOML error exposed credential output")
	}
	if err := os.WriteFile(path, []byte("[bridge]\ntoken = 'secret-output'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, r.Header.Get("Authorization"), http.StatusForbidden)
	}))
	defer api.Close()
	host := Host{RemoteAddr: api.Listener.Addr().String()}
	got, _, err := recoverRemoteAPIToken(context.Background(), client, host)
	if err == nil || strings.Contains(err.Error(), "secret-output") || got.APIToken != "" {
		t.Fatal("failed validation exposed or accepted a credential")
	}
}

func TestWaitForStatusReturnsAuthenticationWithoutReadingLogs(t *testing.T) {
	client, _ := tokenFixture(t)
	var requests atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer api.Close()
	_, err := waitForStatus(context.Background(), client, Host{RemoteAddr: api.Listener.Addr().String()}, time.Second, "start")
	var response *remoteHTTPError
	if !errors.As(err, &response) || response.status != 401 || requests.Load() != 1 || client.runs.Load() != 0 {
		t.Fatal("authentication failure was polled, read logs, or lost its error type")
	}
}

func TestManagerAutomaticallyImportsAndRefreshesRemoteToken(t *testing.T) {
	client, path := tokenFixture(t)
	writeToken := func(token string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("[bridge]\ntoken = '"+token+"'\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeToken("import-secret")
	var expected atomic.Value
	expected.Store("import-secret")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+expected.Load().(string) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer api.Close()
	key, keyPath := proxyTestKey(t)
	sshHost, sshPort := startSSHForwarderWithExec(t, key.PublicKey(), func(command string, stdin io.Reader) ([]byte, error) {
		return client.Run(context.Background(), command, stdin)
	}, key)
	storePath := filepath.Join(t.TempDir(), "hosts.json")
	manager, err := NewManager(storePath, 5*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	manager.install = func(context.Context, remoteClient, Host) error {
		t.Error("401 must not install or restart remote service")
		return errors.New("unexpected installation")
	}
	candidate := Host{Name: "auto-token", Host: sshHost, Port: sshPort, User: "tester", KeyPath: keyPath, RemoteAddr: api.Listener.Addr().String()}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err = manager.Import(ctx, candidate, false)
	var untrusted *UnknownHostKeyError
	if !errors.As(err, &untrusted) || client.runs.Load() != 0 || len(manager.List()) != 0 {
		t.Fatal("credential discovery ran before host trust")
	}
	imported, err := manager.Import(ctx, candidate, true)
	if err != nil {
		t.Fatal(err)
	}
	if !imported.OK || !imported.Host.APITokenSet || imported.Installed {
		t.Fatal("authenticated import was not successful")
	}
	encoded, _ := json.Marshal(imported)
	if strings.Contains(string(encoded), "import-secret") {
		t.Fatal("API response exposed token")
	}
	stored := mustStoredHost(t, manager, imported.Host.ID)
	if stored.APIToken != "import-secret" {
		t.Fatal("validated token was not saved")
	}
	info, err := os.Stat(storePath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("host credentials were not saved privately")
	}
	reloaded, err := NewManager(storePath, 5*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if _, err := reloaded.Status(ctx, stored.ID); err != nil {
		t.Fatal(err)
	}
	runs := client.runs.Load()
	if _, err := manager.Import(ctx, candidate, false); err != nil {
		t.Fatal(err)
	}
	if client.runs.Load() != runs {
		t.Fatal("valid saved token triggered unnecessary discovery")
	}
	writeToken("rotated-secret")
	expected.Store("rotated-secret")
	if _, err := manager.Test(ctx, stored.ID, false); err != nil {
		t.Fatal(err)
	}
	if mustStoredHost(t, manager, stored.ID).APIToken != "rotated-secret" {
		t.Fatal("connection test did not persist refreshed token")
	}
	writeToken("invalid-secret")
	expected.Store("different-secret")
	if _, err := manager.Test(ctx, stored.ID, false); err == nil {
		t.Fatal("invalid discovered token was accepted")
	}
	if mustStoredHost(t, manager, stored.ID).APIToken != "rotated-secret" {
		t.Fatal("failed discovery replaced saved token")
	}
	if err := manager.Delete(stored.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Import(ctx, candidate, true); err == nil || len(manager.List()) != 0 {
		t.Fatal("failed import persisted credentials")
	}
}
