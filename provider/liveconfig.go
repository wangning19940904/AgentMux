package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/agentnexus/agentnexus/core"
	"github.com/agentnexus/agentnexus/store"
)

// WriteLiveConfig writes a tool's live config file to point at provider p.
// It mirrors cc-switch's "write to live files on switch" behavior, using an
// atomic temp+rename write. Only the provider-relevant keys are touched.
func WriteLiveConfig(tool string, p *core.Provider) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	switch tool {
	case "claudecode":
		return writeClaudeConfig(home, p)
	case "codex":
		return writeCodexConfig(home, p)
	case "gemini":
		return writeGeminiConfig(home, p)
	default:
		return fmt.Errorf("unsupported tool %q for live config", tool)
	}
}

func writeClaudeConfig(home string, p *core.Provider) error {
	path := filepath.Join(home, ".claude", "settings.json")
	existing := readJSONObject(path)
	env, _ := existing["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	if p.BaseURL != "" {
		env["ANTHROPIC_BASE_URL"] = p.BaseURL
	}
	if p.APIKeyEnv != "" {
		if v := os.Getenv(p.APIKeyEnv); v != "" {
			env["ANTHROPIC_API_KEY"] = v
		}
	}
	if p.Model != "" {
		env["ANTHROPIC_MODEL"] = p.Model
	}
	existing["env"] = env
	return writeJSONObject(path, existing)
}

func writeCodexConfig(home string, p *core.Provider) error {
	path := filepath.Join(home, ".codex", "provider.json")
	obj := map[string]any{"base_url": p.BaseURL, "model": p.Model}
	if p.APIKeyEnv != "" {
		obj["api_key_env"] = p.APIKeyEnv
	}
	return writeJSONObject(path, obj)
}

func writeGeminiConfig(home string, p *core.Provider) error {
	path := filepath.Join(home, ".gemini", "settings.json")
	existing := readJSONObject(path)
	if p.BaseURL != "" {
		existing["base_url"] = p.BaseURL
	}
	if p.Model != "" {
		existing["model"] = p.Model
	}
	return writeJSONObject(path, existing)
}

func readJSONObject(path string) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil || m == nil {
		return map[string]any{}
	}
	return m
}

func writeJSONObject(path string, obj map[string]any) error {
	data, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return err
	}
	return store.AtomicWrite(path, data, 0o600)
}
