package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/mcp"
	"github.com/wangning19940904/AgentMux/skills"
	"github.com/wangning19940904/AgentMux/workspace"
)

func TestInstallProgressStreamFlushesStagesAndResult(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/test/install/stream", nil)

	streamInstall(recorder, request, func(report func(string, string, int)) any {
		report("preparing", "", 5)
		report("installing", "npm install example", 30)
		return map[string]any{"ok": true}
	})

	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("content type = %q", contentType)
	}
	body := recorder.Body.String()
	for _, want := range []string{`"phase":"preparing"`, `"phase":"installing"`, `"phase":"complete"`, `"type":"result"`, `"ok":true`} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream missing %q: %s", want, body)
		}
	}
	if strings.Index(body, `"phase":"installing"`) > strings.Index(body, `"type":"result"`) {
		t.Fatalf("result arrived before buffered progress: %s", body)
	}
}

func TestToolsEndpointAggregatesModules(t *testing.T) {
	s, st := newTestServer(t)
	s.SetModules(nil, skills.New(t.TempDir()), mcp.New(st), nil)

	rec := doJSON(t, s, http.MethodGet, "/api/v1/tools", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body.String())
	}
	var body toolsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.CLI) == 0 || len(body.Frameworks) == 0 {
		t.Fatalf("tools response = %+v", body)
	}
}

func TestCLIAuthEndpointsValidateCatalogAndSession(t *testing.T) {
	s, _ := newTestServer(t)

	rec := doJSON(t, s, http.MethodGet, "/api/v1/tools/cli/auth", nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "cli id is required") {
		t.Fatalf("missing id response = %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPost, "/api/v1/tools/cli/auth/login", map[string]any{"id": "curl"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unknown CLI") {
		t.Fatalf("unknown cli response = %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodGet, "/api/v1/tools/cli/auth/login?session_id=missing", nil)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "not found") {
		t.Fatalf("missing session response = %d %s", rec.Code, rec.Body.String())
	}
}

func TestSkillsMarketplaceEndpointFilters(t *testing.T) {
	s, _ := newTestServer(t)
	s.SetModules(nil, skills.New(t.TempDir()), nil, nil)

	rec := doJSON(t, s, http.MethodGet, "/api/v1/skills/marketplace?q=pdf", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body.String())
	}
	var items []skills.MarketplaceSkill
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 || items[0].Name != "pdf" {
		t.Fatalf("items = %+v", items)
	}
}

func TestAgentInitializeEndpointUsesWorkspaceInitializer(t *testing.T) {
	s, _ := newTestServer(t)
	s.SetWorkspaceInitializer(workspace.New(t.TempDir()))
	workDir := t.TempDir()

	rec := doJSON(t, s, http.MethodPost, "/api/v1/agent-instances/initialize", core.WorkspaceInitOptions{
		RuntimeID: "codex",
		WorkDir:   workDir,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(workDir, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
}

func TestCLIInstallEndpointRejectsUnknownTool(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodPost, "/api/v1/tools/cli/install", cliInstallRequest{ID: "curl", Action: "install"})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["ok"].(bool) {
		t.Fatalf("expected rejected result: %+v", body)
	}
}

func TestCLICheckEndpointRejectsUnknownTool(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodPost, "/api/v1/tools/cli/check", cliInstallRequest{ID: "curl"})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if errText, _ := body["error"].(string); errText == "" {
		t.Fatalf("expected rejected result: %+v", body)
	}
}

func TestCLISkillSyncEndpointRejectsUnknownTool(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodPost, "/api/v1/tools/cli/skills/sync", cliInstallRequest{ID: "curl"})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if errText, _ := body["error"].(string); errText == "" {
		t.Fatalf("expected rejected result: %+v", body)
	}
}

func TestFrameworkCheckEndpointRejectsUnknownFramework(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodPost, "/api/v1/frameworks/check", frameworkInstallRequest{Kind: "not-a-framework"})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if errText, _ := body["error"].(string); errText == "" {
		t.Fatalf("expected rejected result: %+v", body)
	}
}
