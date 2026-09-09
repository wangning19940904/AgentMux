package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

func TestBlockedModelsPersistAndReplaceDefault(t *testing.T) {
	st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "models.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	manager := NewManager(st)
	p := &core.Provider{ID: "relay", Model: "bad", Meta: core.ProviderMeta{SupportedModels: []string{"bad", "ready"}, BlockedModels: []string{"bad"}}}
	if err := manager.Upsert(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	saved, err := manager.Get(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Model != "ready" || !saved.ModelBlocked("bad") {
		t.Fatalf("saved = %+v", saved)
	}
	if got := codexCatalogModels(saved); !reflect.DeepEqual(got, []string{"ready"}) {
		t.Fatalf("catalog = %v", got)
	}
	saved.Meta.BlockedModels = []string{"bad", "ready"}
	if err := manager.Upsert(context.Background(), saved); err != nil {
		t.Fatal(err)
	}
	if saved.Model != "" {
		t.Fatalf("all blocked default = %s", saved.Model)
	}
}

func TestResolveUpstreamRejectsBlockedModelsAndAliases(t *testing.T) {
	p := &core.Provider{ID: "relay", Model: "ready", Meta: core.ProviderMeta{SupportedModels: []string{"ready", "bad"}, BlockedModels: []string{"bad"}, ClaudeOpusModel: "bad"}}
	for _, model := range []string{"bad", "claude-bad", "claude-bad[1m]", "claude-opus-4"} {
		if _, err := resolveUpstreamModel(p, map[string]any{"model": model}, nil); err == nil {
			t.Errorf("accepted blocked model %s", model)
		}
	}
	if _, err := resolveUpstreamModel(p, map[string]any{"model": "desktop-alias"}, func(_ *core.Provider, body map[string]any) error { body["model"] = "bad"; return nil }); err == nil {
		t.Error("accepted blocked mapped model")
	}
	if model, err := resolveUpstreamModel(p, map[string]any{"model": "ready"}, nil); err != nil || model != "ready" {
		t.Fatalf("ready = %s, %v", model, err)
	}
	p.Meta.BlockedModels = nil
	if model, err := resolveUpstreamModel(p, map[string]any{"model": "bad"}, nil); err != nil || model != "bad" {
		t.Fatalf("restored = %s, %v", model, err)
	}
}

func TestProxyModelCatalogsExcludeBlockedModels(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "ready"}, {"id": "bad"}}})
	}))
	defer upstream.Close()
	t.Setenv("AGENTMUX_BLOCK_CATALOG_KEY", "test-key")
	proxy, st := newTestProxy(t)
	p := &core.Provider{ID: "relay", BaseURL: upstream.URL, APIKeyEnv: "AGENTMUX_BLOCK_CATALOG_KEY", Model: "ready", Meta: core.ProviderMeta{SupportedModels: []string{"ready", "bad"}, BlockedModels: []string{"bad"}, ClaudeDesktopModels: []core.ClaudeDesktopModel{{ID: "claude-opus-4", UpstreamModel: "bad"}}}}
	if err := st.UpsertProvider(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"codex", "claudecode"} {
		if err := st.SetActiveProvider(context.Background(), tool, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"/v1/models", "/claude/v1/models"} {
		resp, body := getJSON(t, proxy.BaseURL()+path, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d: %v", path, resp.StatusCode, body)
		}
		data, _ := body["data"].([]any)
		if len(data) != 1 {
			t.Fatalf("%s: models=%v", path, data)
		}
		id := stringValue(data[0].(map[string]any)["id"])
		if id != "ready" && id != "claude-ready" {
			t.Fatalf("%s: model=%s", path, id)
		}
	}
}
