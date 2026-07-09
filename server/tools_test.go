package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/agentnexus/agentnexus/core"
	"github.com/agentnexus/agentnexus/mcp"
	"github.com/agentnexus/agentnexus/skills"
	"github.com/agentnexus/agentnexus/workspace"
)

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
