package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

func newTestProxy(t *testing.T) (*ProxyServer, *store.Store) {
	t.Helper()
	st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "test.db"))
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

func getJSON(t *testing.T, url string, headers map[string]string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return resp, body
}

func latestTrace(t *testing.T, st *store.Store, tool string) core.ProxyTrace {
	t.Helper()
	traces, err := st.QueryProxyTraces(context.Background(), tool, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 1 {
		t.Fatalf("trace count = %d, want 1", len(traces))
	}
	return traces[0]
}

func TestProxyTraceStoresGenericErrorAndForwardsDetailToEncryptedObserver(t *testing.T) {
	srv, st := newTestProxy(t)
	var observedBody []byte
	var observedTrace core.ProxyTrace
	srv.SetTraceObserver(func(_ context.Context, trace core.ProxyTrace, _, responseBody []byte) error {
		observedTrace = trace
		observedBody = append([]byte(nil), responseBody...)
		return nil
	})
	request := httptest.NewRequest(http.MethodPost, "http://localhost/v1/messages", nil)
	provider := &core.Provider{ID: "provider", Name: "Provider"}
	srv.recordProxyTrace(request, map[string]any{"model": "model"}, forwardOpts{tool: "claudecode", clientProto: protoAnthropic},
		provider, "model", "model", proxyRequestIdentity{RequestID: "request"}, "attempt", "", 1,
		proxyAttemptResult{Err: errors.New("upstream-secret-error"), Upstream: protoAnthropic})
	stored := latestTrace(t, st, "claudecode")
	if stored.Error != "Proxy request failed" || observedTrace.Error != "Proxy request failed" || !bytes.Contains(observedBody, []byte("upstream-secret-error")) {
		t.Fatalf("stored=%+v observed=%+v body=%q", stored, observedTrace, observedBody)
	}
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
		APIKeyEnv: "TEST_RELAY_KEY",
		Meta:      core.ProviderMeta{APIFormat: "anthropic"},
	}
	if err := st.UpsertProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActiveProviderRoute(ctx, core.ProviderRoute{
		Tool:       "claudecode",
		ProviderID: p.ID,
		Meta:       core.ProviderMeta{ClaudeSonnetModel: "relay-sonnet"},
	}); err != nil {
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
	trace := latestTrace(t, st, "claudecode")
	if !trace.Success || trace.ProviderID != "relay" || trace.ClientModel != "claude-sonnet-4-8" || trace.UpstreamModel != "relay-sonnet" {
		t.Fatalf("trace = %+v", trace)
	}
	if trace.ClientProtocol != "anthropic" || trace.UpstreamProtocol != "anthropic" {
		t.Fatalf("trace protocols = %+v", trace)
	}
}

func TestProxyClaudeCodeModelsFollowActiveRouteAndSelectedModel(t *testing.T) {
	ctx := context.Background()
	var gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel, _ = body["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_models","type":"message","content":[]}`))
	}))
	defer upstream.Close()

	proxy, st := newTestProxy(t)
	provider := &core.Provider{
		ID: "catalog", Name: "Catalog", BaseURL: upstream.URL, Model: "catalog-default",
		Meta: core.ProviderMeta{
			APIFormat:       "anthropic",
			SupportedModels: []string{"catalog-default", "catalog-fast", "opensource/glm5.2", "claude-native", "catalog-fast"},
		},
	}
	if err := st.UpsertProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActiveProviderRoute(ctx, core.ProviderRoute{
		Tool:       "claudecode",
		ProviderID: provider.ID,
		Meta: core.ProviderMeta{
			ClaudeSonnetModel: "catalog-sonnet",
			ClaudeDesktopModels: []core.ClaudeDesktopModel{
				{ID: "claude-sonnet-5", DisplayName: "Claude Sonnet 5", UpstreamModel: "catalog-fast"},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	assertCatalog := func(path string, headers map[string]string, wantIDs, wantNames []string) {
		t.Helper()
		resp, body := getJSON(t, proxy.BaseURL()+path, headers)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d body=%v", path, resp.StatusCode, body)
		}
		raw, _ := body["data"].([]any)
		gotIDs := make([]string, 0, len(raw))
		gotNames := make([]string, 0, len(raw))
		for _, item := range raw {
			entry, _ := item.(map[string]any)
			gotIDs = append(gotIDs, stringValue(entry["id"]))
			gotNames = append(gotNames, stringValue(entry["display_name"]))
		}
		if strings.Join(gotIDs, ",") != strings.Join(wantIDs, ",") {
			t.Fatalf("GET %s model ids = %v want %v", path, gotIDs, wantIDs)
		}
		if strings.Join(gotNames, ",") != strings.Join(wantNames, ",") {
			t.Fatalf("GET %s model names = %v want %v", path, gotNames, wantNames)
		}
	}
	wantIDs := []string{"claude-sonnet-5", "claude-catalog-default", "claude-catalog-fast", "claude-opensource/glm5.2", "claude-claude-native"}
	wantNames := []string{"Claude Sonnet 5", "catalog-default", "catalog-fast", "opensource/glm5.2", "claude-native"}
	assertCatalog("/claude/v1/models", nil, wantIDs, wantNames)
	// Legacy root takeover configs are dispatched by Anthropic request headers.
	assertCatalog("/v1/models", map[string]string{"anthropic-version": "2023-06-01"}, wantIDs, wantNames)

	resp, data := postJSON(t, proxy.BaseURL()+"/claude/v1/messages", map[string]any{
		"model": "claude-catalog-fast", "max_tokens": 10,
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, data)
	}
	if gotModel != "catalog-fast" {
		t.Fatalf("selected alias was not mapped to provider model: got %q", gotModel)
	}

	resp, data = postJSON(t, proxy.BaseURL()+"/claude/v1/messages", map[string]any{
		"model": "claude-sonnet-5", "max_tokens": 10,
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, data)
	}
	if gotModel != "catalog-fast" {
		t.Fatalf("configured route alias was not mapped to provider model: got %q", gotModel)
	}

	resp, data = postJSON(t, proxy.BaseURL()+"/claude/v1/messages", map[string]any{
		"model": "claude-opensource/glm5.2", "max_tokens": 10,
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, data)
	}
	if gotModel != "opensource/glm5.2" {
		t.Fatalf("provider model containing a slash was not mapped: got %q", gotModel)
	}

	resp, data = postJSON(t, proxy.BaseURL()+"/claude/v1/messages", map[string]any{
		"model": "claude-claude-native", "max_tokens": 10,
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, data)
	}
	if gotModel != "claude-native" {
		t.Fatalf("already-prefixed provider model was not mapped: got %q", gotModel)
	}

	other := &core.Provider{
		ID: "other-catalog", Name: "Other Catalog", Model: "other-default",
		Meta: core.ProviderMeta{APIFormat: "anthropic", SupportedModels: []string{"other-fast"}},
	}
	if err := st.UpsertProvider(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActiveProviderRoute(ctx, core.ProviderRoute{Tool: "claudecode", ProviderID: other.ID}); err != nil {
		t.Fatal(err)
	}
	assertCatalog("/claude/v1/models", nil,
		[]string{"claude-other-default", "claude-other-fast"},
		[]string{"other-default", "other-fast"})
}

func TestProxyReportsMissingProviderAPIKeyEnv(t *testing.T) {
	ctx := context.Background()
	hitUpstream := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitUpstream = true
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	defer upstream.Close()

	proxy, st := newTestProxy(t)
	p := &core.Provider{
		ID: "relay", Name: "Relay", BaseURL: upstream.URL,
		APIKeyEnv: "MISSING_PROXY_KEY",
		Meta:      core.ProviderMeta{APIFormat: "anthropic"},
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
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d body=%s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "environment variable MISSING_PROXY_KEY is empty or not set") {
		t.Fatalf("missing env error not surfaced: %s", data)
	}
	if hitUpstream {
		t.Fatal("proxy should not call upstream without the configured key")
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
		Meta: core.ProviderMeta{APIFormat: "anthropic"},
	}
	backup := &core.Provider{
		ID: "backup", Name: "Backup", BaseURL: good.URL,
		Meta:            core.ProviderMeta{APIFormat: "anthropic"},
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
		APIKeyEnv: "GLM_KEY", Model: "glm-4.6",
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
	trace := latestTrace(t, st, "claudecode")
	if !trace.Success || trace.ClientProtocol != "anthropic" || trace.UpstreamProtocol != "openai_chat" {
		t.Fatalf("trace protocols = %+v", trace)
	}
	if trace.ClientModel != "claude-sonnet-4-8" || trace.UpstreamModel != "glm-4.6" || trace.ProviderID != "glm" {
		t.Fatalf("trace models = %+v", trace)
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
		Model: "glm-4.6",
		Meta:  core.ProviderMeta{APIFormat: "openai_chat"},
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
		Meta: core.ProviderMeta{
			APIFormat: "anthropic",
			ClaudeDesktopModels: []core.ClaudeDesktopModel{
				{ID: "ark/60b-0614c", DisplayName: "ark/60b-0614c"},
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
	resp, _ := postJSON(t, proxy.BaseURL()+"/claude-desktop/v1/messages", map[string]any{"model": "claude-sonnet-5"})
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
	if resp := do("claude-sonnet-5"); resp.StatusCode != http.StatusOK {
		t.Fatalf("routed status = %d", resp.StatusCode)
	}
	if gotModel != "ark/60b-0614c" {
		t.Fatalf("upstream model = %q", gotModel)
	}
	if resp := do("ark/60b-0614c"); resp.StatusCode != http.StatusOK {
		t.Fatalf("legacy raw route status = %d", resp.StatusCode)
	}
	if gotModel != "ark/60b-0614c" {
		t.Fatalf("legacy upstream model = %q", gotModel)
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
	if !strings.Contains(string(modelsRaw), "claude-sonnet-5") || !strings.Contains(string(modelsRaw), "ark/60b-0614c") {
		t.Fatalf("models = %s", modelsRaw)
	}
	if strings.Contains(string(modelsRaw), `"id":"ark/60b-0614c"`) {
		t.Fatalf("raw upstream id leaked into model list: %s", modelsRaw)
	}
}

func TestProxyCodexChatPassthrough(t *testing.T) {
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
		APIKeyEnv: "CODEX_RELAY_KEY",
		Meta:      core.ProviderMeta{APIFormat: "openai_chat"},
	}
	if err := st.UpsertProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActiveProvider(ctx, "codex", p.ID); err != nil {
		t.Fatal(err)
	}
	// Same-protocol (openai_chat client -> openai_chat upstream) passes through.
	resp, data := postJSON(t, proxy.BaseURL()+"/v1/chat/completions", map[string]any{
		"model": "glm-4.6", "messages": []any{},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, data)
	}
	if gotPath != "/v1/chat/completions" || gotAuth != "Bearer sk-codex" {
		t.Fatalf("path = %q auth = %q", gotPath, gotAuth)
	}
}

func TestProxyGeminiUpstreamServesClaudeClient(t *testing.T) {
	ctx := context.Background()
	var gotPath, gotKey string
	var gotBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-goog-api-key")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates":[{"content":{"role":"model","parts":[{"text":"你好"}]},"finishReason":"STOP"}],
			"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":22}
		}`))
	}))
	defer upstream.Close()

	proxy, st := newTestProxy(t)
	t.Setenv("GEMINI_KEY", "sk-gemini")
	p := &core.Provider{
		ID: "gemini-up", Name: "Gemini", BaseURL: upstream.URL,
		APIKeyEnv: "GEMINI_KEY", Model: "gemini-2.5-pro",
		Meta: core.ProviderMeta{APIFormat: "gemini"},
	}
	if err := st.UpsertProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActiveProvider(ctx, "claudecode", p.ID); err != nil {
		t.Fatal(err)
	}

	// A Claude Code (Anthropic) client is served by a Gemini upstream.
	resp, data := postJSON(t, proxy.BaseURL()+"/v1/messages", map[string]any{
		"model": "claude-sonnet-4-8", "max_tokens": 50,
		"system":   "be nice",
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, data)
	}
	if gotKey != "sk-gemini" {
		t.Fatalf("gemini key header = %q", gotKey)
	}
	if !strings.Contains(gotPath, "gemini-2.5-pro:generateContent") {
		t.Fatalf("upstream path = %q", gotPath)
	}
	if gotBody["systemInstruction"] == nil {
		t.Fatalf("system not forwarded: %#v", gotBody)
	}
	var anthropic map[string]any
	if err := json.Unmarshal(data, &anthropic); err != nil {
		t.Fatal(err)
	}
	if anthropic["type"] != "message" {
		t.Fatalf("anthropic response = %s", data)
	}
	usage := anthropic["usage"].(map[string]any)
	if usage["input_tokens"].(float64) != 11 || usage["output_tokens"].(float64) != 22 {
		t.Fatalf("usage = %#v", usage)
	}
	trace := latestTrace(t, st, "claudecode")
	if !trace.Success || trace.ClientProtocol != "anthropic" || trace.UpstreamProtocol != "gemini" {
		t.Fatalf("trace protocols = %+v", trace)
	}
	if trace.ClientModel != "claude-sonnet-4-8" || trace.UpstreamModel != "gemini-2.5-pro" || trace.ProviderID != "gemini-up" {
		t.Fatalf("trace models = %+v", trace)
	}
}

func TestProxyGeminiCLIEntryToOpenAIUpstream(t *testing.T) {
	ctx := context.Background()
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-3","model":"glm-4.6",
			"choices":[{"message":{"role":"assistant","content":"hi there"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":4,"completion_tokens":6}
		}`))
	}))
	defer upstream.Close()

	proxy, st := newTestProxy(t)
	t.Setenv("GLM_KEY2", "sk-glm2")
	p := &core.Provider{
		ID: "glm-for-gemini", Name: "GLM", BaseURL: upstream.URL + "/v1",
		APIKeyEnv: "GLM_KEY2", Model: "glm-4.6",
		Meta: core.ProviderMeta{APIFormat: "openai_chat"},
	}
	if err := st.UpsertProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActiveProvider(ctx, "gemini", p.ID); err != nil {
		t.Fatal(err)
	}

	// Gemini CLI client -> OpenAI chat upstream, translated both ways.
	resp, data := postJSON(t, proxy.BaseURL()+"/v1beta/models/gemini-2.5-pro:generateContent", map[string]any{
		"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hey"}}}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, data)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("upstream path = %q", gotPath)
	}
	var gemini map[string]any
	if err := json.Unmarshal(data, &gemini); err != nil {
		t.Fatal(err)
	}
	candidates, ok := gemini["candidates"].([]any)
	if !ok || len(candidates) == 0 {
		t.Fatalf("gemini response = %s", data)
	}
	parts := candidates[0].(map[string]any)["content"].(map[string]any)["parts"].([]any)
	if parts[0].(map[string]any)["text"] != "hi there" {
		t.Fatalf("gemini parts = %#v", parts)
	}
}
