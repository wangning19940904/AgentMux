package server

import (
	"context"
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
	if len(body.CLI) == 0 || len(body.Frameworks) == 0 || len(body.Bundles) == 0 {
		t.Fatalf("tools response = %+v", body)
	}
}

func TestInternalInstallEndpointsRequireAcknowledgement(t *testing.T) {
	s, _ := newTestServer(t)

	rec := doJSON(t, s, http.MethodPost, "/api/v1/tools/bundles/install", bundleInstallRequest{ID: "bytedance-internal"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "explicit acknowledgement") {
		t.Fatalf("bundle response = %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/api/v1/tools/cli/install", cliInstallRequest{ID: "bytedcli", Action: "install"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "explicit acknowledgement") {
		t.Fatalf("CLI response = %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/api/v1/frameworks/install", frameworkInstallRequest{Kind: "traecli", Action: "install"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "explicit acknowledgement") {
		t.Fatalf("framework response = %d %s", rec.Code, rec.Body.String())
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

func TestValidateAgentMCPServers(t *testing.T) {
	s, st := newTestServer(t)
	registry := mcp.New(st)
	if err := registry.Upsert(context.Background(), &core.MCPServer{
		Name: "files", Transport: "stdio", Command: "npx", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	s.mcp = registry
	if err := s.validateAgentMCPServers(context.Background(), &core.AgentInstance{
		RuntimeID: "claudecode", MCPServers: []string{"files"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.validateAgentMCPServers(context.Background(), &core.AgentInstance{
		RuntimeID: "codex-app", MCPServers: []string{"files"},
	}); err == nil {
		t.Fatal("expected unsupported runtime error")
	}
	if err := s.validateAgentMCPServers(context.Background(), &core.AgentInstance{
		RuntimeID: "codex", MCPServers: []string{"missing"},
	}); err == nil {
		t.Fatal("expected missing MCP server error")
	}
}

func TestSkillUninstallEndpointRemovesManagedSkill(t *testing.T) {
	s, _ := newTestServer(t)
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: demo\ndescription: Demo\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.SetModules(nil, skills.New(root), nil, nil)

	rec := doJSON(t, s, http.MethodDelete, "/api/v1/skills?name=demo", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("skill directory still exists: %v", err)
	}
	rec = doJSON(t, s, http.MethodDelete, "/api/v1/skills?name=demo", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing skill code = %d body = %s", rec.Code, rec.Body.String())
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
