package provider

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

func newTestService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "svc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := &Service{Manager: NewManager(st), st: st, proxy: NewProxyServer(nil, st, "127.0.0.1:0")}
	t.Cleanup(func() { _ = svc.Proxy().Stop() })
	return svc, st
}

func TestTakeoverClaudeCodeRoundtrip(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	claudeDir := filepath.Join(home, ".claude")
	svc, st := newTestService(t)

	t.Setenv("RELAY_KEY_TK", "sk-relay")
	p := &core.Provider{
		ID: "relay", Name: "Relay", Category: "third_party",
		BaseURL: "https://relay.example.com", APIKeyEnv: "RELAY_KEY_TK",
		Model:          "relay-model",
		SettingsConfig: map[string]any{"claude_config_dir": claudeDir},
		Meta:           core.ProviderMeta{APIFormat: "anthropic"},
	}
	other := &core.Provider{
		ID: "relay2", Name: "Relay 2", Category: "third_party",
		BaseURL: "https://relay2.example.com", APIKeyEnv: "RELAY_KEY_TK",
		SettingsConfig: map[string]any{"claude_config_dir": claudeDir},
		Meta:           core.ProviderMeta{APIFormat: "anthropic"},
	}
	for _, prov := range []*core.Provider{p, other} {
		if err := svc.Upsert(ctx, prov); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.Switch(ctx, p.ID, "claudecode"); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")
	original, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.EnableTakeover(ctx, "claudecode"); err != nil {
		t.Fatal(err)
	}
	if !svc.Proxy().Running() {
		t.Fatal("proxy should be running after takeover")
	}
	env := envOf(t, readJSON(t, settingsPath))
	wantClaudeURL := svc.Proxy().BaseURL() + "/claude"
	if env["ANTHROPIC_BASE_URL"] != wantClaudeURL {
		t.Fatalf("base url = %v want %v", env["ANTHROPIC_BASE_URL"], wantClaudeURL)
	}
	if env["ANTHROPIC_AUTH_TOKEN"] != ProxyManagedToken {
		t.Fatalf("token = %v", env["ANTHROPIC_AUTH_TOKEN"])
	}
	if env["CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY"] != "1" {
		t.Fatalf("gateway model discovery = %v", env["CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY"])
	}
	if _, exists, _ := st.GetLiveBackup(ctx, "claudecode"); !exists {
		t.Fatal("live backup missing")
	}

	// Switching under takeover is a DB-only hot switch: live keeps pointing
	// at the proxy.
	if err := svc.Switch(ctx, other.ID, "claudecode"); err != nil {
		t.Fatal(err)
	}
	env = envOf(t, readJSON(t, settingsPath))
	if env["ANTHROPIC_BASE_URL"] != wantClaudeURL {
		t.Fatalf("hot switch rewrote live config: %v", env["ANTHROPIC_BASE_URL"])
	}
	id, ok, _ := st.ActiveProviderID(ctx, "claudecode")
	if !ok || id != other.ID {
		t.Fatalf("active = %q,%v", id, ok)
	}

	if err := svc.DisableTakeover(ctx, "claudecode"); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(original) {
		t.Fatalf("live not restored.\noriginal: %s\nrestored: %s", original, restored)
	}
	if _, exists, _ := st.GetLiveBackup(ctx, "claudecode"); exists {
		t.Fatal("backup should be deleted after restore")
	}
	if svc.Proxy().Running() {
		t.Fatal("proxy should stop when no tool is taken over")
	}
}

func TestTakeoverDisablePreservesThirdPartyDrift(t *testing.T) {
	ctx := context.Background()
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	svc, st := newTestService(t)
	t.Setenv("DRIFT_KEY", "sk-drift")
	p := &core.Provider{
		ID: "drift", Name: "Drift", Category: "third_party",
		BaseURL: "https://relay.example.com", APIKeyEnv: "DRIFT_KEY",
		SettingsConfig: map[string]any{"claude_config_dir": claudeDir},
		Meta:           core.ProviderMeta{APIFormat: "anthropic"},
	}
	if err := svc.Upsert(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := svc.Switch(ctx, p.ID, "claudecode"); err != nil {
		t.Fatal(err)
	}
	if err := svc.EnableTakeover(ctx, "claudecode"); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")
	settings := readJSON(t, settingsPath)
	env := envOf(t, settings)
	env["ANTHROPIC_BASE_URL"] = "http://127.0.0.1:19999"
	env["FLUX_KEEP"] = "third-party"
	settings["env"] = env
	raw, _ := json.MarshalIndent(settings, "", "  ")
	if err := os.WriteFile(settingsPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	err := svc.DisableTakeover(ctx, "claudecode")
	var drift *TakeoverDriftError
	if !errors.As(err, &drift) {
		t.Fatalf("disable error = %v, want TakeoverDriftError", err)
	}
	after := envOf(t, readJSON(t, settingsPath))
	if after["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:19999" || after["FLUX_KEEP"] != "third-party" {
		t.Fatalf("third-party values were overwritten: %#v", after)
	}
	if _, exists, _ := st.GetLiveBackup(ctx, "claudecode"); !exists {
		t.Fatal("ownership journal must remain while drift is unresolved")
	}
}

func TestTakeoverRefusesForeignLoopbackOwner(t *testing.T) {
	ctx := context.Background()
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")
	original := []byte(`{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:15721","CC_SWITCH":"owned"}}`)
	if err := os.WriteFile(settingsPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	svc, st := newTestService(t)
	p := &core.Provider{
		ID: "foreign", Name: "Foreign", Category: "third_party", BaseURL: "https://relay.example.com",
		SettingsConfig: map[string]any{"claude_config_dir": claudeDir}, Meta: core.ProviderMeta{APIFormat: "anthropic"},
	}
	if err := svc.Upsert(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActiveProvider(ctx, "claudecode", p.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.EnableTakeover(ctx, "claudecode"); err == nil || !strings.Contains(err.Error(), "refusing takeover") {
		t.Fatalf("enable error = %v, want foreign-owner refusal", err)
	}
	got, _ := os.ReadFile(settingsPath)
	if string(got) != string(original) {
		t.Fatalf("foreign config changed: %s", got)
	}
}

func TestTakeoverClaudeCodeUsesAPIKeyWhenPrimaryAPIKeyExists(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "config.json"), []byte(`{"primaryApiKey":"set"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	svc, st := newTestService(t)
	p := &core.Provider{
		ID: "relay", Name: "Relay", Category: "third_party",
		BaseURL: "https://relay.example.com", APIKeyEnv: "RELAY_KEY_TK",
		SettingsConfig: map[string]any{"claude_config_dir": claudeDir},
		Meta:           core.ProviderMeta{APIFormat: "anthropic"},
	}
	if err := svc.Upsert(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActiveProvider(ctx, "claudecode", p.ID); err != nil {
		t.Fatal(err)
	}

	if err := svc.EnableTakeover(ctx, "claudecode"); err != nil {
		t.Fatal(err)
	}
	env := envOf(t, readJSON(t, filepath.Join(claudeDir, "settings.json")))
	if env["ANTHROPIC_API_KEY"] != ProxyManagedToken {
		t.Fatalf("api key = %v", env["ANTHROPIC_API_KEY"])
	}
	if _, ok := env["ANTHROPIC_AUTH_TOKEN"]; ok {
		t.Fatalf("auth token should be absent to avoid Claude auth conflicts: %v", env)
	}
}

func TestTakeoverCodexRoundtrip(t *testing.T) {
	ctx := context.Background()
	codexDir := filepath.Join(t.TempDir(), "codex-home")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte("approval_policy = \"never\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc, st := newTestService(t)
	t.Setenv("CODEX_TK_KEY", "sk-ck")
	p := &core.Provider{
		ID: "codex-relay", Name: "Codex Relay", Category: "third_party",
		BaseURL: "https://relay.example/v1", APIKeyEnv: "CODEX_TK_KEY",
		Model:          "some-model",
		SettingsConfig: map[string]any{"codex_home": codexDir},
		Meta: core.ProviderMeta{
			APIFormat:           "openai_chat",
			SupportedAPIFormats: []string{"openai_chat", "openai_responses"},
		},
	}
	if err := svc.Upsert(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := svc.Switch(ctx, p.ID, "codex"); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(codexDir, "config.toml")
	original, _ := os.ReadFile(configPath)

	if err := svc.EnableTakeover(ctx, "codex"); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if _, err := toml.DecodeFile(configPath, &doc); err != nil {
		t.Fatal(err)
	}
	block := doc["model_providers"].(map[string]any)[codexModelProviderID].(map[string]any)
	if block["base_url"] != svc.Proxy().BaseURL()+"/v1" {
		t.Fatalf("base_url = %v", block["base_url"])
	}
	if block["experimental_bearer_token"] != ProxyManagedToken {
		t.Fatalf("bearer = %v", block["experimental_bearer_token"])
	}
	if block["wire_api"] != codexWireAPIResponses {
		t.Fatalf("wire_api = %v", block["wire_api"])
	}
	if doc["approval_policy"] != "never" {
		t.Fatalf("approval_policy lost: %#v", doc)
	}

	if err := svc.DisableTakeover(ctx, "codex"); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(configPath)
	if string(restored) != string(original) {
		t.Fatalf("codex config not restored.\noriginal: %s\nrestored: %s", original, restored)
	}
	if _, exists, _ := st.GetLiveBackup(ctx, "codex"); exists {
		t.Fatal("backup should be deleted")
	}
}

func TestTakeoverClaudeDesktopProxyProfile(t *testing.T) {
	ctx := context.Background()
	profileRoot := filepath.Join(t.TempDir(), "Claude-3p")
	svc, st := newTestService(t)
	p := &core.Provider{
		ID: "cd-relay", Name: "Desktop Relay", Category: "third_party",
		BaseURL:        "https://relay.example.com",
		SettingsConfig: map[string]any{"claude_desktop_dir": profileRoot},
		Meta: core.ProviderMeta{
			APIFormat:         "anthropic",
			ClaudeDesktopMode: "direct",
			ClaudeDesktopModels: []core.ClaudeDesktopModel{
				{ID: "claude-sonnet-4-8", DisplayName: "Sonnet", UpstreamModel: "relay-sonnet"},
			},
		},
	}
	if err := svc.Upsert(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := svc.Switch(ctx, p.ID, "claude-desktop"); err != nil {
		t.Fatal(err)
	}
	if err := svc.EnableTakeover(ctx, "claude-desktop"); err != nil {
		t.Fatal(err)
	}
	profile := readJSON(t, filepath.Join(profileRoot, "configLibrary", claudeDesktopProfileID+".json"))
	wantURL := svc.Proxy().BaseURL() + "/claude-desktop"
	if profile["inferenceGatewayBaseUrl"] != wantURL {
		t.Fatalf("gateway url = %v want %v", profile["inferenceGatewayBaseUrl"], wantURL)
	}
	token, _ := st.GetOrCreateGatewayToken(ctx)
	if profile["inferenceGatewayApiKey"] != token {
		t.Fatalf("gateway token mismatch")
	}
	raw, _ := json.Marshal(profile["inferenceModels"])
	if !strings.Contains(string(raw), "claude-sonnet-4-8") {
		t.Fatalf("models = %s", raw)
	}

	if err := svc.DisableTakeover(ctx, "claude-desktop"); err != nil {
		t.Fatal(err)
	}
	// Restored to the direct-mode profile (pre-takeover backup).
	profile = readJSON(t, filepath.Join(profileRoot, "configLibrary", claudeDesktopProfileID+".json"))
	if profile["inferenceGatewayBaseUrl"] != p.BaseURL {
		t.Fatalf("restored gateway url = %v", profile["inferenceGatewayBaseUrl"])
	}
}

func TestTakeoverClaudeDesktopHotSwitchAdvancesJournalAndPreservesUnownedKeys(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	profileRoot := filepath.Join(home, "Claude-3p")
	normalRoot := filepath.Join(home, "Claude")
	if err := os.MkdirAll(normalRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	normalConfig := filepath.Join(normalRoot, claudeDesktopConfigFile)
	if err := os.WriteFile(normalConfig, []byte(`{"deploymentMode":"1p","fluxOriginal":"keep"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	svc, st := newTestService(t)
	providerFor := func(id, route, upstream string) *core.Provider {
		return &core.Provider{
			ID: id, Name: id, Category: "third_party", BaseURL: "https://" + id + ".example.com",
			SettingsConfig: map[string]any{
				"claude_desktop_dir":        profileRoot,
				"claude_desktop_normal_dir": normalRoot,
			},
			Meta: core.ProviderMeta{
				APIFormat:         "anthropic",
				ClaudeDesktopMode: "direct",
				ClaudeDesktopModels: []core.ClaudeDesktopModel{
					{ID: route, DisplayName: route, UpstreamModel: upstream},
				},
			},
		}
	}
	first := providerFor("desktop-first", "claude-sonnet-4-8", "first-sonnet")
	second := providerFor("desktop-second", "claude-opus-4-6", "second-opus")
	for _, item := range []*core.Provider{first, second} {
		if err := svc.Upsert(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.Switch(ctx, first.ID, "claude-desktop"); err != nil {
		t.Fatal(err)
	}
	if err := svc.EnableTakeover(ctx, "claude-desktop"); err != nil {
		t.Fatal(err)
	}
	journalBefore, ok, err := st.GetLiveBackup(ctx, "claude-desktop")
	if err != nil || !ok {
		t.Fatalf("journal before = %q, %v, %v", journalBefore, ok, err)
	}

	// Flux/user state added to a shared, pre-existing file is not an owned
	// pointer and must survive both the hot-switch and final restore.
	normal := readJSON(t, normalConfig)
	normal["fluxRuntime"] = "preserve"
	raw, _ := json.MarshalIndent(normal, "", "  ")
	if err := os.WriteFile(normalConfig, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(profileRoot, "configLibrary", claudeDesktopProfileID+".json")
	profile := readJSON(t, profilePath)
	profile["fluxProfileMetadata"] = "preserve"
	raw, _ = json.MarshalIndent(profile, "", "  ")
	if err := os.WriteFile(profilePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := svc.Switch(ctx, second.ID, "claude-desktop"); err != nil {
		t.Fatal(err)
	}
	journalAfter, ok, err := st.GetLiveBackup(ctx, "claude-desktop")
	if err != nil || !ok || journalAfter == journalBefore {
		t.Fatalf("hot-switch did not advance ownership journal: ok=%v err=%v", ok, err)
	}
	profile = readJSON(t, profilePath)
	profileRaw, _ := json.Marshal(profile["inferenceModels"])
	if !strings.Contains(string(profileRaw), "claude-opus-4-6") || strings.Contains(string(profileRaw), "claude-sonnet-4-8") {
		t.Fatalf("profile was not hot-switched: %s", profileRaw)
	}
	if profile["fluxProfileMetadata"] != "preserve" {
		t.Fatalf("hot-switch erased unowned profile metadata: %#v", profile)
	}

	if err := svc.DisableTakeover(ctx, "claude-desktop"); err != nil {
		t.Fatal(err)
	}
	normal = readJSON(t, normalConfig)
	if normal["deploymentMode"] != "3p" || normal["fluxOriginal"] != "keep" || normal["fluxRuntime"] != "preserve" {
		t.Fatalf("shared config was not safely restored: %#v", normal)
	}
	profile = readJSON(t, profilePath)
	profileRaw, _ = json.Marshal(profile["inferenceModels"])
	if !strings.Contains(string(profileRaw), "claude-sonnet-4-8") {
		t.Fatalf("original direct profile was not restored: %s", profileRaw)
	}
	if profile["fluxProfileMetadata"] != "preserve" {
		t.Fatalf("restore erased unowned profile metadata: %#v", profile)
	}
}

func TestTakeoverClaudeDesktopHotSwitchRejectsOwnedDrift(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	profileRoot := filepath.Join(home, "Claude-3p")
	svc, st := newTestService(t)
	providerFor := func(id, route string) *core.Provider {
		return &core.Provider{
			ID: id, Name: id, Category: "third_party", BaseURL: "https://" + id + ".example.com",
			SettingsConfig: map[string]any{"claude_desktop_dir": profileRoot},
			Meta: core.ProviderMeta{APIFormat: "anthropic", ClaudeDesktopMode: "direct", ClaudeDesktopModels: []core.ClaudeDesktopModel{
				{ID: route, DisplayName: route, UpstreamModel: route},
			}},
		}
	}
	first := providerFor("drift-first", "claude-sonnet-4-8")
	second := providerFor("drift-second", "claude-opus-4-6")
	for _, item := range []*core.Provider{first, second} {
		if err := svc.Upsert(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.Switch(ctx, first.ID, "claude-desktop"); err != nil {
		t.Fatal(err)
	}
	if err := svc.EnableTakeover(ctx, "claude-desktop"); err != nil {
		t.Fatal(err)
	}
	journalBefore, _, _ := st.GetLiveBackup(ctx, "claude-desktop")
	profilePath := filepath.Join(profileRoot, "configLibrary", claudeDesktopProfileID+".json")
	profile := readJSON(t, profilePath)
	profile["inferenceGatewayBaseUrl"] = "http://127.0.0.1:16666/flux"
	raw, _ := json.MarshalIndent(profile, "", "  ")
	if err := os.WriteFile(profilePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	err := svc.Switch(ctx, second.ID, "claude-desktop")
	var drift *TakeoverDriftError
	if !errors.As(err, &drift) {
		t.Fatalf("hot-switch error = %v, want TakeoverDriftError", err)
	}
	active, ok, activeErr := st.ActiveProviderID(ctx, "claude-desktop")
	if activeErr != nil || !ok || active != first.ID {
		t.Fatalf("failed hot-switch changed active provider: %q, %v, %v", active, ok, activeErr)
	}
	journalAfter, _, _ := st.GetLiveBackup(ctx, "claude-desktop")
	if journalAfter != journalBefore {
		t.Fatal("failed hot-switch changed ownership journal")
	}
	if got := readJSON(t, profilePath)["inferenceGatewayBaseUrl"]; got != "http://127.0.0.1:16666/flux" {
		t.Fatalf("owned drift was overwritten: %v", got)
	}
}

func TestSwitchRouteClaudeDesktopProxyAutoEnablesTakeover(t *testing.T) {
	ctx := context.Background()
	profileRoot := filepath.Join(t.TempDir(), "Claude-3p")
	svc, st := newTestService(t)
	p := &core.Provider{
		ID: "relay", Name: "Relay", Category: "third_party",
		BaseURL:        "https://relay.example.com",
		SettingsConfig: map[string]any{"claude_desktop_dir": profileRoot},
		Meta: core.ProviderMeta{
			APIFormat: "anthropic",
			ClaudeDesktopModels: []core.ClaudeDesktopModel{
				{ID: "ark/60b-0614c", Name: "ark/60b-0614c", DisplayName: "ark/60b-0614c"},
			},
		},
	}
	if err := svc.Upsert(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := svc.SwitchRoute(ctx, core.ProviderRoute{
		Tool:       "claude-desktop",
		ProviderID: p.ID,
		Meta:       core.ProviderMeta{ClaudeDesktopMode: "proxy"},
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := st.GetProxyToolConfig(ctx, "claude-desktop")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled {
		t.Fatal("claude-desktop takeover should be enabled")
	}
	if !svc.Proxy().Running() {
		t.Fatal("proxy should be running")
	}
	route, ok, err := st.ActiveProviderRoute(ctx, "claude-desktop")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || route.ProviderID != p.ID || route.Meta.ClaudeDesktopMode != "proxy" {
		t.Fatalf("route = %#v ok=%v", route, ok)
	}
	profile := readJSON(t, filepath.Join(profileRoot, "configLibrary", claudeDesktopProfileID+".json"))
	if profile["inferenceGatewayBaseUrl"] != svc.Proxy().BaseURL()+"/claude-desktop" {
		t.Fatalf("gateway url = %v", profile["inferenceGatewayBaseUrl"])
	}
	raw, _ := json.Marshal(profile["inferenceModels"])
	if !strings.Contains(string(raw), "claude-sonnet-5") || !strings.Contains(string(raw), "ark/60b-0614c") {
		t.Fatalf("models = %s", raw)
	}
	if strings.Contains(string(raw), `"id":"ark/60b-0614c"`) {
		t.Fatalf("raw upstream id leaked into profile route id: %s", raw)
	}
}

func TestDirectSwitchRejectsConvertingFormats(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	p := &core.Provider{
		ID: "or", Name: "OpenRouter", Category: "third_party",
		BaseURL: "https://openrouter.ai/api/v1",
		Meta:    core.ProviderMeta{APIFormat: "openai_chat"},
	}
	if err := svc.Upsert(ctx, p); err != nil {
		t.Fatal(err)
	}
	err := svc.Switch(ctx, p.ID, "claudecode")
	if err == nil || !strings.Contains(err.Error(), "local routing") {
		t.Fatalf("expected local-routing hint, got %v", err)
	}
}

func TestSwitchRouteWithLocalTakeoverAllowsConvertingFormats(t *testing.T) {
	ctx := context.Background()
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	svc, st := newTestService(t)
	p := &core.Provider{
		ID: "or", Name: "OpenRouter", Category: "third_party",
		BaseURL:        "https://openrouter.ai/api/v1",
		SettingsConfig: map[string]any{"claude_config_dir": claudeDir},
		Meta:           core.ProviderMeta{APIFormat: "openai_chat"},
	}
	if err := svc.Upsert(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := svc.SwitchRouteWithLocalTakeover(ctx, core.ProviderRoute{
		Tool:       "claudecode",
		ProviderID: p.ID,
	}, true); err != nil {
		t.Fatal(err)
	}
	cfg, err := st.GetProxyToolConfig(ctx, "claudecode")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled {
		t.Fatal("claudecode takeover should be enabled")
	}
	route, ok, err := st.ActiveProviderRoute(ctx, "claudecode")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || route.ProviderID != p.ID {
		t.Fatalf("route = %#v ok=%v", route, ok)
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")
	env := envOf(t, readJSON(t, settingsPath))
	wantClaudeURL := svc.Proxy().BaseURL() + "/claude"
	if env["ANTHROPIC_BASE_URL"] != wantClaudeURL {
		t.Fatalf("base url = %v want %v", env["ANTHROPIC_BASE_URL"], wantClaudeURL)
	}
	if env["ANTHROPIC_AUTH_TOKEN"] != ProxyManagedToken {
		t.Fatalf("token = %v", env["ANTHROPIC_AUTH_TOKEN"])
	}
	if env["CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY"] != "1" {
		t.Fatalf("gateway model discovery = %v", env["CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY"])
	}
}

func TestRestoreProxyStateRepairsLegacyCodexWireAPI(t *testing.T) {
	ctx := context.Background()
	codexDir := filepath.Join(t.TempDir(), "codex-home")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `model_provider = "agentmux"
approval_policy = "never"

[model_providers.agentmux]
name = "AgentMux Local Routing"
base_url = "http://127.0.0.1:8787/v1"
wire_api = "chat"

[model_providers.openai]
name = "OpenAI"
wire_api = "chat"
`
	configPath := filepath.Join(codexDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	svc, st := newTestService(t)
	p := &core.Provider{
		ID: "codex-relay", Name: "Codex Relay", Category: "third_party",
		BaseURL:        "https://relay.example/v1",
		SettingsConfig: map[string]any{"codex_home": codexDir},
		Meta:           core.ProviderMeta{APIFormat: "openai_chat"},
	}
	if err := st.UpsertProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActiveProvider(ctx, "codex", p.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestoreProxyState(ctx); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if _, err := toml.DecodeFile(configPath, &doc); err != nil {
		t.Fatal(err)
	}
	providers := doc["model_providers"].(map[string]any)
	own := providers[codexModelProviderID].(map[string]any)
	if own["wire_api"] != codexWireAPIResponses {
		t.Fatalf("agentmux wire_api = %v", own["wire_api"])
	}
	if own["base_url"] != "http://127.0.0.1:8787/v1" {
		t.Fatalf("base_url should be preserved: %#v", own)
	}
	builtin := providers["openai"].(map[string]any)
	if builtin["wire_api"] != "chat" {
		t.Fatalf("built-in provider block must not be rewritten: %#v", builtin)
	}
	if doc["approval_policy"] != "never" {
		t.Fatalf("approval_policy lost: %#v", doc)
	}
}

func TestDirectCodexSwitchRejectsChatOnlyProvider(t *testing.T) {
	p := &core.Provider{
		ID: "chat-only",
		Meta: core.ProviderMeta{
			APIFormat:          "openai_chat",
			SupportedProtocols: []string{"chat_completions"},
		},
	}
	err := validateDirectSwitch(p, "codex")
	if err == nil || !strings.Contains(err.Error(), "local routing takeover") {
		t.Fatalf("expected takeover requirement, got %v", err)
	}
	p.Meta.SupportedProtocols = append(p.Meta.SupportedProtocols, "responses")
	if err := validateDirectSwitch(p, "codex"); err != nil {
		t.Fatalf("responses-capable provider should pass: %v", err)
	}
}
