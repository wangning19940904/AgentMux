package provider

import (
	_ "embed"
	"encoding/json"

	"github.com/wangning19940904/AgentMux/core"
)

//go:embed presets.json
var presetsRaw []byte

// presetEntry is the on-disk preset shape.
type presetEntry struct {
	ID                    string                    `json:"id"`
	Name                  string                    `json:"name"`
	Category              string                    `json:"category"`
	BaseURL               string                    `json:"base_url"`
	APIKeyEnv             string                    `json:"api_key_env"`
	Model                 string                    `json:"model"`
	APIFormat             string                    `json:"api_format"`
	ClaudeAuthScheme      string                    `json:"claude_auth_scheme"`
	ClaudeSonnetModel     string                    `json:"claude_sonnet_model"`
	ClaudeOpusModel       string                    `json:"claude_opus_model"`
	ClaudeHaikuModel      string                    `json:"claude_haiku_model"`
	ClaudeDesktopMode     string                    `json:"claude_desktop_mode"`
	ClaudeDesktopModels   []core.ClaudeDesktopModel `json:"claude_desktop_models"`
	ClaudeDesktopAuthMode string                    `json:"claude_desktop_auth_mode"`
}

// Presets returns the built-in provider presets as core.Provider templates.
func Presets() []*core.Provider {
	var entries []presetEntry
	if err := json.Unmarshal(presetsRaw, &entries); err != nil {
		return nil
	}
	out := make([]*core.Provider, 0, len(entries))
	for _, e := range entries {
		out = append(out, &core.Provider{
			ID:        e.ID,
			Name:      e.Name,
			Preset:    e.ID,
			Category:  e.Category,
			BaseURL:   e.BaseURL,
			APIKeyEnv: e.APIKeyEnv,
			Model:     e.Model,
			Meta: core.ProviderMeta{
				APIFormat:             e.APIFormat,
				ClaudeAuthScheme:      e.ClaudeAuthScheme,
				ClaudeSonnetModel:     e.ClaudeSonnetModel,
				ClaudeOpusModel:       e.ClaudeOpusModel,
				ClaudeHaikuModel:      e.ClaudeHaikuModel,
				ClaudeDesktopMode:     e.ClaudeDesktopMode,
				ClaudeDesktopModels:   e.ClaudeDesktopModels,
				ClaudeDesktopAuthMode: e.ClaudeDesktopAuthMode,
			},
		})
	}
	return out
}

// PresetByID returns a single preset template, or nil.
func PresetByID(id string) *core.Provider {
	for _, p := range Presets() {
		if p.ID == id {
			return p
		}
	}
	return nil
}
