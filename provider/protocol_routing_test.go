package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

func TestMultiProtocolAdapterUsesNativeMessages(t *testing.T) {
	var got map[string]any
	var path string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		writeProxyJSON(w, 200, map[string]any{"type": "message", "content": []any{}})
	}))
	defer upstream.Close()
	proxy, st := newTestProxy(t)
	p := &core.Provider{ID: "bridge", BaseURL: upstream.URL + "/v1", Meta: core.ProviderMeta{
		APIFormat: protoResponses, SupportedAPIFormats: []string{protoResponses, protoAnthropic},
	}}
	ctx := context.Background()
	if err := st.UpsertProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActiveProvider(ctx, "claudecode", p.ID); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"model": "model", "messages": []any{map[string]any{
		"role": "user", "content": []any{map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "test"}}},
	}}, "thinking": map[string]any{"type": "adaptive"}}
	resp, _ := postJSON(t, proxy.BaseURL()+"/claude/v1/messages", body)
	if resp.StatusCode != 200 || path != "/v1/messages" || !reflect.DeepEqual(got, body) {
		t.Fatalf("native forwarding status=%d path=%q body=%#v", resp.StatusCode, path, got)
	}
}

func TestAnthropicResponsesSessionAffinityAndToolResult(t *testing.T) {
	ctx := context.Background()
	var received []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		received = append(received, body)
		writeProxyJSON(w, 200, map[string]any{"id": "resp_test", "output": []any{
			map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "ok"}}},
		}})
	}))
	defer upstream.Close()
	proxy, st := newTestProxy(t)
	p := &core.Provider{ID: "stateful", BaseURL: upstream.URL + "/v1", Meta: core.ProviderMeta{APIFormat: protoResponses}}
	if err := st.UpsertProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActiveProvider(ctx, "claudecode", p.ID); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"model": "model", "metadata": map[string]any{"user_id": `{"device_id":"private-device","account_uuid":"private-account","session_id":"thread-a"}`},
		"messages":    []any{map[string]any{"role": "user", "content": "read file"}},
		"tool_choice": map[string]any{"type": "tool", "name": "Read"},
	}
	resp, _ := postJSON(t, proxy.BaseURL()+"/claude/v1/messages", body)
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	body["messages"] = append(body["messages"].([]any),
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "call_1", "name": "Read", "input": map[string]any{"path": "file"}}}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "call_1", "content": "marker"}}},
	)
	postJSON(t, proxy.BaseURL()+"/claude/v1/messages", body)
	body["metadata"] = map[string]any{"user_id": `{"device_id":"private-device","account_uuid":"private-account","session_id":"thread-b"}`}
	postJSON(t, proxy.BaseURL()+"/claude/v1/messages", body)
	key := stringValue(received[0]["prompt_cache_key"])
	if key == "" || received[1]["prompt_cache_key"] != key || received[2]["prompt_cache_key"] == key || strings.Contains(key, "private") {
		t.Fatalf("thread affinity not isolated: %#v", received)
	}
	wantChoice := map[string]any{"type": "function", "name": "Read"}
	if !reflect.DeepEqual(received[0]["tool_choice"], wantChoice) {
		t.Fatalf("choice = %#v", received[0]["tool_choice"])
	}
	input := received[1]["input"].([]any)
	result := input[len(input)-1].(map[string]any)
	if result["type"] != "function_call_output" || result["call_id"] != "call_1" || result["output"] != "marker" {
		t.Fatalf("result = %#v", result)
	}
}

func TestAnthropicSessionIdentityFormats(t *testing.T) {
	key := func(id string) string {
		return anthropicResponseCacheKey(map[string]any{"metadata": map[string]any{"user_id": id}})
	}
	if key(`{"session_id":"thread-a"}`) != key("user_device_account_account_session_thread-a") {
		t.Fatal("legacy and JSON identities diverge")
	}
	for _, id := range []string{"account-only", "user_device_account_account", `{"account_uuid":"account"}`, "", "user_device_session_"} {
		if key(id) != "" {
			t.Fatalf("account identity became thread key: %q", id)
		}
	}
}

func TestCodexTakeoverSwitchRefreshesCatalogAndRestores(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	original := "model = \"original\"\n"
	if err := os.WriteFile(configPath, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"provider-a", "provider-b"} {
		p := &core.Provider{ID: id, Name: id, BaseURL: "http://127.0.0.1:19000/v1", Model: id + "-model", SettingsConfig: map[string]any{"codex_home": dir}, Meta: core.ProviderMeta{APIFormat: protoResponses}}
		if err := svc.Upsert(ctx, p); err != nil {
			t.Fatal(err)
		}
		if err := svc.SwitchRouteWithLocalTakeover(ctx, core.ProviderRoute{Tool: "codex", ProviderID: id}, true); err != nil {
			t.Fatal(err)
		}
		config := readTOMLObject(configPath)
		if config["model"] != id+"-model" {
			t.Fatalf("default model = %v", config["model"])
		}
		catalog := readJSON(t, filepath.Join(dir, codexModelCatalogFilename))
		models := catalog["models"].([]any)
		if len(models) != 1 || models[0].(map[string]any)["slug"] != id+"-model" {
			t.Fatalf("stale catalog: %#v", models)
		}
		blob, _, _ := st.GetLiveBackup(ctx, "codex")
		if err := validateLiveOwnershipSnapshot(blob); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.DisableTakeover(ctx, "codex"); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(configPath)
	if string(restored) != original {
		t.Fatalf("restore = %s", restored)
	}
	if _, err := os.Stat(filepath.Join(dir, codexModelCatalogFilename)); !os.IsNotExist(err) {
		t.Fatalf("generated catalog not removed: %v", err)
	}
}
