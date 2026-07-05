package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/agentnexus/agentnexus/core"
)

func TestProbeProviderModelsOpenAICompatible(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			if r.Header.Get("Authorization") != "Bearer secret" {
				http.Error(w, "missing bearer", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"z-model"},{"id":"a-model"}]}`))
		case "/v1/chat/completions":
			if r.Header.Get("Authorization") != "Bearer secret" {
				http.Error(w, "missing bearer", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"id":"chat-ok"}`))
		case "/v1/responses":
			http.Error(w, "not found", http.StatusNotFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	t.Setenv("TEST_PROVIDER_KEY", "secret")
	got, err := probeProviderModels(context.Background(), &core.Provider{
		BaseURL:   srv.URL + "/v1",
		APIKeyEnv: "TEST_PROVIDER_KEY",
		Meta:      core.ProviderMeta{APIFormat: "openai_chat"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Count != 2 || got.Models[0] != "a-model" || got.Models[1] != "z-model" {
		t.Fatalf("probe = %+v", got)
	}
	if got.APIFormat != "openai_chat" || got.CodexWireAPI != "chat" {
		t.Fatalf("detected formats = %+v", got)
	}
	if len(got.Formats) != 3 || len(got.Protocols) != 2 {
		t.Fatalf("checks = %+v protocols=%+v", got.Formats, got.Protocols)
	}
}

func TestProbeProviderModelsAnthropic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" && r.Header.Get("x-api-key") == "secret" && r.Header.Get("anthropic-version") != "" {
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-sonnet"}]}`))
			return
		}
		http.Error(w, "unsupported", http.StatusUnauthorized)
	}))
	defer srv.Close()

	t.Setenv("ANTHROPIC_TEST_KEY", "secret")
	got, err := probeProviderModels(context.Background(), &core.Provider{
		BaseURL:   srv.URL,
		APIKeyEnv: "ANTHROPIC_TEST_KEY",
		Meta:      core.ProviderMeta{APIFormat: "anthropic"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Count != 1 || got.Models[0] != "claude-sonnet" {
		t.Fatalf("probe = %+v", got)
	}
	if got.APIFormat != "anthropic" {
		t.Fatalf("api format = %q", got.APIFormat)
	}
}

func TestHandleProviderProbeAcceptsInlineAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" && r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"model-a"}]}`))
	}))
	defer srv.Close()

	reqBody := `{"id":"super-relay","base_url":` + strconvQuote(srv.URL) + `,"api_key":"secret","meta":{"api_format":"openai_chat"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/providers/probe", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()

	(&Server{}).handleProviderProbe(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var got providerProbeResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Count != 1 || got.Models[0] != "model-a" {
		t.Fatalf("probe = %+v", got)
	}
}

func TestProbeProviderModelsDetectsResponsesProtocol(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			if r.Header.Get("Authorization") != "Bearer secret" {
				http.Error(w, "missing bearer", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"resp-model"}]}`))
		case "/v1/chat/completions":
			http.Error(w, "not supported", http.StatusNotFound)
		case "/v1/responses":
			if r.Header.Get("Authorization") != "Bearer secret" {
				http.Error(w, "missing bearer", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"id":"resp-ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	t.Setenv("RESPONSES_TEST_KEY", "secret")
	got, err := probeProviderModels(context.Background(), &core.Provider{
		BaseURL:   srv.URL + "/v1",
		APIKeyEnv: "RESPONSES_TEST_KEY",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.CodexWireAPI != "responses" {
		t.Fatalf("CodexWireAPI = %q, result = %+v", got.CodexWireAPI, got)
	}
	var responsesOK bool
	for _, check := range got.Protocols {
		if check.Name == "responses" {
			responsesOK = check.OK
		}
	}
	if !responsesOK {
		t.Fatalf("responses protocol not detected: %+v", got.Protocols)
	}
}

func TestNormalizeProviderAPIKeyInjectsGeneratedEnv(t *testing.T) {
	p := &core.Provider{ID: "super-relay", APIKey: "secret"}
	if err := normalizeProviderAPIKey(p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Unsetenv(p.APIKeyEnv) })

	if p.APIKey != "" {
		t.Fatalf("APIKey should be cleared, got %q", p.APIKey)
	}
	if p.APIKeyEnv != "AGENTNEXUS_PROVIDER_SUPER_RELAY_API_KEY" {
		t.Fatalf("APIKeyEnv = %q", p.APIKeyEnv)
	}
	if os.Getenv(p.APIKeyEnv) != "secret" {
		t.Fatalf("injected env = %q", os.Getenv(p.APIKeyEnv))
	}
}

func TestNormalizeProviderAPIKeyTreatsLegacyInlineEnvValueAsKey(t *testing.T) {
	p := &core.Provider{ID: "super-relay", APIKeyEnv: "plat_secret-with-dash"}
	if err := normalizeProviderAPIKey(p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Unsetenv(p.APIKeyEnv) })

	if p.APIKeyEnv != "AGENTNEXUS_PROVIDER_SUPER_RELAY_API_KEY" {
		t.Fatalf("APIKeyEnv = %q", p.APIKeyEnv)
	}
	if os.Getenv(p.APIKeyEnv) != "plat_secret-with-dash" {
		t.Fatalf("injected env = %q", os.Getenv(p.APIKeyEnv))
	}
}

func TestProbeProviderModelsRequiresEnv(t *testing.T) {
	_, err := probeProviderModels(context.Background(), &core.Provider{
		BaseURL:   "https://example.com/v1",
		APIKeyEnv: "MISSING_PROVIDER_KEY",
	})
	if err == nil {
		t.Fatal("expected missing env error")
	}
}

func strconvQuote(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
