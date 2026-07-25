package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
	providerpkg "github.com/wangning19940904/AgentMux/provider"
	"github.com/wangning19940904/AgentMux/store"
)

func TestProviderMonitorRefreshesCatalogAndTracksModelHealth(t *testing.T) {
	var modelBFailing atomic.Bool
	modelBFailing.Store(true)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "monitor-secret" {
			http.Error(w, "missing key", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			writeJSON(w, http.StatusOK, map[string]any{
				"data": []map[string]string{{"id": "model-a"}, {"id": "model-b"}},
			})
		case "/v1/messages":
			var payload struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if payload.Model == "model-b" && modelBFailing.Load() {
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{
					"error": map[string]string{"message": "model is cooling down"},
				})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"content": []map[string]string{{"type": "text", "text": "ok"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	t.Setenv("AGENTMUX_TEST_MONITOR_KEY", "monitor-secret")
	st, err := store.Open(filepath.Join(t.TempDir(), "provider-monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	manager := providerpkg.NewManager(st)
	configured := &core.Provider{
		ID:        "relay",
		Name:      "Relay",
		BaseURL:   upstream.URL,
		APIKeyEnv: "AGENTMUX_TEST_MONITOR_KEY",
		Model:     "model-a",
		Meta: core.ProviderMeta{
			APIFormat:       "anthropic",
			SupportedModels: []string{"model-a"},
		},
	}
	if err := manager.Upsert(context.Background(), configured); err != nil {
		t.Fatal(err)
	}

	monitor := newProviderMonitor(slog.New(slog.NewTextHandler(os.Stderr, nil)), st, manager)
	first, err := monitor.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if len(first.Providers) != 1 {
		t.Fatalf("provider statuses = %+v", first.Providers)
	}
	status := first.Providers[0]
	if status.State != "warning" || status.CatalogCount != 2 || status.HealthyModels != 1 || status.UnhealthyModels != 1 {
		t.Fatalf("first status = %+v", status)
	}
	if len(status.AddedModels) != 1 || status.AddedModels[0] != "model-b" {
		t.Fatalf("added models = %v", status.AddedModels)
	}
	if !hasProviderMonitorAlert(first.Alerts, "new_models", "") ||
		!hasProviderMonitorAlert(first.Alerts, "model_error", "model-b") {
		t.Fatalf("first alerts = %+v", first.Alerts)
	}
	saved, err := manager.Get(context.Background(), "relay")
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Meta.SupportedModels) != 2 || saved.Meta.SupportedModels[1] != "model-b" {
		t.Fatalf("saved catalog = %v", saved.Meta.SupportedModels)
	}

	modelBFailing.Store(false)
	second, err := monitor.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.Providers[0].State != "healthy" || second.Providers[0].HealthyModels != 2 {
		t.Fatalf("second status = %+v", second.Providers[0])
	}
	if hasProviderMonitorAlert(second.Alerts, "model_error", "model-b") {
		t.Fatalf("recovered model alert remained active: %+v", second.Alerts)
	}

	for _, alert := range second.Alerts {
		if alert.Type != "new_models" {
			continue
		}
		dismissed, dismissErr := monitor.DismissAlert(context.Background(), alert.ID)
		if dismissErr != nil {
			t.Fatal(dismissErr)
		}
		if len(dismissed.Alerts) != 0 {
			t.Fatalf("alerts after dismissal = %+v", dismissed.Alerts)
		}
	}
}

func TestProviderMonitorConfigPersists(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "provider-monitor-config.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	monitor := newProviderMonitor(nil, st, nil)
	want := ProviderMonitorConfig{
		Enabled:              true,
		IntervalMinutes:      90,
		ProbeModels:          false,
		MaxModelsPerProvider: 7,
	}
	if _, err := monitor.UpdateConfig(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	reloaded := newProviderMonitor(nil, st, nil)
	if got := reloaded.Snapshot().Config; got != want {
		t.Fatalf("reloaded config = %+v, want %+v", got, want)
	}
	if _, err := monitor.UpdateConfig(context.Background(), ProviderMonitorConfig{
		IntervalMinutes:      1,
		MaxModelsPerProvider: 7,
	}); err == nil {
		t.Fatal("expected short interval validation error")
	}
}

func TestProviderModelProbeEndpoint(t *testing.T) {
	tests := []struct {
		name       string
		baseURL    string
		apiFormat  string
		model      string
		wantURL    string
		wantFormat string
	}{
		{
			name: "anthropic", baseURL: "https://api.example.com", apiFormat: "anthropic", model: "claude",
			wantURL: "https://api.example.com/v1/messages", wantFormat: "anthropic",
		},
		{
			name: "openai responses", baseURL: "https://api.example.com/v1", apiFormat: "openai_responses", model: "gpt",
			wantURL: "https://api.example.com/v1/responses", wantFormat: "openai_responses",
		},
		{
			name: "gemini", baseURL: "https://generativelanguage.googleapis.com", apiFormat: "gemini_native", model: "gemini-2.5-pro",
			wantURL:    "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-pro:generateContent",
			wantFormat: "gemini_native",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotURL, gotFormat, err := providerModelProbeEndpoint(test.baseURL, test.apiFormat, test.model)
			if err != nil {
				t.Fatal(err)
			}
			if gotURL != test.wantURL || gotFormat != test.wantFormat {
				t.Fatalf("endpoint = %q, %q; want %q, %q", gotURL, gotFormat, test.wantURL, test.wantFormat)
			}
		})
	}
}

func hasProviderMonitorAlert(alerts []ProviderMonitorAlert, kind, model string) bool {
	for _, alert := range alerts {
		if alert.Type == kind && alert.Model == model {
			return true
		}
	}
	return false
}
