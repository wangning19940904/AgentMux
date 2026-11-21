package server

import (
	"encoding/json"
	"net/http"
	"strings"
)

// usageDisplaySettingsKey is the settings-table key holding the currency the
// WebUI shows costs in and the fixed USD->CNY rate it converts with.
const usageDisplaySettingsKey = "usage.display"

// legacyMenubarSettingsKey held these two fields, among menu bar display
// choices that no longer exist. It is read only when nothing has been saved
// under usageDisplaySettingsKey, so a rate chosen there carries over.
const legacyMenubarSettingsKey = "menubar.preferences"

// UsageDisplaySettings is how estimated costs are shown. Canonical costs stay
// stored in USD.
type UsageDisplaySettings struct {
	// Currency is "cny" or "usd".
	Currency string `json:"currency"`
	// CNYRate is the fixed USD->CNY exchange rate.
	CNYRate float64 `json:"cny_rate"`
}

func defaultUsageDisplaySettings() UsageDisplaySettings {
	return UsageDisplaySettings{Currency: "cny", CNYRate: 7}
}

// normalize replaces an unknown currency or a non-positive rate with the
// default.
func (u *UsageDisplaySettings) normalize() {
	d := defaultUsageDisplaySettings()
	u.Currency = strings.ToLower(strings.TrimSpace(u.Currency))
	if u.Currency != "cny" && u.Currency != "usd" {
		u.Currency = d.Currency
	}
	if u.CNYRate <= 0 {
		u.CNYRate = d.CNYRate
	}
}

func (s *Server) handleUsageDisplaySettingsGet(w http.ResponseWriter, r *http.Request) {
	settings := defaultUsageDisplaySettings()
	if s.st != nil {
		for _, key := range []string{usageDisplaySettingsKey, legacyMenubarSettingsKey} {
			raw, ok, err := s.st.GetSetting(r.Context(), key)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			if !ok || raw == "" {
				continue
			}
			if err := json.Unmarshal([]byte(raw), &settings); err != nil {
				// A corrupt blob falls back to the defaults.
				settings = defaultUsageDisplaySettings()
			}
			break
		}
	}
	settings.normalize()
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) handleUsageDisplaySettingsPut(w http.ResponseWriter, r *http.Request) {
	var settings UsageDisplaySettings
	if !decodeJSONInto(w, r, &settings) {
		return
	}
	settings.normalize()
	if s.st == nil {
		writeErr(w, http.StatusServiceUnavailable, "no store wired")
		return
	}
	blob, err := json.Marshal(settings)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.st.SetSetting(r.Context(), usageDisplaySettingsKey, string(blob)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, settings)
}
