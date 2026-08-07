package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	if containsString(runtimes, "claudecode") {
		t.Fatalf("runtimes = %v, missing claudecode must be filtered", runtimes)
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
