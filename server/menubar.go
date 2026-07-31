package server

import (
	"encoding/json"
	"net/http"
	"strings"
)

// menubarSettingsKey is the settings-table key holding the menubar preferences
// JSON blob. The Swift menubar helper and the WebUI panel both read/write it
// through this API so there is a single source of truth.
const menubarSettingsKey = "menubar.preferences"

// MenubarSettings captures the user-selectable menubar display preferences.
// It is persisted verbatim as JSON in the settings table.
type MenubarSettings struct {
	// IconTheme selects the animated status icon ladder: "flame", "drop" or
	// "custom" (uses IconStages).
	IconTheme string `json:"icon_theme"`
	// IconStages is the 5-step emoji ladder used when IconTheme == "custom",
	// ordered idle -> spark -> small -> strong -> intense.
	IconStages []string `json:"icon_stages,omitempty"`
	// IconMetric drives the icon stage: "cost", "tokens" or "messages".
	IconMetric string `json:"icon_metric"`
	// CostThresholds are the ascending USD boundaries that map the cost metric
	// onto the 5-step icon ladder.
	CostThresholds []float64 `json:"cost_thresholds,omitempty"`

	ShowStatusIcon bool `json:"show_status_icon"`
	ShowMessages   bool `json:"show_messages"`
	ShowTokens     bool `json:"show_tokens"`
	ShowCost       bool `json:"show_cost"`
	ShowCNY        bool `json:"show_cny"`

	// Currency is the preferred cost display currency across the WebUI.
	// Canonical costs remain stored in USD.
	Currency string `json:"currency"`
	// CNYRate is the fixed USD->CNY exchange rate for the ¥ display.
	CNYRate float64 `json:"cny_rate"`

	// Breakdowns lists which grouped sections to show, in order. Valid values:
	// "model", "runtime", "date".
	Breakdowns []string `json:"breakdowns,omitempty"`
	// TopN limits each breakdown section to the top N rows.
	TopN int `json:"top_n"`
}

// defaultMenubarSettings returns the baseline preferences applied when nothing
// has been persisted yet, or to backfill missing/invalid fields.
func defaultMenubarSettings() MenubarSettings {
	return MenubarSettings{
		IconTheme:      "flame",
		IconMetric:     "cost",
		CostThresholds: []float64{0.01, 1, 10, 100},
		// Keep the macOS menu bar compact by default. The app logo is always
		// visible; users can opt into each additional status item separately.
		ShowStatusIcon: false,
		ShowMessages:   false,
		ShowTokens:     false,
		ShowCost:       false,
		ShowCNY:        false,
		Currency:       "cny",
		CNYRate:        7,
		Breakdowns:     []string{"model", "runtime", "date"},
		TopN:           3,
	}
}

// normalize backfills defaults for empty or out-of-range fields so the menubar
// never has to reason about missing values.
func (m *MenubarSettings) normalize() {
	d := defaultMenubarSettings()
	if m.IconTheme == "" {
		m.IconTheme = d.IconTheme
	}
	if m.IconMetric == "" {
		m.IconMetric = d.IconMetric
	}
	if len(m.CostThresholds) == 0 {
		m.CostThresholds = d.CostThresholds
	}
	m.Currency = strings.ToLower(strings.TrimSpace(m.Currency))
	if m.Currency != "cny" && m.Currency != "usd" {
		m.Currency = d.Currency
	}
	if m.CNYRate <= 0 {
		m.CNYRate = d.CNYRate
	}
	if len(m.Breakdowns) == 0 {
		m.Breakdowns = d.Breakdowns
	}
	if m.TopN <= 0 {
		m.TopN = d.TopN
	}
}

func (s *Server) handleMenubarSettingsGet(w http.ResponseWriter, r *http.Request) {
	settings := defaultMenubarSettings()
	if s.st != nil {
		if raw, ok, err := s.st.GetSetting(r.Context(), menubarSettingsKey); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		} else if ok && raw != "" {
			if err := json.Unmarshal([]byte(raw), &settings); err != nil {
				// Corrupt blob: fall back to defaults rather than 500ing the menubar.
				settings = defaultMenubarSettings()
			}
		}
	}
	settings.normalize()
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) handleMenubarSettingsPut(w http.ResponseWriter, r *http.Request) {
	var settings MenubarSettings
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	settings.normalize()
	if s.st == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no store wired"})
		return
	}
	blob, err := json.Marshal(settings)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := s.st.SetSetting(r.Context(), menubarSettingsKey, string(blob)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, settings)
}
