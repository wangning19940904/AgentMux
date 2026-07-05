package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentnexus/agentnexus/core"
	"github.com/agentnexus/agentnexus/store"
)

func newTestProxy(t *testing.T) (*ProxyServer, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := NewProxyServer(nil, st, "127.0.0.1:0")
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Stop() })
	return srv, st
}

func postJSON(t *testing.T, url string, body map[string]any) (*http.Response, []byte) {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, data
}

func TestProxyAnthropicPassthroughInjectsAuth(t *testing.T) {
	ctx := context.Background()
	var gotAuth, gotAPIKey, gotPath, gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("x-api-key")
		gotPath = r.URL.Path
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel, _ = body["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","content":[{"type":"text","text":"hi"}]}`))
	}))
	defer upstream.Close()

	proxy, st := newTestProxy(t)
	t.Setenv("TEST_RELAY_KEY", "sk-real")
	p := &core.Provider{
		ID: "relay", Name: "Relay", BaseURL: upstream.URL,
		APIKeyEnv: "TEST_RELAY_KEY", Tools: []string{"claudecode"},
		Meta: core.ProviderMeta{APIFormat: "anthropic", ClaudeSonnetModel: "relay-sonnet"},
	}
	if err := st.UpsertProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActiveProvider(ctx, "claudecode", p.ID); err != nil {
		t.Fatal(err)
	}
	resp, data := postJSON(t, proxy.BaseURL()+"/v1/messages", map[string]any{
		"model": "claude-sonnet-4-8", "max_tokens": 10,
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, data)
	}
	if gotAuth != "Bearer sk-real" || gotAPIKey != "sk-real" {
		t.Fatalf("auth = %q apikey = %q", gotAuth, gotAPIKey)
	}
	if gotPath != "/v1/messages" {
		t.Fatalf("path = %q", gotPath)
	}
	// Tier mapping: claude-sonnet-* -> provider's ClaudeSonnetModel.
	if gotModel != "relay-sonnet" {
		t.Fatalf("model = %q", gotModel)
	}
}

func TestProxyFailoverAndHotSwitch(t *testing.T) {
	ctx := context.Background()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_ok","type":"message","content":[]}`))
	}))
	defer good.Close()

	proxy, st := newTestProxy(t)
	primary := &core.Provider{
		ID: "primary", Name: "Primary", BaseURL: bad.URL,
		Tools: []string{"claudecode"}, Meta: core.ProviderMeta{APIFormat: "anthropic"},
	}
	backup := &core.Provider{
		ID: "backup", Name: "Backup", BaseURL: good.URL,
		Tools: []string{"claudecode"}, Meta: core.ProviderMeta{APIFormat: "anthropic"},
		InFailoverQueue: true, SortIndex: 1,
	}
	for _, p := range []*core.Provider{primary, backup} {
		if err := st.UpsertProvider(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetActiveProvider(ctx, "claudecode", primary.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProxyToolConfig(ctx, store.ProxyToolConfig{
		Tool: "claudecode", AutoFailover: true, MaxRetries: 3, FailureThreshold: 4, CooldownSeconds: 60,
	}); err != nil {
		t.Fatal(err)
	}

	resp, data := postJSON(t, proxy.BaseURL()+"/v1/messages", map[string]any{
		"model": "claude-sonnet-4-8", "messages": []any{},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "msg_ok") {
		t.Fatalf("body = %s", data)
	}
	// Failover hot-switches the active provider (cc-switch parity).
	id, ok, err := st.ActiveProviderID(ctx, "claudecode")
	if err != nil || !ok || id != backup.ID {
		t.Fatalf("active after failover = %q,%v,%v", id, ok, err)
	}
}

func TestProxyOpenAIChatConversion(t *testing.T) {
	ctx := context.Background()
	var gotBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "wrong path "+r.URL.Path, http.StatusNotFound)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-1","model":"glm-4.6",
			"choices":[{"message":{"role":"assistant","content":"你好",
				"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.txt\"}"}}]},
				"finish_reason":"tool_calls"}],
			"usage":{"prompt_tokens":12,"completion_tokens":34}
		}`))
	}))
	defer upstream.Close()

	proxy, st := newTestProxy(t)
	t.Setenv("GLM_KEY", "sk-glm")
	p := &core.Provider{
		ID: "glm", Name: "GLM", BaseURL: upstream.URL + "/v1",
		APIKeyEnv: "GLM_KEY", Model: "glm-4.6", Tools: []string{"claudecode"},
		Meta: core.ProviderMeta{APIFormat: "openai_chat"},
	}
	if err := st.UpsertProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActiveProvider(ctx, "claudecode", p.ID); err != nil {
		t.Fatal(err)
	}

	resp, data := postJSON(t, proxy.BaseURL()+"/v1/messages", map[string]any{
		"model": "claude-sonnet-4-8", "max_tokens": 100,
		"system":   "be nice",
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
		"tools": []any{map[string]any{
			"name": "read_file", "description": "read a file",
			"input_schema": map[string]any{"type": "object"},
		}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, data)
	}
	// Upstream saw an OpenAI chat request with the provider model.
	if gotBody["model"] != "glm-4.6" {
		t.Fatalf("upstream model = %v", gotBody["model"])
	}
	messages := gotBody["messages"].([]any)
	first := messages[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "be nice" {
		t.Fatalf("system message = %#v", first)
	}
	// Client got an Anthropic response with text + tool_use.
	var anthropic map[string]any
	if err := json.Unmarshal(data, &anthropic); err != nil {
		t.Fatal(err)
	}
	if anthropic["type"] != "message" || anthropic["stop_reason"] != "tool_use" {
		t.Fatalf("anthropic response = %s", data)
	}
	content := anthropic["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content = %#v", content)
	}
	toolUse := content[1].(map[string]any)
	if toolUse["type"] != "tool_use" || toolUse["name"] != "read_file" {
		t.Fatalf("tool_use = %#v", toolUse)
	}
	usage := anthropic["usage"].(map[string]any)
	if usage["input_tokens"].(float64) != 12 || usage["output_tokens"].(float64) != 34 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestProxyOpenAIChatStreamConversion(t *testing.T) {
	ctx := context.Background()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"id":"c1","model":"glm-4.6","choices":[{"delta":{"content":"He"}}]}`,
			`{"id":"c1","choices":[{"delta":{"content":"llo"}}]}`,
			`{"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
		}
		for _, chunk := range chunks {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	proxy, st := newTestProxy(t)
	p := &core.Provider{
		ID: "glm-stream", Name: "GLM", BaseURL: upstream.URL,
		Model: "glm-4.6", Tools: []string{"claudecode"},
		Meta: core.ProviderMeta{APIFormat: "openai_chat"},
	}
	if err := st.UpsertProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActiveProvider(ctx, "claudecode", p.ID); err != nil {
		t.Fatal(err)
	}
	resp, data := postJSON(t, proxy.BaseURL()+"/v1/messages", map[string]any{
		"model": "claude-sonnet-4-8", "stream": true,
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, data)
	}
	out := string(data)
	for _, want := range []string{
		"event: message_start",
		"event: content_block_start",
		`"text_delta"`,
		`"text":"He"`,
		"event: message_delta",
		`"stop_reason":"end_turn"`,
		`"output_tokens":2`,
		"event: message_stop",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stream missing %q:\n%s", want, out)
		}
	}
}

func TestProxyClaudeDesktopGateway(t *testing.T) {
	ctx := context.Background()
	var gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel, _ = body["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_cd","type":"message","content":[]}`))
	}))
	defer upstream.Close()

	proxy, st := newTestProxy(t)
	p := &core.Provider{
		ID: "cd-provider", Name: "Desktop Provider", BaseURL: upstream.URL,
		Tools: []string{"claude-desktop"},
		Meta: core.ProviderMeta{
			APIFormat: "anthropic",
			ClaudeDesktopModels: []core.ClaudeDesktopModel{
				{ID: "claude-sonnet-4-8", DisplayName: "Sonnet", UpstreamModel: "upstream-sonnet"},
			},
		},
	}
	if err := st.UpsertProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActiveProvider(ctx, "claude-desktop", p.ID); err != nil {
		t.Fatal(err)
	}
	token, err := st.GetOrCreateGatewayToken(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Missing token -> 401.
	resp, _ := postJSON(t, proxy.BaseURL()+"/claude-desktop/v1/messages", map[string]any{"model": "claude-sonnet-4-8"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", resp.StatusCode)
	}

	do := func(model string) *http.Response {
		raw, _ := json.Marshal(map[string]any{"model": model, "messages": []any{}})
		req, _ := http.NewRequest(http.MethodPost, proxy.BaseURL()+"/claude-desktop/v1/messages", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}

	// Known route maps to the upstream model.
	if resp := do("claude-sonnet-4-8"); resp.StatusCode != http.StatusOK {
		t.Fatalf("routed status = %d", resp.StatusCode)
	}
	if gotModel != "upstream-sonnet" {
		t.Fatalf("upstream model = %q", gotModel)
	}
	// Unknown route is a hard error (no silent default).
	if resp := do("claude-nonexistent-9"); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown route status = %d", resp.StatusCode)
	}

	// Models listing serves the provider's route table.
	req, _ := http.NewRequest(http.MethodGet, proxy.BaseURL()+"/claude-desktop/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	modelsResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer modelsResp.Body.Close()
	modelsRaw, _ := io.ReadAll(modelsResp.Body)
	if !strings.Contains(string(modelsRaw), "claude-sonnet-4-8") {
		t.Fatalf("models = %s", modelsRaw)
	}
}

func TestProxyCodexPassthroughChecksWireAPI(t *testing.T) {
	ctx := context.Background()
	var gotPath, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-2","choices":[]}`))
	}))
	defer upstream.Close()

	proxy, st := newTestProxy(t)
	t.Setenv("CODEX_RELAY_KEY", "sk-codex")
	p := &core.Provider{
		ID: "codex-relay", Name: "Codex Relay", BaseURL: upstream.URL + "/v1",
		APIKeyEnv: "CODEX_RELAY_KEY", Tools: []string{"codex"},
		Meta: core.ProviderMeta{CodexWireAPI: "chat", APIFormat: "openai_chat"},
	}
	if err := st.UpsertProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActiveProvider(ctx, "codex", p.ID); err != nil {
		t.Fatal(err)
	}
	resp, data := postJSON(t, proxy.BaseURL()+"/v1/chat/completions", map[string]any{
		"model": "glm-4.6", "messages": []any{},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, data)
	}
	if gotPath != "/v1/chat/completions" || gotAuth != "Bearer sk-codex" {
		t.Fatalf("path = %q auth = %q", gotPath, gotAuth)
	}
	// A responses call cannot be served by a chat-only provider.
	resp, data = postJSON(t, proxy.BaseURL()+"/v1/responses", map[string]any{"model": "glm-4.6"})
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("responses status = %d body=%s", resp.StatusCode, data)
	}
}
