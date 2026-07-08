package provider

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/agentnexus/agentnexus/core"
	"github.com/agentnexus/agentnexus/store"
)

const claudeDesktopProfileID = "00000000-0000-4000-8000-000000157210"
const claudeDesktopProfileName = "AgentNexus"
const claudeDesktopConfigFile = "claude_desktop_config.json"

var claudeDesktopDefaultProxyRoutes = []string{
	"claude-sonnet-5",
	"claude-opus-4-8",
	"claude-haiku-4-5",
	"claude-fable-5",
}

// ClaudeDesktopConfigStatus is the read model for the Claude-3p local profile.
type ClaudeDesktopConfigStatus struct {
	Enabled           bool   `json:"enabled"`
	Configured        bool   `json:"configured"`
	ConfigDir         string `json:"config_dir"`
	ProfilePath       string `json:"profile_path,omitempty"`
	ActiveProfileID   string `json:"active_profile_id,omitempty"`
	ActiveProfileName string `json:"active_profile_name,omitempty"`
	DeploymentMode    string `json:"deployment_mode,omitempty"`
	BaseURL           string `json:"base_url,omitempty"`
	AuthScheme        string `json:"auth_scheme,omitempty"`
	ModelCount        int    `json:"model_count"`
	ProviderID        string `json:"provider_id,omitempty"`
	ProviderName      string `json:"provider_name,omitempty"`
	BackupPath        string `json:"backup_path,omitempty"`
	Message           string `json:"message,omitempty"`
}

// claudeDesktopPaths mirrors cc-switch's ClaudeDesktopPaths: the normal Claude
// dir and the Claude-3p dir each carry a claude_desktop_config.json whose
// deploymentMode ("1p"/"3p") decides whether the 3P profile is used.
type claudeDesktopPaths struct {
	normalConfigPath string
	threepConfigPath string
	configLibrary    string
	profilePath      string
	metaPath         string
	baseDir          string
}

func claudeDesktopPathsFor(home string, p *core.Provider) (claudeDesktopPaths, error) {
	base, err := claudeDesktopConfigDir(home, p)
	if err != nil {
		return claudeDesktopPaths{}, err
	}
	normal := configString(p, "claude_desktop_normal_dir")
	if normal == "" {
		normal = filepath.Join(filepath.Dir(base), "Claude")
	}
	configLibrary := filepath.Join(base, "configLibrary")
	return claudeDesktopPaths{
		normalConfigPath: filepath.Join(normal, claudeDesktopConfigFile),
		threepConfigPath: filepath.Join(base, claudeDesktopConfigFile),
		configLibrary:    configLibrary,
		profilePath:      filepath.Join(configLibrary, claudeDesktopProfileID+".json"),
		metaPath:         filepath.Join(configLibrary, "_meta.json"),
		baseDir:          base,
	}, nil
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
		return "", fmt.Errorf("claude desktop is unsupported on %s", runtime.GOOS)
	}
}

// writeClaudeDesktopConfig applies provider p to Claude Desktop, mirroring
// cc-switch's apply_provider_to_paths: official providers restore 1p mode,
// everything else installs the AgentNexus 3P gateway profile. All touched
// files are snapshotted first and rolled back on error.
func writeClaudeDesktopConfig(home string, p *core.Provider) error {
	paths, err := claudeDesktopPathsFor(home, p)
	if err != nil {
		return err
	}
	if isClaudeDesktopOfficial(p) {
		return withClaudeDesktopRollback(paths, restoreClaudeDesktopOfficialAtPaths)
	}
	switch p.Meta.ClaudeDesktopMode {
	case "", "direct":
	case "proxy":
		return fmt.Errorf("claude desktop proxy mode is managed by local routing; enable takeover for claude-desktop instead")
	default:
		return fmt.Errorf("claude desktop mode %q is not supported", p.Meta.ClaudeDesktopMode)
	}
	profile, err := claudeDesktopDirectProfile(p)
	if err != nil {
		return err
	}
	return withClaudeDesktopRollback(paths, func(paths claudeDesktopPaths) error {
		return applyClaudeDesktopProfile(paths, profile)
	})
}

// WriteClaudeDesktopProxyProfile points the Claude Desktop 3P profile at the
// local routing gateway (cc-switch proxy mode): every listed route id becomes
// a Desktop-visible model, and the gateway token authenticates the profile.
func WriteClaudeDesktopProxyProfile(p *core.Provider, gatewayBaseURL, gatewayToken string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	paths, err := claudeDesktopPathsFor(home, p)
	if err != nil {
		return err
	}
	models := claudeDesktopRouteModels(p)
	if len(models) == 0 {
		return fmt.Errorf("claude desktop proxy mode requires at least one model route")
	}
	profile := claudeDesktopGatewayProfile(gatewayBaseURL, gatewayToken, "bearer", models)
	return withClaudeDesktopRollback(paths, func(paths claudeDesktopPaths) error {
		return applyClaudeDesktopProfile(paths, profile)
	})
}

// RestoreClaudeDesktopOfficial resets Claude Desktop to 1p mode and removes
// the AgentNexus profile (used when disabling takeover without a provider).
func RestoreClaudeDesktopOfficial(p *core.Provider) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	paths, err := claudeDesktopPathsFor(home, p)
	if err != nil {
		return err
	}
	return withClaudeDesktopRollback(paths, restoreClaudeDesktopOfficialAtPaths)
}

func isClaudeDesktopOfficial(p *core.Provider) bool {
	if p == nil {
		return false
	}
	if p.Meta.ClaudeDesktopMode == "official" {
		return true
	}
	return p.Category == "official" && p.Meta.ClaudeDesktopMode == "" && p.BaseURL == "" && p.APIKeyEnv == ""
}

func claudeDesktopDirectProfile(p *core.Provider) (map[string]any, error) {
	if p.Model != "" && !isClaudeDesktopDirectModel(p.Model) {
		return nil, fmt.Errorf("model %q is not a Claude Desktop direct-mode route", p.Model)
	}
	models := p.Meta.ClaudeDesktopModels
	if len(models) == 0 && p.Model != "" {
		models = []core.ClaudeDesktopModel{{ID: p.Model, Name: p.Model, DisplayName: p.Model}}
	}
	for _, model := range models {
		if model.ID != "" && !isClaudeDesktopDirectModel(model.ID) {
			return nil, fmt.Errorf("model %q is not a Claude Desktop direct-mode route", model.ID)
		}
	}
	apiKey := providerAPIKey(p)
	return claudeDesktopGatewayProfile(p.BaseURL, apiKey, orDefault(p.Meta.ClaudeDesktopAuthMode, "bearer"), models), nil
}

func claudeDesktopRouteModels(p *core.Provider) []core.ClaudeDesktopModel {
	models := p.Meta.ClaudeDesktopModels
	if len(models) == 0 && p.Model != "" {
		models = []core.ClaudeDesktopModel{{ID: claudeDesktopDefaultProxyRoutes[0], Name: claudeDesktopDefaultProxyRoutes[0], DisplayName: p.Model, UpstreamModel: p.Model}}
	}
	return repairClaudeDesktopProxyModels(models)
}

func repairClaudeDesktopProxyModels(models []core.ClaudeDesktopModel) []core.ClaudeDesktopModel {
	reserved := map[string]bool{}
	for _, model := range models {
		id := claudeDesktopModelID(model)
		if isClaudeDesktopDirectModel(id) {
			reserved[id] = true
		}
	}

	out := make([]core.ClaudeDesktopModel, 0, len(models))
	seen := map[string]bool{}
	for _, model := range models {
		originalID := claudeDesktopModelID(model)
		upstream := strings.TrimSpace(model.UpstreamModel)
		if upstream == "" {
			upstream = originalID
		}
		if originalID == "" && upstream == "" {
			continue
		}

		routeID := originalID
		if !isClaudeDesktopDirectModel(routeID) {
			routeID = nextClaudeDesktopSafeRouteID(out, reserved)
		}
		if routeID == "" || seen[routeID] {
			continue
		}
		seen[routeID] = true

		display := strings.TrimSpace(model.DisplayName)
		if display == "" {
			display = originalID
		}
		if display == "" {
			display = upstream
		}

		out = append(out, core.ClaudeDesktopModel{
			ID:            routeID,
			Name:          routeID,
			DisplayName:   display,
			UpstreamModel: upstream,
		})
	}
	return out
}

func nextClaudeDesktopSafeRouteID(existing []core.ClaudeDesktopModel, reserved map[string]bool) string {
	for _, routeID := range claudeDesktopDefaultProxyRoutes {
		if !reserved[routeID] && !claudeDesktopRouteExists(existing, routeID) {
			return routeID
		}
	}
	for i := 2; ; i++ {
		routeID := fmt.Sprintf("%s-r%d", claudeDesktopDefaultProxyRoutes[0], i)
		if !reserved[routeID] && !claudeDesktopRouteExists(existing, routeID) {
			return routeID
		}
	}
}

func claudeDesktopRouteExists(models []core.ClaudeDesktopModel, routeID string) bool {
	for _, model := range models {
		if model.ID == routeID {
			return true
		}
	}
	return false
}

func claudeDesktopModelID(model core.ClaudeDesktopModel) string {
	id := strings.TrimSpace(model.ID)
	if id == "" {
		id = strings.TrimSpace(model.Name)
	}
	return id
}

// claudeDesktopGatewayProfile mirrors cc-switch's build_gateway_profile.
func claudeDesktopGatewayProfile(baseURL, apiKey, authScheme string, models []core.ClaudeDesktopModel) map[string]any {
	profile := map[string]any{
		"coworkEgressAllowedHosts":     []string{"*"},
		"disableDeploymentModeChooser": true,
		"id":                           claudeDesktopProfileID,
		"name":                         claudeDesktopProfileName,
		"isActive":                     true,
		"inferenceGatewayBaseUrl":      baseURL,
		"inferenceGatewayApiKey":       apiKey,
		"inferenceGatewayAuthScheme":   authScheme,
		"inferenceProvider":            "gateway",
	}
	if len(models) > 0 {
		profile["inferenceModels"] = claudeDesktopModelsJSON(models)
	}
	return profile
}

// applyClaudeDesktopProfile mirrors cc-switch apply_provider_to_paths_inner:
// deploymentMode "3p" in BOTH the normal Claude dir and the Claude-3p dir is
// what actually makes the Desktop honor the 3P profile.
func applyClaudeDesktopProfile(paths claudeDesktopPaths, profile map[string]any) error {
	if err := writeClaudeDesktopDeploymentMode(paths.normalConfigPath, "3p"); err != nil {
		return err
	}
	if err := writeClaudeDesktopDeploymentMode(paths.threepConfigPath, "3p"); err != nil {
		return err
	}
	if err := writeJSONObject(paths.profilePath, profile); err != nil {
		return err
	}
	return writeClaudeDesktopMeta(paths.metaPath, claudeDesktopProfileID, claudeDesktopProfileName)
}

// restoreClaudeDesktopOfficialAtPaths mirrors restore_official_at_paths_inner.
func restoreClaudeDesktopOfficialAtPaths(paths claudeDesktopPaths) error {
	if err := writeClaudeDesktopDeploymentMode(paths.normalConfigPath, "1p"); err != nil {
		return err
	}
	if err := writeClaudeDesktopDeploymentMode(paths.threepConfigPath, "1p"); err != nil {
		return err
	}
	if fileExists(paths.profilePath) {
		if err := os.Remove(paths.profilePath); err != nil {
			return err
		}
	}
	return clearClaudeDesktopMeta(paths.metaPath)
}

func writeClaudeDesktopDeploymentMode(path, mode string) error {
	obj := readJSONObject(path)
	obj["deploymentMode"] = mode
	return writeJSONObject(path, obj)
}

// claudeDesktopSnapshot captures file bytes (nil = absent) for rollback.
type claudeDesktopSnapshot struct {
	path    string
	content []byte
	exists  bool
}

func withClaudeDesktopRollback(paths claudeDesktopPaths, op func(claudeDesktopPaths) error) error {
	files := []string{paths.normalConfigPath, paths.threepConfigPath, paths.profilePath, paths.metaPath}
	snapshots := make([]claudeDesktopSnapshot, 0, len(files))
	for _, f := range files {
		snap := claudeDesktopSnapshot{path: f}
		if data, err := os.ReadFile(f); err == nil {
			snap.content = data
			snap.exists = true
		} else if !os.IsNotExist(err) {
			return err
		}
		snapshots = append(snapshots, snap)
	}
	err := op(paths)
	if err == nil {
		return nil
	}
	for _, snap := range snapshots {
		if snap.exists {
			_ = store.AtomicWrite(snap.path, snap.content, 0o600)
		} else {
			_ = os.Remove(snap.path)
		}
	}
	return err
}

// ClaudeDesktopStatus returns whether the AgentNexus Claude-3p profile is active.
func ClaudeDesktopStatus(p *core.Provider) (ClaudeDesktopConfigStatus, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return ClaudeDesktopConfigStatus{}, err
	}
	paths, err := claudeDesktopPathsFor(home, p)
	if err != nil {
		return ClaudeDesktopConfigStatus{}, err
	}
	return claudeDesktopStatusForPaths(paths, "")
}

// DisableClaudeDesktopConfig removes the AgentNexus Claude-3p profile and
// restores 1p deployment mode, backing up touched files first.
func DisableClaudeDesktopConfig(p *core.Provider) (ClaudeDesktopConfigStatus, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return ClaudeDesktopConfigStatus{}, err
	}
	paths, err := claudeDesktopPathsFor(home, p)
	if err != nil {
		return ClaudeDesktopConfigStatus{}, err
	}
	backupPath, err := disableClaudeDesktopProfile(paths)
	if err != nil {
		return ClaudeDesktopConfigStatus{}, err
	}
	return claudeDesktopStatusForPaths(paths, backupPath)
}

func isClaudeDesktopDirectModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if strings.Contains(model, "[1m]") {
		return false
	}
	tail, ok := strings.CutPrefix(model, "anthropic/claude-")
	if !ok {
		tail, ok = strings.CutPrefix(model, "claude-")
	}
	if !ok {
		return false
	}
	for _, prefix := range []string{"sonnet-", "opus-", "haiku-", "fable-"} {
		if rest, ok := strings.CutPrefix(tail, prefix); ok && rest != "" {
			return true
		}
	}
	return false
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

func writeClaudeDesktopMeta(path, id, name string) error {
	meta := readJSONObject(path)
	meta["appliedId"] = id
	meta["entries"] = appendClaudeDesktopEntry(meta["entries"], id, name)
	return writeJSONObject(path, meta)
}

// clearClaudeDesktopMeta removes our entry and hands appliedId to the next
// remaining entry (or drops it), mirroring cc-switch write_meta(None).
func clearClaudeDesktopMeta(path string) error {
	meta := readJSONObject(path)
	entries := filterClaudeDesktopEntries(meta["entries"], claudeDesktopProfileID)
	meta["entries"] = entries
	if stringValue(meta["appliedId"]) == claudeDesktopProfileID {
		next := ""
		for _, item := range entries {
			if entry, ok := item.(map[string]any); ok {
				if id := stringValue(entry["id"]); id != "" {
					next = id
					break
				}
			}
		}
		if next != "" {
			meta["appliedId"] = next
		} else {
			delete(meta, "appliedId")
		}
	}
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

func disableClaudeDesktopProfile(paths claudeDesktopPaths) (string, error) {
	backupDir := filepath.Join(paths.baseDir, "configLibrary.backup."+time.Now().Format("20060102-150405"))
	changed := false

	meta := readJSONObject(paths.metaPath)
	if len(meta) > 0 {
		if err := backupClaudeDesktopFile(paths.metaPath, backupDir); err != nil {
			return "", err
		}
		if err := clearClaudeDesktopMeta(paths.metaPath); err != nil {
			return "", err
		}
		changed = true
	}

	// Legacy index keys written by older AgentNexus builds.
	index := readJSONObject(paths.threepConfigPath)
	if len(index) > 0 && stringValue(index["activeProfileId"]) == claudeDesktopProfileID {
		if err := backupClaudeDesktopFile(paths.threepConfigPath, backupDir); err != nil {
			return "", err
		}
		delete(index, "activeProfileId")
		delete(index, "profileName")
		delete(index, "profilePath")
		if err := writeJSONObject(paths.threepConfigPath, index); err != nil {
			return "", err
		}
		changed = true
	}

	if fileExists(paths.profilePath) {
		if err := os.MkdirAll(backupDir, 0o755); err != nil {
			return "", err
		}
		if err := os.Rename(paths.profilePath, filepath.Join(backupDir, filepath.Base(paths.profilePath)+".moved")); err != nil {
			return "", err
		}
		changed = true
	}

	// Restore 1p deployment mode so Desktop returns to the official login.
	if fileExists(paths.normalConfigPath) || fileExists(paths.threepConfigPath) || changed {
		if err := writeClaudeDesktopDeploymentMode(paths.normalConfigPath, "1p"); err != nil {
			return "", err
		}
		if err := writeClaudeDesktopDeploymentMode(paths.threepConfigPath, "1p"); err != nil {
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

func claudeDesktopStatusForPaths(paths claudeDesktopPaths, backupPath string) (ClaudeDesktopConfigStatus, error) {
	profile := readJSONObject(paths.profilePath)
	meta := readJSONObject(paths.metaPath)
	index := readJSONObject(paths.threepConfigPath)

	activeID := stringValue(meta["appliedId"])
	activeName := claudeDesktopEntryName(meta["entries"], activeID)
	if activeID == "" {
		activeID = stringValue(index["activeProfileId"])
		activeName = stringValue(index["profileName"])
	}
	if activeName == "" && activeID == claudeDesktopProfileID {
		activeName = stringValue(profile["name"])
	}
	deploymentMode := stringValue(index["deploymentMode"])
	enabled := activeID == claudeDesktopProfileID && deploymentMode != "1p"
	configured := len(profile) > 0
	status := ClaudeDesktopConfigStatus{
		Enabled:           enabled,
		Configured:        configured,
		ConfigDir:         paths.baseDir,
		ProfilePath:       paths.profilePath,
		ActiveProfileID:   activeID,
		ActiveProfileName: activeName,
		DeploymentMode:    deploymentMode,
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
