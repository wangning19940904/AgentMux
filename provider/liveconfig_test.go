package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/agentnexus/agentnexus/core"
)

func TestWriteCodexConfigUsesTomlAndPreservesAuth(t *testing.T) {
	home := t.TempDir()
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte("approval_policy = \"never\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(codexDir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"tokens":"keep"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &core.Provider{
		ID:        "openrouter",
		Name:      "OpenRouter",
		BaseURL:   "https://openrouter.ai/api/v1",
		APIKeyEnv: "OPENROUTER_API_KEY",
		Model:     "anthropic/claude-sonnet-4.5",
		Meta:      core.ProviderMeta{CodexWireAPI: "chat"},
	}
	if err := writeCodexConfig(home, p); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if _, err := toml.DecodeFile(filepath.Join(codexDir, "config.toml"), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["approval_policy"] != "never" {
		t.Fatalf("approval_policy = %v", doc["approval_policy"])
	}
	if doc["model_provider"] != "openrouter" || doc["model"] != p.Model {
		t.Fatalf("codex route = %#v", doc)
	}
	providers := doc["model_providers"].(map[string]any)
	block := providers["openrouter"].(map[string]any)
	if block["wire_api"] != "chat" || block["env_key"] != "OPENROUTER_API_KEY" {
		t.Fatalf("provider block = %#v", block)
	}
	auth, _ := os.ReadFile(authPath)
	if string(auth) != `{"tokens":"keep"}` {
		t.Fatalf("auth.json was modified: %s", auth)
	}
}

func TestClaudeDesktopDirectProfile(t *testing.T) {
	home := t.TempDir()
	profileRoot := filepath.Join(home, "Claude-3p")
	t.Setenv("ANTHROPIC_API_KEY", "secret")
	p := &core.Provider{
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
	if err := writeClaudeDesktopConfig(home, p); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(profileRoot, "configLibrary", claudeDesktopProfileID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var profile map[string]any
	if err := json.Unmarshal(raw, &profile); err != nil {
		t.Fatal(err)
	}
	if profile["inferenceGatewayApiKey"] != "secret" || profile["inferenceGatewayAuthScheme"] != "bearer" {
		t.Fatalf("profile = %#v", profile)
	}
	if !strings.Contains(string(raw), "claude-sonnet-4-8") {
		t.Fatalf("model missing from profile: %s", raw)
	}
	metaRaw, err := os.ReadFile(filepath.Join(profileRoot, "configLibrary", "_meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["appliedId"] != claudeDesktopProfileID {
		t.Fatalf("meta = %#v", meta)
	}
	status, err := ClaudeDesktopStatus(p)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Enabled || !status.Configured || status.BaseURL != p.BaseURL || status.ModelCount != 1 {
		t.Fatalf("status = %+v", status)
	}
}

func TestClaudeDesktopDirectRejectsNonClaudeModel(t *testing.T) {
	p := &core.Provider{Model: "gpt-5", Meta: core.ProviderMeta{ClaudeDesktopMode: "direct"}}
	if err := writeClaudeDesktopConfig(t.TempDir(), p); err == nil {
		t.Fatal("expected non-Claude direct model to be rejected")
	}
}

func TestDisableClaudeDesktopConfigClearsProfile(t *testing.T) {
	home := t.TempDir()
	profileRoot := filepath.Join(home, "Claude-3p")
	p := &core.Provider{
		ID:      "claude-desktop-official",
		Name:    "Claude Desktop Direct",
		BaseURL: "https://api.anthropic.com",
		Model:   "claude-sonnet-4-8",
		SettingsConfig: map[string]any{
			"claude_desktop_dir": profileRoot,
		},
		Meta: core.ProviderMeta{ClaudeDesktopMode: "direct"},
	}
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
}
