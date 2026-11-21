package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func getUsageDisplaySettings(t *testing.T, s *Server) UsageDisplaySettings {
	t.Helper()
	rec := doJSON(t, s, http.MethodGet, "/api/v1/usage/display-settings", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get settings: code = %d body = %s", rec.Code, rec.Body.String())
	}
	var got UsageDisplaySettings
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestUsageDisplaySettingsDefaults(t *testing.T) {
	s, _ := newTestServer(t)
	if got := getUsageDisplaySettings(t, s); got != defaultUsageDisplaySettings() {
		t.Fatalf("defaults = %+v, want cny 7", got)
	}
}

func TestUsageDisplaySettingsRoundTrip(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodPut, "/api/v1/usage/display-settings", UsageDisplaySettings{Currency: "usd", CNYRate: 6.88})
	if rec.Code != http.StatusOK {
		t.Fatalf("put settings: code = %d body = %s", rec.Code, rec.Body.String())
	}
	if got := getUsageDisplaySettings(t, s); got.Currency != "usd" || got.CNYRate != 6.88 {
		t.Fatalf("settings did not round-trip: %+v", got)
	}
}

func TestUsageDisplaySettingsNormalizesInvalidCurrencyAndRate(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodPut, "/api/v1/usage/display-settings", UsageDisplaySettings{Currency: "eur", CNYRate: 0})
	if rec.Code != http.StatusOK {
		t.Fatalf("put settings: code = %d body = %s", rec.Code, rec.Body.String())
	}
	var got UsageDisplaySettings
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got != defaultUsageDisplaySettings() {
		t.Fatalf("normalized settings = %+v", got)
	}
}

func TestUsageDisplaySettingsCarryOverFromMenubarPreferences(t *testing.T) {
	s, st := newTestServer(t)
	legacy := `{"icon_theme":"flame","show_cost":true,"currency":"usd","cny_rate":6.7249,"top_n":3}`
	if err := st.SetSetting(context.Background(), legacyMenubarSettingsKey, legacy); err != nil {
		t.Fatal(err)
	}
	if got := getUsageDisplaySettings(t, s); got.Currency != "usd" || got.CNYRate != 6.7249 {
		t.Fatalf("legacy settings not carried over: %+v", got)
	}

	// Once saved under the new key, the legacy blob is no longer consulted.
	doJSON(t, s, http.MethodPut, "/api/v1/usage/display-settings", UsageDisplaySettings{Currency: "cny", CNYRate: 7.1})
	if got := getUsageDisplaySettings(t, s); got.Currency != "cny" || got.CNYRate != 7.1 {
		t.Fatalf("saved settings = %+v", got)
	}
}
