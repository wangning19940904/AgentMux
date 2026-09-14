package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/skills"
)

func TestToolDescriptionSaveAndCatalogRead(t *testing.T) {
	s, _ := newTestServer(t)
	description := "自定义描述\n保留换行"
	rec := doJSON(t, s, http.MethodPost, "/api/v1/tools/description", map[string]any{
		"kind": "cli", "id": "github-cli", "description": "  " + description + "  ",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("save = %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/api/v1/tools", nil)
	var body toolsResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
		t.Fatalf("catalog = %d %s", rec.Code, rec.Body.String())
	}
	for _, cli := range body.CLI {
		if cli.Spec.ID == "github-cli" {
			if cli.Spec.Note != description {
				t.Fatalf("catalog note = %q", cli.Spec.Note)
			}
			return
		}
	}
	t.Fatal("CLI missing from catalog")
}

func TestToolDescriptionValidatesInput(t *testing.T) {
	s, _ := newTestServer(t)
	for _, tc := range []struct {
		body map[string]any
		code int
	}{
		{map[string]any{"kind": "cli", "id": "github-cli"}, http.StatusBadRequest},
		{map[string]any{"kind": "cli", "id": "unknown", "description": "text"}, http.StatusNotFound},
		{map[string]any{"kind": "other", "id": "github-cli", "description": "text"}, http.StatusBadRequest},
		{map[string]any{"kind": "cli", "id": "github-cli", "description": strings.Repeat("字", 4001)}, http.StatusBadRequest},
		{map[string]any{"kind": "cli", "id": "github-cli", "description": ""}, http.StatusOK},
	} {
		rec := doJSON(t, s, http.MethodPost, "/api/v1/tools/description", tc.body)
		if rec.Code != tc.code {
			t.Fatalf("response = %d %s, want %d", rec.Code, rec.Body.String(), tc.code)
		}
	}
}

func TestSkillDescriptionEndpointUpdatesRuntimeFile(t *testing.T) {
	s, _ := newTestServer(t)
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte("---\nname: demo\ndescription: old\n---\n# Instructions\nKeep this.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.SetModules(nil, skills.New(root), nil, nil)
	rec := doJSON(t, s, http.MethodPost, "/api/v1/tools/description", map[string]any{
		"kind": "skill", "id": "demo", "description": "新的描述\n包含多行。",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("save = %d %s", rec.Code, rec.Body.String())
	}
	if got := skills.ParseSkillFile(path); got.Description != "新的描述\n包含多行。" {
		t.Fatalf("runtime skill = %+v", got)
	}
	rec = doJSON(t, s, http.MethodPost, "/api/v1/tools/description", map[string]any{
		"kind": "skill", "id": "../missing", "description": "text",
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing skill = %d %s", rec.Code, rec.Body.String())
	}
}
