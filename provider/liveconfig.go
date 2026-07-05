package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/agentnexus/agentnexus/core"
	"github.com/agentnexus/agentnexus/store"
)

const claudeDesktopProfileID = "00000000-0000-4000-8000-000000157210"
const claudeDesktopProfileName = "AgentNexus"

// ClaudeDesktopConfigStatus is the read model for the Claude-3p local profile.
type ClaudeDesktopConfigStatus struct {
	Enabled           bool   `json:"enabled"`
	Configured        bool   `json:"configured"`
	ConfigDir         string `json:"config_dir"`
	ProfilePath       string `json:"profile_path,omitempty"`
	ActiveProfileID   string `json:"active_profile_id,omitempty"`
	ActiveProfileName string `json:"active_profile_name,omitempty"`
	BaseURL           string `json:"base_url,omitempty"`
	AuthScheme        string `json:"auth_scheme,omitempty"`
	ModelCount        int    `json:"model_count"`
	ProviderID        string `json:"provider_id,omitempty"`
	ProviderName      string `json:"provider_name,omitempty"`
	BackupPath        string `json:"backup_path,omitempty"`
	Message           string `json:"message,omitempty"`
}

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
	case "claude-desktop":
		return writeClaudeDesktopConfig(home, p)
	case "codex":
		return writeCodexConfig(home, p)
	case "gemini":
		return writeGeminiConfig(home, p)
	default:
		return fmt.Errorf("unsupported tool %q for live config", tool)
	}
}

func writeClaudeConfig(home string, p *core.Provider) error {
	path := filepath.Join(claudeConfigDir(home, p), "settings.json")
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
		} else {
			delete(env, "ANTHROPIC_API_KEY")
		}
	}
	if p.Model != "" {
		env["ANTHROPIC_MODEL"] = p.Model
	}
	existing["env"] = env
	return writeJSONObject(path, existing)
}

func writeCodexConfig(home string, p *core.Provider) error {
	path := filepath.Join(codexConfigDir(home, p), "config.toml")
	doc := readTOMLObject(path)
	providerID := codexProviderID(p)
	doc["model_provider"] = providerID
	if p.Model != "" {
		doc["model"] = p.Model
	}
	providers := ensureMap(doc, "model_providers")
	block := map[string]any{
		"name":     p.Name,
		"base_url": p.BaseURL,
		"wire_api": codexWireAPI(p),
	}
	if p.APIKeyEnv != "" {
		block["env_key"] = p.APIKeyEnv
	}
	providers[providerID] = block
	return writeTOMLObject(path, doc)
}

func writeClaudeDesktopConfig(home string, p *core.Provider) error {
	if p.Meta.ClaudeDesktopMode != "" && p.Meta.ClaudeDesktopMode != "direct" {
		return fmt.Errorf("claude desktop mode %q is not supported in core mode", p.Meta.ClaudeDesktopMode)
	}
	if p.Model != "" && !isClaudeDesktopDirectModel(p.Model) {
		return fmt.Errorf("model %q is not a Claude Desktop direct-mode route", p.Model)
	}
	base, err := claudeDesktopConfigDir(home, p)
	if err != nil {
		return err
	}
	apiKey := ""
	if p.APIKeyEnv != "" {
		apiKey = os.Getenv(p.APIKeyEnv)
	}
	models := p.Meta.ClaudeDesktopModels
	if len(models) == 0 && p.Model != "" {
		models = []core.ClaudeDesktopModel{{ID: p.Model, Name: p.Model, DisplayName: p.Model}}
	}
	for _, model := range models {
		if model.ID != "" && !isClaudeDesktopDirectModel(model.ID) {
			return fmt.Errorf("model %q is not a Claude Desktop direct-mode route", model.ID)
		}
	}
	profile := map[string]any{
		"coworkEgressAllowedHosts":     []string{"*"},
		"disableDeploymentModeChooser": true,
		"id":                           claudeDesktopProfileID,
		"name":                         claudeDesktopProfileName,
		"isActive":                     true,
		"inferenceGatewayBaseUrl":      p.BaseURL,
		"inferenceGatewayApiKey":       apiKey,
		"inferenceGatewayAuthScheme":   orDefault(p.Meta.ClaudeDesktopAuthMode, "bearer"),
		"inferenceModels":              claudeDesktopModelsJSON(models),
		"inferenceProvider":            "gateway",
	}
	if err := writeJSONObject(filepath.Join(base, "configLibrary", claudeDesktopProfileID+".json"), profile); err != nil {
		return err
	}
	if err := writeClaudeDesktopMeta(base, claudeDesktopProfileID, claudeDesktopProfileName); err != nil {
		return err
	}
	indexPath := filepath.Join(base, "claude_desktop_config.json")
	index := readJSONObject(indexPath)
	index["activeProfileId"] = claudeDesktopProfileID
	index["profileName"] = claudeDesktopProfileName
	index["profilePath"] = filepath.Join("configLibrary", claudeDesktopProfileID+".json")
	return writeJSONObject(indexPath, index)
}

// ClaudeDesktopStatus returns whether the AgentNexus Claude-3p profile is active.
func ClaudeDesktopStatus(p *core.Provider) (ClaudeDesktopConfigStatus, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return ClaudeDesktopConfigStatus{}, err
	}
	base, err := claudeDesktopConfigDir(home, p)
	if err != nil {
		return ClaudeDesktopConfigStatus{}, err
	}
	return claudeDesktopStatusForBase(base, "")
}

// DisableClaudeDesktopConfig removes the AgentNexus Claude-3p profile from the
// active config library, backing up touched files next to Claude-3p first.
func DisableClaudeDesktopConfig(p *core.Provider) (ClaudeDesktopConfigStatus, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return ClaudeDesktopConfigStatus{}, err
	}
	base, err := claudeDesktopConfigDir(home, p)
	if err != nil {
		return ClaudeDesktopConfigStatus{}, err
	}
	backupPath, err := disableClaudeDesktopProfile(base)
	if err != nil {
		return ClaudeDesktopConfigStatus{}, err
	}
	return claudeDesktopStatusForBase(base, backupPath)
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

func claudeDesktopConfigDir(home string, p *core.Provider) (string, error) {
	if v := configString(p, "claude_desktop_dir"); v != "" {
		return v, nil
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Claude-3p"), nil
	case "windows":
		if v := os.Getenv("APPDATA"); v != "" {
			return filepath.Join(v, "Claude-3p"), nil
		}
		return filepath.Join(home, "AppData", "Roaming", "Claude-3p"), nil
	default:
		return "", fmt.Errorf("claude desktop direct mode is unsupported on %s", runtime.GOOS)
	}
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

func codexProviderID(p *core.Provider) string {
	if v := configString(p, "codex_provider_id"); v != "" {
		return v
	}
	id := p.ID
	if id == "" {
		id = "agentnexus"
	}
	return strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' {
			return r
		}
		return '_'
	}, id)
}

func codexWireAPI(p *core.Provider) string {
	if p.Meta.CodexWireAPI != "" {
		return p.Meta.CodexWireAPI
	}
	switch p.Meta.APIFormat {
	case "openai_responses":
		return "responses"
	case "openai_chat":
		return "chat"
	default:
		if strings.Contains(strings.ToLower(p.ID), "openai") {
			return "responses"
		}
		return "chat"
	}
}

func isClaudeDesktopDirectModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(model, "claude-") || strings.HasPrefix(model, "anthropic/claude-")
}

func claudeDesktopModelsJSON(models []core.ClaudeDesktopModel) []map[string]string {
	out := make([]map[string]string, 0, len(models))
	for _, model := range models {
		id := model.ID
		if id == "" {
			id = model.Name
		}
		if id == "" {
			continue
		}
		name := model.Name
		if name == "" {
			name = id
		}
		display := model.DisplayName
		if display == "" {
			display = name
		}
		out = append(out, map[string]string{
			"id":            id,
			"name":          name,
			"displayName":   display,
			"labelOverride": display,
		})
	}
	return out
}

func writeClaudeDesktopMeta(base, id, name string) error {
	path := filepath.Join(base, "configLibrary", "_meta.json")
	meta := readJSONObject(path)
	meta["appliedId"] = id
	meta["entries"] = appendClaudeDesktopEntry(meta["entries"], id, name)
	return writeJSONObject(path, meta)
}

func appendClaudeDesktopEntry(raw any, id, name string) []any {
	entries := filterClaudeDesktopEntries(raw, id)
	return append(entries, map[string]any{"id": id, "name": name})
}

func filterClaudeDesktopEntries(raw any, id string) []any {
	entries := make([]any, 0)
	if list, ok := raw.([]any); ok {
		for _, item := range list {
			entry, ok := item.(map[string]any)
			if !ok || stringValue(entry["id"]) == id {
				continue
			}
			entries = append(entries, entry)
		}
	}
	return entries
}

func disableClaudeDesktopProfile(base string) (string, error) {
	configLibrary := filepath.Join(base, "configLibrary")
	profilePath := filepath.Join(configLibrary, claudeDesktopProfileID+".json")
	metaPath := filepath.Join(configLibrary, "_meta.json")
	indexPath := filepath.Join(base, "claude_desktop_config.json")
	backupDir := filepath.Join(base, "configLibrary.backup."+time.Now().Format("20060102-150405"))
	changed := false

	meta := readJSONObject(metaPath)
	if len(meta) > 0 {
		if err := backupClaudeDesktopFile(metaPath, backupDir); err != nil {
			return "", err
		}
		if stringValue(meta["appliedId"]) == claudeDesktopProfileID {
			meta["appliedId"] = nil
			changed = true
		}
		filtered := filterClaudeDesktopEntries(meta["entries"], claudeDesktopProfileID)
		if len(filtered) != lenFromJSONArray(meta["entries"]) {
			meta["entries"] = filtered
			changed = true
		}
		if changed {
			if err := writeJSONObject(metaPath, meta); err != nil {
				return "", err
			}
		}
	}

	index := readJSONObject(indexPath)
	if len(index) > 0 && stringValue(index["activeProfileId"]) == claudeDesktopProfileID {
		if err := backupClaudeDesktopFile(indexPath, backupDir); err != nil {
			return "", err
		}
		delete(index, "activeProfileId")
		delete(index, "profileName")
		delete(index, "profilePath")
		if err := writeJSONObject(indexPath, index); err != nil {
			return "", err
		}
		changed = true
	}

	if fileExists(profilePath) {
		if err := os.MkdirAll(backupDir, 0o755); err != nil {
			return "", err
		}
		if err := os.Rename(profilePath, filepath.Join(backupDir, filepath.Base(profilePath)+".moved")); err != nil {
			return "", err
		}
		changed = true
	}
	if !changed {
		return "", nil
	}
	return backupDir, nil
}

func backupClaudeDesktopFile(path, backupDir string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(backupDir, filepath.Base(path)), data, 0o600)
}

func claudeDesktopStatusForBase(base, backupPath string) (ClaudeDesktopConfigStatus, error) {
	profilePath := filepath.Join(base, "configLibrary", claudeDesktopProfileID+".json")
	profile := readJSONObject(profilePath)
	meta := readJSONObject(filepath.Join(base, "configLibrary", "_meta.json"))
	index := readJSONObject(filepath.Join(base, "claude_desktop_config.json"))

	activeID := stringValue(meta["appliedId"])
	activeName := claudeDesktopEntryName(meta["entries"], activeID)
	if activeID == "" {
		activeID = stringValue(index["activeProfileId"])
		activeName = stringValue(index["profileName"])
	}
	if activeName == "" && activeID == claudeDesktopProfileID {
		activeName = stringValue(profile["name"])
	}
	enabled := activeID == claudeDesktopProfileID
	configured := len(profile) > 0
	status := ClaudeDesktopConfigStatus{
		Enabled:           enabled,
		Configured:        configured,
		ConfigDir:         base,
		ProfilePath:       profilePath,
		ActiveProfileID:   activeID,
		ActiveProfileName: activeName,
		BaseURL:           stringValue(profile["inferenceGatewayBaseUrl"]),
		AuthScheme:        stringValue(profile["inferenceGatewayAuthScheme"]),
		ModelCount:        lenFromJSONArray(profile["inferenceModels"]),
		BackupPath:        backupPath,
	}
	switch {
	case enabled:
		status.Message = "Claude-3p is enabled through AgentNexus."
	case configured:
		status.Message = "AgentNexus Claude-3p profile is installed but inactive."
	default:
		status.Message = "No AgentNexus Claude-3p profile is active."
	}
	return status, nil
}

func claudeDesktopEntryName(raw any, id string) string {
	if id == "" {
		return ""
	}
	list, ok := raw.([]any)
	if !ok {
		return ""
	}
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if ok && stringValue(entry["id"]) == id {
			return stringValue(entry["name"])
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
