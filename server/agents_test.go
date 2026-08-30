package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

func TestAgentsEndpointOnlyReturnsInstalledRuntimes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	home := t.TempDir()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin)
	t.Setenv("NVM_DIR", filepath.Join(home, ".nvm-missing"))
	t.Setenv("PNPM_HOME", filepath.Join(home, ".pnpm-missing"))

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	(&Server{}).handleAgents(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var runtimes []string
	if err := json.Unmarshal(recorder.Body.Bytes(), &runtimes); err != nil {
		t.Fatal(err)
	}
	if !containsString(runtimes, "codex") {
		t.Fatalf("runtimes = %v, want installed codex", runtimes)
	}
	if !containsString(runtimes, "codex-app") {
		t.Fatalf("runtimes = %v, want Codex Desktop runtime when codex is installed", runtimes)
	}
	if containsString(runtimes, "claudecode") {
		t.Fatalf("runtimes = %v, missing claudecode must be filtered", runtimes)
	}
}

func TestFrameworkRuntimeSettingsUsesSignedInCLICatalog(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell mock is unix-only")
	}
	bin := t.TempDir()
	command := filepath.Join(bin, "cursor-agent")
	script := `#!/bin/sh
if [ "$1" = "--list-models" ]; then
  cat <<'MODELS'
Available models
auto - Auto (current, default)
gpt-5.6-sol[reasoning=high,fast=true]
composer-2
Tip: choose a model with --model
MODELS
  exit 0
fi
exit 1
`
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	s, _ := newTestServer(t)
	workDir := t.TempDir()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/frameworks/runtime-settings?kind=cursor&work_dir="+url.QueryEscape(workDir), nil)
	s.mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var response frameworkRuntimeSettingsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Kind != "cursor" || response.Defaults.Model != "auto" {
		t.Fatalf("runtime defaults = %+v", response)
	}
	models := make([]string, 0, len(response.Capabilities.Models))
	for _, option := range response.Capabilities.Models {
		models = append(models, option.Value)
	}
	if !containsString(models, "gpt-5.6-sol") || !containsString(models, "composer-2") {
		t.Fatalf("runtime models = %v", models)
	}
	if len(response.Capabilities.ReasoningEfforts) == 0 || len(response.Capabilities.ServiceTiers) == 0 {
		t.Fatalf("runtime capabilities = %+v", response.Capabilities)
	}
}

func TestAgentCreationRejectsUninstalledRuntime(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("NVM_DIR", filepath.Join(home, ".nvm-missing"))
	t.Setenv("PNPM_HOME", filepath.Join(home, ".pnpm-missing"))

	s, _ := newTestServer(t)
	recorder := doJSON(t, s, http.MethodPost, "/api/v1/agent-instances", core.AgentInstance{
		Name: "Unavailable runtime", RuntimeID: "claudecode", Enabled: true,
	})
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "not installed") {
		t.Fatalf("code = %d body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestCodexDesktopAgentBindsVerifiedDesktopThread(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell mock is unix-only")
	}
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	sessionsDir := filepath.Join(codexHome, "sessions", "2026", "08", "20")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"type":"session_meta","payload":{"id":"desktop-thread","cwd":"/repo","source":"vscode","originator":"Codex Desktop"}}`
	if err := os.WriteFile(filepath.Join(sessionsDir, "rollout-desktop-thread.jsonl"), []byte(meta+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	command := filepath.Join(bin, "codex")
	initResponse := `{"jsonrpc":"2.0","id":1,"result":{}}`
	listResponse := `{"jsonrpc":"2.0","id":2,"result":{"data":[{"id":"desktop-thread","name":"Desktop run","cwd":"/repo"}]}}`
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then echo 'codex-cli 1.0.0'; exit 0; fi\n" +
		"IFS= read -r initialize\n" +
		"printf '%s\\n' '" + initResponse + "'\n" +
		"IFS= read -r initialized\n" +
		"IFS= read -r request\n" +
		"printf '%s\\n' '" + listResponse + "'\n" +
		"sleep 1\n"
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("PATH", bin)
	t.Setenv("NVM_DIR", filepath.Join(home, ".nvm-missing"))
	t.Setenv("PNPM_HOME", filepath.Join(home, ".pnpm-missing"))

	s, _ := newTestServer(t)
	recorder := doJSON(t, s, http.MethodPost, "/api/v1/agent-instances", core.AgentInstance{
		Name: "Desktop Agent", RuntimeID: "codex-app", DesktopThreadID: "desktop-thread", Enabled: true,
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var saved core.AgentInstance
	if err := json.Unmarshal(recorder.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.RuntimeID != "codex-app" || saved.DesktopThreadID != "desktop-thread" || saved.WorkDir != "/repo" || saved.WorkspaceMode != "shared" || saved.SessionBackend != "structured" {
		t.Fatalf("saved = %+v", saved)
	}

	recorder = doJSON(t, s, http.MethodPost, "/api/v1/agent-instances", core.AgentInstance{
		Name: "Duplicate Desktop Agent", RuntimeID: "codex-app", DesktopThreadID: "desktop-thread", Enabled: true,
	})
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "already bound") {
		t.Fatalf("duplicate code = %d body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestAgentInstancesListReturnsEmptyJSONArray(t *testing.T) {
	s, _ := newTestServer(t)
	recorder := doJSON(t, s, http.MethodGet, "/api/v1/agent-instances", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if got := strings.TrimSpace(recorder.Body.String()); got != "[]" {
		t.Fatalf("body = %s, want []", got)
	}
}

func TestFrameworkAuthEndpointReturnsConfigurationStatus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("NVM_DIR", filepath.Join(home, ".nvm-missing"))
	t.Setenv("PNPM_HOME", filepath.Join(home, ".pnpm-missing"))
	addTestRuntimeToPath(t, "cursor-agent")

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/frameworks/auth?kind=cursor", nil)
	(&Server{}).handleFrameworkAuthStatus(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var status struct {
		Kind           string `json:"kind"`
		State          string `json:"state"`
		Installed      bool   `json:"installed"`
		LoginSupported bool   `json:"login_supported"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Kind != "cursor" || status.State != "unknown" || !status.Installed || !status.LoginSupported {
		t.Fatalf("status = %+v", status)
	}
}

func TestFrameworkLoginLifecycleEndpointsHandleMissingSession(t *testing.T) {
	s := &Server{}

	statusRecorder := httptest.NewRecorder()
	statusRequest := httptest.NewRequest(http.MethodGet, "/api/v1/frameworks/login?session_id=missing", nil)
	s.handleFrameworkLoginStatus(statusRecorder, statusRequest)
	if statusRecorder.Code != http.StatusOK || !strings.Contains(statusRecorder.Body.String(), `"active":false`) {
		t.Fatalf("missing login status = %d %s", statusRecorder.Code, statusRecorder.Body.String())
	}

	cancelRecorder := httptest.NewRecorder()
	cancelRequest := httptest.NewRequest(http.MethodPost, "/api/v1/frameworks/login/cancel",
		strings.NewReader(`{"session_id":"missing"}`))
	s.handleFrameworkLoginCancel(cancelRecorder, cancelRequest)
	if cancelRecorder.Code != http.StatusNotFound {
		t.Fatalf("missing login cancel = %d %s", cancelRecorder.Code, cancelRecorder.Body.String())
	}
}

func addTestRuntimeToPath(t *testing.T, name string) {
	t.Helper()
	bin := t.TempDir()
	filename := name
	content := []byte("#!/bin/sh\nexit 0\n")
	if runtime.GOOS == "windows" {
		filename += ".bat"
		content = []byte("@exit /b 0\r\n")
		if os.Getenv("PATHEXT") == "" {
			t.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD")
		}
	}
	if err := os.WriteFile(filepath.Join(bin, filename), content, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}
