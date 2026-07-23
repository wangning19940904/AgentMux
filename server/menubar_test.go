package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestMenubarDefaultsToLogoOnly(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodGet, "/api/v1/menubar/settings", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get settings: code = %d body = %s", rec.Code, rec.Body.String())
	}

	var settings MenubarSettings
	if err := json.Unmarshal(rec.Body.Bytes(), &settings); err != nil {
		t.Fatal(err)
	}
	if settings.ShowStatusIcon || settings.ShowMessages || settings.ShowTokens || settings.ShowCost {
		t.Fatalf("menu bar should default to logo only: %+v", settings)
	}
}

func TestMenubarDisplayChoicesRoundTrip(t *testing.T) {
	s, _ := newTestServer(t)
	want := defaultMenubarSettings()
	want.ShowStatusIcon = true
	want.ShowTokens = true

	rec := doJSON(t, s, http.MethodPut, "/api/v1/menubar/settings", want)
	if rec.Code != http.StatusOK {
		t.Fatalf("put settings: code = %d body = %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/api/v1/menubar/settings", nil)

	var got MenubarSettings
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.ShowStatusIcon || !got.ShowTokens || got.ShowMessages || got.ShowCost {
		t.Fatalf("display choices did not round-trip: %+v", got)
	}
}
