package native

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type marketplaceEntry struct {
	Name            string
	Root            string
	InstallLocation string
}

type pluginEntry struct {
	Name        string
	ID          string
	Marketplace string
	Version     string
	Path        string
	Installed   bool
	Enabled     bool
}

type discovery struct {
	CLIPath      string
	CLIError     error
	ListError    error
	Marketplaces []marketplaceEntry
	Plugins      []pluginEntry
	Findings     []Finding
}

func (m *Manager) discover(ctx context.Context, spec hostSpec) discovery {
	result := discovery{}
	path, err := m.runner.LookPath(spec.binary)
	if err != nil {
		result.CLIError = err
		return result
	}
	result.CLIPath = path
	if spec.host == HostCodex {
		if info, statErr := os.Stat(filepath.Join(m.home, ".codex")); os.IsNotExist(statErr) {
			// A never-initialized CODEX_HOME cannot contain a marketplace or
			// installed plugin. Keep Preview read-only; Install creates the empty
			// directory before asking the native CLI to mutate its own state.
			result.Findings = detectThirdPartyOwners(m.home, spec.host)
			return result
		} else if statErr != nil || !info.IsDir() {
			result.ListError = fmt.Errorf("Codex home is not a readable directory")
			return result
		}
	}

	marketplaceArgs := []string{"plugin", "marketplace", "list", "--json"}
	marketplaceOutput, err := m.runner.Run(ctx, Command{Name: spec.binary, Args: marketplaceArgs, Env: m.env()})
	if err != nil {
		result.ListError = fmt.Errorf("list %s marketplaces: %w", spec.host, err)
	} else {
		result.Marketplaces, err = parseMarketplaces([]byte(marketplaceOutput.Stdout))
		if err != nil {
			result.ListError = fmt.Errorf("parse %s marketplaces: %w", spec.host, err)
		}
	}

	pluginArgs := []string{"plugin", "list", "--available", "--json"}
	pluginOutput, pluginErr := m.runner.Run(ctx, Command{Name: spec.binary, Args: pluginArgs, Env: m.env()})
	if pluginErr != nil {
		if result.ListError == nil {
			result.ListError = fmt.Errorf("list %s plugins: %w", spec.host, pluginErr)
		}
	} else {
		plugins, parseErr := parsePlugins([]byte(pluginOutput.Stdout))
		if parseErr != nil {
			if result.ListError == nil {
				result.ListError = fmt.Errorf("parse %s plugins: %w", spec.host, parseErr)
			}
		} else {
			result.Plugins = plugins
		}
	}
	result.Findings = detectThirdPartyOwners(m.home, spec.host)
	return result
}

func parseMarketplaces(raw []byte) ([]marketplaceEntry, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	items := topLevelItems(decoded, "marketplaces")
	entries := make([]marketplaceEntry, 0, len(items))
	for _, item := range items {
		name := stringField(item, "name", "marketplaceName")
		if name == "" {
			continue
		}
		root := stringField(item, "root", "path", "sourcePath")
		if root == "" {
			if source, ok := item["marketplaceSource"].(map[string]any); ok {
				root = stringField(source, "source", "path")
			}
		}
		if root == "" {
			if source, ok := item["source"].(map[string]any); ok {
				root = stringField(source, "path", "source")
			}
		}
		entries = append(entries, marketplaceEntry{
			Name:            name,
			Root:            root,
			InstallLocation: stringField(item, "installLocation", "install_path"),
		})
	}
	return entries, nil
}

func parsePlugins(raw []byte) ([]pluginEntry, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	items := topLevelItems(decoded, "installed", "plugins")
	entries := make([]pluginEntry, 0, len(items))
	for _, item := range items {
		id := stringField(item, "pluginId", "id", "plugin_id")
		name := stringField(item, "name", "pluginName")
		marketplace := stringField(item, "marketplaceName", "marketplace", "marketplace_name")
		if id != "" {
			parts := strings.SplitN(id, "@", 2)
			if name == "" {
				name = parts[0]
			}
			if marketplace == "" && len(parts) == 2 {
				marketplace = parts[1]
			}
		}
		if name == "" {
			continue
		}
		installed, hasInstalled := boolField(item, "installed")
		if !hasInstalled {
			installed = true
		}
		enabled, hasEnabled := boolField(item, "enabled")
		if !hasEnabled {
			enabled = installed
		}
		path := stringField(item, "installPath", "path", "installLocation")
		if path == "" {
			if source, ok := item["source"].(map[string]any); ok {
				path = stringField(source, "path", "source")
			}
		}
		entries = append(entries, pluginEntry{
			Name:        name,
			ID:          id,
			Marketplace: marketplace,
			Version:     stringField(item, "version"),
			Path:        path,
			Installed:   installed,
			Enabled:     enabled,
		})
	}
	return entries, nil
}

func topLevelItems(decoded any, keys ...string) []map[string]any {
	if list, ok := decoded.([]any); ok {
		return mapsFromList(list)
	}
	root, ok := decoded.(map[string]any)
	if !ok {
		return nil
	}
	var result []map[string]any
	for _, key := range keys {
		if list, ok := root[key].([]any); ok {
			result = append(result, mapsFromList(list)...)
		}
	}
	return result
}

func mapsFromList(list []any) []map[string]any {
	result := make([]map[string]any, 0, len(list))
	for _, value := range list {
		if item, ok := value.(map[string]any); ok {
			result = append(result, item)
		}
	}
	return result
}

func stringField(item map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := item[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func boolField(item map[string]any, key string) (bool, bool) {
	value, ok := item[key].(bool)
	return value, ok
}

func (d discovery) marketplace(name string) *marketplaceEntry {
	for i := range d.Marketplaces {
		if d.Marketplaces[i].Name == name {
			return &d.Marketplaces[i]
		}
	}
	return nil
}

func (d discovery) installedPlugin(name string) *pluginEntry {
	for i := range d.Plugins {
		if d.Plugins[i].Name == name && d.Plugins[i].Installed {
			return &d.Plugins[i]
		}
	}
	return nil
}

func detectThirdPartyOwners(home string, host Host) []Finding {
	var findings []Finding
	fluxManifest := filepath.Join(home, ".flux", "hooks", string(host)+"-manifest.json")
	if host == HostClaude {
		fluxManifest = filepath.Join(home, ".flux", "hooks", "claude-manifest.json")
	}
	if exists(fluxManifest) {
		findings = append(findings, Finding{
			Code: "third_party_owner", Severity: SeverityInfo, Owner: "flux-island", Path: fluxManifest,
			Message: "Flux Island also manages hooks for this host; AgentNexus will use an additive plugin and will not edit Flux resources.",
		})
	}
	ccSwitchPaths := []string{
		filepath.Join(home, ".cc-switch", "cc-switch.db"),
		filepath.Join(home, ".cc-switch", "config.json"),
	}
	for _, path := range ccSwitchPaths {
		if exists(path) {
			findings = append(findings, Finding{
				Code: "third_party_owner", Severity: SeverityInfo, Owner: "cc-switch", Path: path,
				Message: "CC Switch state was detected; AgentNexus will not edit its database or common configuration.",
			})
			break
		}
	}

	for _, path := range sharedConfigPaths(home, host) {
		raw, err := readSmallFile(path)
		if err != nil || len(raw) == 0 {
			continue
		}
		text := strings.ToLower(string(raw))
		if strings.Contains(text, "flux-hooks") || strings.Contains(text, "flux island.app") {
			findings = append(findings, Finding{
				Code: "shared_config_owner", Severity: SeverityInfo, Owner: "flux-island", Path: path,
				Message: "Flux Island handlers are present in a shared host config and will be preserved.",
			})
		}
		if strings.Contains(text, "cc-switch") {
			findings = append(findings, Finding{
				Code: "shared_config_owner", Severity: SeverityInfo, Owner: "cc-switch", Path: path,
				Message: "CC Switch markers are present in a shared host config and will be preserved.",
			})
		}
		if strings.Contains(text, ".agentnexus/bin/agentnexus-hook") {
			findings = append(findings, Finding{
				Code: "same_handler_unowned", Severity: SeverityError, Owner: "unknown", Path: path, Blocking: true,
				Message: "An AgentNexus hook handler already exists in shared configuration but is not owned by this plugin installation; refusing to create a duplicate.",
			})
		}
		if containsLoopback(text) && !strings.Contains(text, "agentnexus-proxy-managed") {
			findings = append(findings, Finding{
				Code: "unknown_loopback_route", Severity: SeverityWarning, Owner: "unknown", Path: path,
				Message: "An existing loopback route is present. The observer plugin is additive, but AgentNexus will not claim or rewrite this route.",
			})
		}
	}
	return dedupeFindings(findings)
}

func sharedConfigPaths(home string, host Host) []string {
	if host == HostClaude {
		return []string{
			filepath.Join(home, ".claude", "settings.json"),
			filepath.Join(home, ".claude", "plugins", "known_marketplaces.json"),
			filepath.Join(home, ".claude", "plugins", "installed_plugins.json"),
			filepath.Join(home, ".claude", "plugins", "config.json"),
		}
	}
	return []string{
		filepath.Join(home, ".codex", "config.toml"),
		filepath.Join(home, ".codex", "hooks.json"),
	}
}

func readSmallFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 4<<20))
}

func containsLoopback(value string) bool {
	return strings.Contains(value, "127.0.0.1") || strings.Contains(value, "localhost") || strings.Contains(value, "[::1]")
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func dedupeFindings(findings []Finding) []Finding {
	seen := map[string]bool{}
	result := make([]Finding, 0, len(findings))
	for _, finding := range findings {
		key := finding.Code + "\x00" + finding.Owner + "\x00" + finding.Path
		if !seen[key] {
			seen[key] = true
			result = append(result, finding)
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		if severityRank(result[i].Severity) != severityRank(result[j].Severity) {
			return severityRank(result[i].Severity) > severityRank(result[j].Severity)
		}
		return result[i].Code < result[j].Code
	})
	return result
}

func severityRank(severity Severity) int {
	switch severity {
	case SeverityError:
		return 3
	case SeverityWarning:
		return 2
	default:
		return 1
	}
}
