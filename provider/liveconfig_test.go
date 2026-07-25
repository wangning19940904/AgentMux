package provider

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return m
}

func envOf(t *testing.T, settings map[string]any) map[string]any {
	t.Helper()
	env, _ := settings["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	return env
}

func TestWriteClaudeConfigThirdPartyUsesAuthToken(t *testing.T) {
	home := t.TempDir()
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-existing user settings must survive; managed keys must be replaced.
	seed := map[string]any{
		"permissions": map[string]any{"allow": []any{"Bash"}},
		"env": map[string]any{
			"ANTHROPIC_BASE_URL": "https://old.example.com",
			"ANTHROPIC_API_KEY":  "old-key",
			"USER_CUSTOM":        "keep-me",
		},
	}
	raw, _ := json.Marshal(seed)
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEEPSEEK_API_KEY", "sk-third-party")
	p := &core.Provider{
		ID:        "deepseek",
		Name:      "DeepSeek",
		Category:  "third_party",
		BaseURL:   "https://api.deepseek.com/anthropic",
		APIKeyEnv: "DEEPSEEK_API_KEY",
		Model:     "deepseek-chat",
		Meta: core.ProviderMeta{
			ClaudeSonnetModel: "deepseek-chat",
			ClaudeHaikuModel:  "deepseek-lite",
		},
		SettingsConfig: map[string]any{
			"env": map[string]any{"API_TIMEOUT_MS": "3000000"},
		},
	}
	if err := writeClaudeConfig(home, p, nil); err != nil {
		t.Fatal(err)
	}
	settings := readJSON(t, filepath.Join(claudeDir, "settings.json"))
	env := envOf(t, settings)
	if env["ANTHROPIC_BASE_URL"] != p.BaseURL {
		t.Fatalf("base url = %v", env["ANTHROPIC_BASE_URL"])
	}
	if env["CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY"] != "1" {
		t.Fatalf("gateway model discovery = %v", env["CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY"])
	}
	if env["ANTHROPIC_AUTH_TOKEN"] != "sk-third-party" {
		t.Fatalf("auth token = %v", env["ANTHROPIC_AUTH_TOKEN"])
	}
	if _, ok := env["ANTHROPIC_API_KEY"]; ok {
		t.Fatalf("stale ANTHROPIC_API_KEY kept: %v", env)
	}
	if env["ANTHROPIC_MODEL"] != "deepseek-chat" || env["ANTHROPIC_DEFAULT_SONNET_MODEL"] != "deepseek-chat" {
		t.Fatalf("models = %v", env)
	}
	if env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] != "deepseek-lite" || env["ANTHROPIC_SMALL_FAST_MODEL"] != "deepseek-lite" {
		t.Fatalf("haiku models = %v", env)
	}
	if env["API_TIMEOUT_MS"] != "3000000" || env["USER_CUSTOM"] != "keep-me" {
		t.Fatalf("env passthrough = %v", env)
	}
	if _, ok := settings["permissions"]; !ok {
		t.Fatalf("user settings lost: %v", settings)
	}
}

func TestWriteClaudeConfigOfficialClearsManagedKeys(t *testing.T) {
	home := t.TempDir()
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("THIRD_KEY", "sk-third")
	third := &core.Provider{
		ID: "relay", Category: "third_party",
		BaseURL: "https://relay.example.com", APIKeyEnv: "THIRD_KEY",
		Model: "some-model",
		SettingsConfig: map[string]any{
			"env": map[string]any{"API_TIMEOUT_MS": "600000"},
		},
	}
	if err := writeClaudeConfig(home, third, nil); err != nil {
		t.Fatal(err)
	}
	// Switch back to official: base url is Claude Code's default so no env
	// pollution may remain, letting OAuth login work again. The previous
	// provider's extra env keys must be cleaned too.
	official := &core.Provider{
		ID: "anthropic-official", Category: "official",
		BaseURL: "https://api.anthropic.com",
	}
	if err := writeClaudeConfig(home, official, third); err != nil {
		t.Fatal(err)
	}
	settings := readJSON(t, filepath.Join(claudeDir, "settings.json"))
	env := envOf(t, settings)
	for _, k := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_MODEL", "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY", "API_TIMEOUT_MS"} {
		if _, ok := env[k]; ok {
			t.Fatalf("stale key %s survived official switch: %v", k, env)
		}
	}
}

func TestWriteClaudeConfigAPIKeyScheme(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AIHUBMIX_KEY", "sk-hub")
	p := &core.Provider{
		ID: "aihubmix", Category: "third_party",
		BaseURL: "https://aihubmix.com/claude", APIKeyEnv: "AIHUBMIX_KEY",
		Meta: core.ProviderMeta{ClaudeAuthScheme: "api_key"},
	}
	if err := writeClaudeConfig(home, p, nil); err != nil {
		t.Fatal(err)
	}
	env := envOf(t, readJSON(t, filepath.Join(home, ".claude", "settings.json")))
	if env["ANTHROPIC_API_KEY"] != "sk-hub" {
		t.Fatalf("api key = %v", env)
	}
	if _, ok := env["ANTHROPIC_AUTH_TOKEN"]; ok {
		t.Fatalf("auth token should not be set: %v", env)
	}
}

func TestWriteCodexConfigUsesBearerTokenAndPreservesAuth(t *testing.T) {
	home := t.TempDir()
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte("approval_policy = \"never\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(codexDir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"tokens":"chatgpt-oauth"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENROUTER_API_KEY", "sk-or")
	p := &core.Provider{
		ID:        "openrouter",
		Name:      "OpenRouter",
		Category:  "third_party",
		BaseURL:   "https://openrouter.ai/api/v1",
		APIKeyEnv: "OPENROUTER_API_KEY",
		Model:     "anthropic/claude-sonnet-4.5",
		Meta:      core.ProviderMeta{CodexWireAPI: "chat"},
	}
	if err := writeCodexConfig(home, p, nil); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if _, err := toml.DecodeFile(filepath.Join(codexDir, "config.toml"), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["approval_policy"] != "never" {
		t.Fatalf("approval_policy = %v", doc["approval_policy"])
	}
	if doc["model_provider"] != codexModelProviderID || doc["model"] != p.Model {
		t.Fatalf("codex route = %#v", doc)
	}
	block := doc["model_providers"].(map[string]any)[codexModelProviderID].(map[string]any)
	if block["wire_api"] != "chat" || block["base_url"] != p.BaseURL {
		t.Fatalf("provider block = %#v", block)
	}
	if block["experimental_bearer_token"] != "sk-or" {
		t.Fatalf("bearer token = %#v", block)
	}
	if _, ok := block["env_key"]; ok {
		t.Fatalf("env_key must not be written: %#v", block)
	}
	auth, _ := os.ReadFile(authPath)
	if string(auth) != `{"tokens":"chatgpt-oauth"}` {
		t.Fatalf("auth.json was modified: %s", auth)
	}
	// Model catalog generated for the desktop app.
	catalog := readJSON(t, filepath.Join(codexDir, codexModelCatalogFilename))
	models := catalog["models"].([]any)
	if len(models) != 1 {
		t.Fatalf("catalog models = %#v", models)
	}
	entry := models[0].(map[string]any)
	if entry["slug"] != p.Model || entry["shell_type"] != "shell_command" {
		t.Fatalf("catalog entry = %#v", entry)
	}
	if doc["model_catalog_json"] != codexModelCatalogFilename {
		t.Fatalf("model_catalog_json = %v", doc["model_catalog_json"])
	}
}

func TestWriteCodexConfigOfficialRestoresBuiltin(t *testing.T) {
	home := t.TempDir()
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RELAY_KEY", "sk-relay")
	third := &core.Provider{
		ID: "relay", Name: "Relay", Category: "third_party",
		BaseURL: "https://relay.example/v1", APIKeyEnv: "RELAY_KEY",
		Model: "some-model",
		Meta:  core.ProviderMeta{SupportedModels: []string{"some-model"}},
	}
	if err := writeCodexConfig(home, third, nil); err != nil {
		t.Fatal(err)
	}
	official := &core.Provider{
		ID: "openai-official", Name: "OpenAI", Category: "official",
		BaseURL: "https://api.openai.com/v1", Model: "gpt-5",
	}
	if err := writeCodexConfig(home, official, third); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if _, err := toml.DecodeFile(filepath.Join(codexDir, "config.toml"), &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["model_provider"]; ok {
		t.Fatalf("model_provider should be removed for official: %#v", doc)
	}
	if _, ok := doc["model_providers"]; ok {
		t.Fatalf("agentmux block should be removed for official: %#v", doc)
	}
	if doc["model"] != "gpt-5" {
		t.Fatalf("model = %v", doc["model"])
	}
	if _, ok := doc["model_catalog_json"]; ok {
		t.Fatalf("catalog pointer should be removed: %#v", doc)
	}
}

func TestWriteCodexConfigCleansLegacyBlock(t *testing.T) {
	home := t.TempDir()
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `model_provider = "openrouter"

[model_providers.openrouter]
name = "OpenRouter"
base_url = "https://openrouter.ai/api/v1"
env_key = "OPENROUTER_API_KEY"
`
	if err := os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := &core.Provider{ID: "openrouter", Name: "OpenRouter"}
	next := &core.Provider{
		ID: "deepseek", Name: "DeepSeek", Category: "third_party",
		BaseURL: "https://api.deepseek.com", Meta: core.ProviderMeta{CodexWireAPI: "chat"},
	}
	if err := writeCodexConfig(home, next, prev); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if _, err := toml.DecodeFile(filepath.Join(codexDir, "config.toml"), &doc); err != nil {
		t.Fatal(err)
	}
	providers := doc["model_providers"].(map[string]any)
	if _, ok := providers["openrouter"]; ok {
		t.Fatalf("legacy block survived: %#v", providers)
	}
	if _, ok := providers[codexModelProviderID]; !ok {
		t.Fatalf("agentmux block missing: %#v", providers)
	}
}

func TestSwitchCodexAppRouteUsesCodexProviderConfig(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	codexDir := filepath.Join(home, "codex-home")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte("approval_policy = \"never\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(home, "providers.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	mgr := NewManager(st)
	t.Setenv("ZHIPU_API_KEY", "sk-zhipu")
	p := &core.Provider{
		ID:        "zhipu-glm",
		Name:      "Zhipu GLM",
		Category:  "third_party",
		BaseURL:   "https://open.bigmodel.cn/api/paas/v4",
		APIKeyEnv: "ZHIPU_API_KEY",
		Model:     "glm-4.6",
		SettingsConfig: map[string]any{
			"codex_home": codexDir,
		},
		Meta: core.ProviderMeta{CodexWireAPI: "chat"},
	}
	if err := mgr.Upsert(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Switch(ctx, p.ID, "codex-app"); err != nil {
		t.Fatal(err)
	}
	id, ok, err := st.ActiveProviderID(ctx, "codex-app")
	if err != nil || !ok || id != p.ID {
		t.Fatalf("codex-app route = %q,%v,%v", id, ok, err)
	}
	var doc map[string]any
	if _, err := toml.DecodeFile(filepath.Join(codexDir, "config.toml"), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["model_provider"] != codexModelProviderID || doc["model"] != p.Model || doc["approval_policy"] != "never" {
		t.Fatalf("codex app config = %#v", doc)
	}
}

func desktopTestProvider(profileRoot string) *core.Provider {
	return &core.Provider{
		ID:        "claude-desktop-official",
		Name:      "Claude Desktop Direct",
		BaseURL:   "https://api.anthropic.com",
		APIKeyEnv: "ANTHROPIC_API_KEY",
		Model:     "claude-sonnet-4-8",
		SettingsConfig: map[string]any{
			"claude_desktop_dir": profileRoot,
		},
		Meta: core.ProviderMeta{ClaudeDesktopMode: "direct"},
	}
}

func TestClaudeDesktopDirectProfileWritesDeploymentMode(t *testing.T) {
	home := t.TempDir()
	profileRoot := filepath.Join(home, "Claude-3p")
	normalConfig := filepath.Join(home, "Claude", claudeDesktopConfigFile)
	t.Setenv("ANTHROPIC_API_KEY", "secret")
	p := desktopTestProvider(profileRoot)
	if err := writeClaudeDesktopConfig(home, p); err != nil {
		t.Fatal(err)
	}
	profile := readJSON(t, filepath.Join(profileRoot, "configLibrary", claudeDesktopProfileID+".json"))
	if profile["inferenceGatewayApiKey"] != "secret" || profile["inferenceGatewayAuthScheme"] != "bearer" {
		t.Fatalf("profile = %#v", profile)
	}
	if profile["inferenceProvider"] != "gateway" {
		t.Fatalf("profile provider = %#v", profile)
	}
	meta := readJSON(t, filepath.Join(profileRoot, "configLibrary", "_meta.json"))
	if meta["appliedId"] != claudeDesktopProfileID {
		t.Fatalf("meta = %#v", meta)
	}
	// cc-switch alignment: deploymentMode "3p" written to BOTH config files.
	if mode := readJSON(t, normalConfig)["deploymentMode"]; mode != "3p" {
		t.Fatalf("normal deploymentMode = %v", mode)
	}
	if mode := readJSON(t, filepath.Join(profileRoot, claudeDesktopConfigFile))["deploymentMode"]; mode != "3p" {
		t.Fatalf("3p deploymentMode = %v", mode)
	}
	status, err := ClaudeDesktopStatus(p)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Enabled || !status.Configured || status.BaseURL != p.BaseURL || status.ModelCount != 1 {
		t.Fatalf("status = %+v", status)
	}
}

func TestClaudeDesktopOfficialRestores1P(t *testing.T) {
	home := t.TempDir()
	profileRoot := filepath.Join(home, "Claude-3p")
	t.Setenv("ANTHROPIC_API_KEY", "secret")
	if err := writeClaudeDesktopConfig(home, desktopTestProvider(profileRoot)); err != nil {
		t.Fatal(err)
	}
	official := &core.Provider{
		ID: "claude-desktop-builtin", Name: "Claude Desktop (Official)",
		Category: "official",
		SettingsConfig: map[string]any{
			"claude_desktop_dir": profileRoot,
		},
	}
	if err := writeClaudeDesktopConfig(home, official); err != nil {
		t.Fatal(err)
	}
	if mode := readJSON(t, filepath.Join(home, "Claude", claudeDesktopConfigFile))["deploymentMode"]; mode != "1p" {
		t.Fatalf("normal deploymentMode = %v", mode)
	}
	if mode := readJSON(t, filepath.Join(profileRoot, claudeDesktopConfigFile))["deploymentMode"]; mode != "1p" {
		t.Fatalf("3p deploymentMode = %v", mode)
	}
	if fileExists(filepath.Join(profileRoot, "configLibrary", claudeDesktopProfileID+".json")) {
		t.Fatal("profile should be deleted for official")
	}
	meta := readJSON(t, filepath.Join(profileRoot, "configLibrary", "_meta.json"))
	if meta["appliedId"] == claudeDesktopProfileID {
		t.Fatalf("meta still applied: %#v", meta)
	}
}

func TestClaudeDesktopRollbackOnFailure(t *testing.T) {
	home := t.TempDir()
	profileRoot := filepath.Join(home, "Claude-3p")
	if err := os.MkdirAll(profileRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	normalDir := filepath.Join(home, "Claude")
	if err := os.MkdirAll(normalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	seed := []byte(`{"deploymentMode":"1p","userKey":true}`)
	if err := os.WriteFile(filepath.Join(normalDir, claudeDesktopConfigFile), seed, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profileRoot, claudeDesktopConfigFile), seed, 0o600); err != nil {
		t.Fatal(err)
	}
	// Block profile creation: configLibrary is a file, not a directory.
	if err := os.WriteFile(filepath.Join(profileRoot, "configLibrary"), []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "secret")
	if err := writeClaudeDesktopConfig(home, desktopTestProvider(profileRoot)); err == nil {
		t.Fatal("expected write to fail")
	}
	// Both config files must be rolled back to their original contents.
	if got := readJSON(t, filepath.Join(normalDir, claudeDesktopConfigFile)); got["deploymentMode"] != "1p" || got["userKey"] != true {
		t.Fatalf("normal config not rolled back: %#v", got)
	}
	if got := readJSON(t, filepath.Join(profileRoot, claudeDesktopConfigFile)); got["deploymentMode"] != "1p" || got["userKey"] != true {
		t.Fatalf("3p config not rolled back: %#v", got)
	}
}

func TestClaudeDesktopDirectRejectsNonClaudeModel(t *testing.T) {
	p := &core.Provider{Model: "gpt-5", Meta: core.ProviderMeta{ClaudeDesktopMode: "direct"}}
	p.SettingsConfig = map[string]any{"claude_desktop_dir": filepath.Join(os.TempDir(), "agentmux-cd-test")}
	if err := writeClaudeDesktopConfig(os.TempDir(), p); err == nil {
		t.Fatal("expected non-Claude direct model to be rejected")
	}
}

func TestDisableClaudeDesktopConfigClearsProfile(t *testing.T) {
	home := t.TempDir()
	profileRoot := filepath.Join(home, "Claude-3p")
	p := desktopTestProvider(profileRoot)
	p.APIKeyEnv = ""
	if err := writeClaudeDesktopConfig(home, p); err != nil {
		t.Fatal(err)
	}
	status, err := DisableClaudeDesktopConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if status.Enabled || status.Configured || status.BackupPath == "" {
		t.Fatalf("status = %+v", status)
	}
	if _, err := os.Stat(filepath.Join(profileRoot, "configLibrary", claudeDesktopProfileID+".json")); !os.IsNotExist(err) {
		t.Fatalf("profile should be moved, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(status.BackupPath, claudeDesktopProfileID+".json.moved")); err != nil {
		t.Fatalf("backup profile missing: %v", err)
	}
	meta := readJSONObject(filepath.Join(profileRoot, "configLibrary", "_meta.json"))
	if meta["appliedId"] != nil || len(meta["entries"].([]any)) != 0 {
		t.Fatalf("meta = %#v", meta)
	}
	// Disable restores 1p mode.
	if mode := readJSON(t, filepath.Join(profileRoot, claudeDesktopConfigFile))["deploymentMode"]; mode != "1p" {
		t.Fatalf("3p deploymentMode = %v", mode)
	}
}
