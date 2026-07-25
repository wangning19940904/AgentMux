// Package provider: live-config writers ported from cc-switch. Switching a
// provider rewrites the target tool's live config (e.g. ~/.claude/settings.json,
// ~/.codex/config.toml, Claude Desktop's Claude-3p profile) atomically, only
// touching keys AgentMux manages.
package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

// WriteLiveConfig writes a tool's live config file to point at provider p.
// It mirrors cc-switch's "write to live files on switch" behavior, using an
// atomic temp+rename write. Only the provider-relevant keys are touched.
func WriteLiveConfig(tool string, p *core.Provider) error {
	return WriteLiveConfigForSwitch(tool, p, nil)
}

// WriteLiveConfigForSwitch writes tool's live config for p, additionally
// cleaning keys the previous provider (prev, may be nil) left behind.
func WriteLiveConfigForSwitch(tool string, p, prev *core.Provider) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	switch liveConfigTool(tool) {
	case "claudecode":
		return writeClaudeConfig(home, p, prev)
	case "claude-desktop":
		return writeClaudeDesktopConfig(home, p)
	case "codex":
		return writeCodexConfig(home, p, prev)
	case "gemini":
		return writeGeminiConfig(home, p)
	default:
		return fmt.Errorf("unsupported tool %q for live config", tool)
	}
}

func liveConfigTool(tool string) string {
	switch strings.TrimSpace(tool) {
	case "claude", "claudecode", "claudecode-cli", "claude-code-cli":
		return "claudecode"
	case "claude-desktop", "claudecode-desktop", "claude-code-desktop":
		return "claude-desktop"
	case "codex", "codex-cli", "codex-app", "codex-desktop", "codex-app-server":
		return "codex"
	default:
		return strings.TrimSpace(tool)
	}
}

// managedClaudeEnvKeys are the env keys in ~/.claude/settings.json that
// AgentMux owns; every switch clears them before writing the new provider
// (cc-switch clears these via full settingsConfig replacement).
var managedClaudeEnvKeys = []string{
	"ANTHROPIC_BASE_URL",
	"ANTHROPIC_AUTH_TOKEN",
	"ANTHROPIC_API_KEY",
	"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY",
	"ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"ANTHROPIC_SMALL_FAST_MODEL",
}

const defaultAnthropicBaseURL = "https://api.anthropic.com"

// claudeAuthScheme resolves which credential env key Claude Code gets.
// cc-switch's third-party presets default to ANTHROPIC_AUTH_TOKEN; official
// direct API access uses ANTHROPIC_API_KEY (apiKeyField in cc-switch).
func claudeAuthScheme(p *core.Provider) string {
	switch p.Meta.ClaudeAuthScheme {
	case "auth_token", "api_key":
		return p.Meta.ClaudeAuthScheme
	}
	if p.Category == "official" {
		return "api_key"
	}
	return "auth_token"
}

// providerAPIKey resolves the real credential: the APIKeyEnv environment
// variable first, then the OS secret backend, then the transient write-only
// APIKey field.
func providerAPIKey(p *core.Provider) string {
	if p == nil {
		return ""
	}
	if p.APIKeyEnv != "" {
		if v, err := providerAPIKeyFromEnvOrSecret(p.APIKeyEnv); err == nil && v != "" {
			return v
		}
	}
	return strings.TrimSpace(p.APIKey)
}

func providerAPIKeyIssue(p *core.Provider) string {
	if p == nil || strings.TrimSpace(p.APIKey) != "" {
		return ""
	}
	apiKeyEnv := strings.TrimSpace(p.APIKeyEnv)
	if apiKeyEnv == "" {
		return ""
	}
	ok, err := EnsureProviderAPIKeyEnv(apiKeyEnv)
	if err != nil {
		return err.Error()
	}
	if !ok || os.Getenv(apiKeyEnv) == "" {
		return fmt.Sprintf("environment variable %s is empty or not set", apiKeyEnv)
	}
	return ""
}

// providerEnvSnippet returns the extra env passthrough map from
// settings_config.env (e.g. API_TIMEOUT_MS), mirroring cc-switch's
// settingsConfig.env shape.
func providerEnvSnippet(p *core.Provider) map[string]any {
	if p == nil || p.SettingsConfig == nil {
		return nil
	}
	env, _ := p.SettingsConfig["env"].(map[string]any)
	return env
}

func writeClaudeConfig(home string, p, prev *core.Provider) error {
	path := filepath.Join(claudeConfigDir(home, p), "settings.json")
	existing := readJSONObject(path)
	env, _ := existing["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	// Clear every key we manage plus extras the previous provider wrote, so
	// stale routing (e.g. a third-party ANTHROPIC_BASE_URL after switching
	// back to official) can never leak through.
	for _, k := range managedClaudeEnvKeys {
		delete(env, k)
	}
	for k := range providerEnvSnippet(prev) {
		delete(env, k)
	}

	// Official base URL is Claude Code's default; omitting it (cc-switch's
	// official preset has an empty env) lets OAuth login keep working.
	if p.BaseURL != "" && !strings.EqualFold(strings.TrimRight(p.BaseURL, "/"), defaultAnthropicBaseURL) {
		env["ANTHROPIC_BASE_URL"] = p.BaseURL
		// Claude Code can populate /model from a third-party gateway's
		// Anthropic-compatible GET /v1/models endpoint. Direct mode talks to
		// the provider itself, so discovery reflects the provider's live list.
		env["CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY"] = "1"
	}
	if key := providerAPIKey(p); key != "" {
		if claudeAuthScheme(p) == "api_key" {
			env["ANTHROPIC_API_KEY"] = key
		} else {
			env["ANTHROPIC_AUTH_TOKEN"] = key
		}
	}
	if p.Model != "" {
		env["ANTHROPIC_MODEL"] = p.Model
	}
	if v := p.Meta.ClaudeSonnetModel; v != "" {
		env["ANTHROPIC_DEFAULT_SONNET_MODEL"] = v
	}
	if v := p.Meta.ClaudeOpusModel; v != "" {
		env["ANTHROPIC_DEFAULT_OPUS_MODEL"] = v
	}
	if v := p.Meta.ClaudeHaikuModel; v != "" {
		env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = v
		env["ANTHROPIC_SMALL_FAST_MODEL"] = v
	}
	for k, v := range providerEnvSnippet(p) {
		env[k] = v
	}

	if len(env) == 0 {
		delete(existing, "env")
	} else {
		existing["env"] = env
	}
	return writeJSONObject(path, existing)
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

func claudeConfigDir(home string, p *core.Provider) string {
	if v := configString(p, "claude_config_dir"); v != "" {
		return v
	}
	if v := os.Getenv("CLAUDE_CONFIG_DIR"); v != "" {
		return v
	}
	return filepath.Join(home, ".claude")
}

func codexConfigDir(home string, p *core.Provider) string {
	if v := configString(p, "codex_home"); v != "" {
		return v
	}
	if v := os.Getenv("CODEX_HOME"); v != "" {
		return v
	}
	return filepath.Join(home, ".codex")
}

func configString(p *core.Provider, key string) string {
	if p == nil {
		return ""
	}
	if p.Extra != nil && p.Extra[key] != "" {
		return p.Extra[key]
	}
	if p.SettingsConfig != nil {
		if v, ok := p.SettingsConfig[key].(string); ok {
			return v
		}
	}
	return ""
}

func stringValue(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func lenFromJSONArray(value any) int {
	if list, ok := value.([]any); ok {
		return len(list)
	}
	return 0
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func readTOMLObject(path string) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if _, err := toml.Decode(string(data), &m); err != nil || m == nil {
		return map[string]any{}
	}
	return m
}

func writeTOMLObject(path string, obj map[string]any) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(obj); err != nil {
		return err
	}
	return store.AtomicWrite(path, buf.Bytes(), 0o600)
}

func ensureMap(parent map[string]any, key string) map[string]any {
	if existing, ok := parent[key].(map[string]any); ok {
		return existing
	}
	if existing, ok := parent[key].(map[string]interface{}); ok {
		return existing
	}
	next := map[string]any{}
	parent[key] = next
	return next
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
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
