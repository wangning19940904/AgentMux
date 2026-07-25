package native

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Preview returns an exact, non-mutating native CLI plan and all conflict
// findings. A blocking finding means Install/Repair will refuse to write.
func (m *Manager) Preview(ctx context.Context, host Host) (Preview, error) {
	spec, err := m.spec(host)
	if err != nil {
		return Preview{}, err
	}
	return m.preview(ctx, spec)
}

func (m *Manager) preview(ctx context.Context, spec hostSpec) (Preview, error) {
	result := Preview{Host: spec.host, Status: StatusNotInstalled, Actions: []Action{}, Findings: []Finding{}}
	assets, err := m.inspectAssets(spec)
	if err != nil {
		result.Status = StatusUnavailable
		result.Findings = append(result.Findings, Finding{
			Code: "invalid_assets", Severity: SeverityError, Blocking: true, Path: spec.root,
			Message: err.Error(),
		})
		result.Blocked = true
		return result, nil
	}
	result.PluginSHA256 = assets.PluginSHA
	result.MarketplaceSHA256 = assets.MarketplaceSHA
	result.HandlerFingerprint = assets.HandlerFingerprint

	record, _, stateErr := m.loadRecord(spec.host)
	if stateErr != nil {
		result.Status = StatusConflict
		result.Findings = append(result.Findings, Finding{
			Code: "ownership_manifest_invalid", Severity: SeverityError, Blocking: true, Path: m.statePath(spec.host),
			Message: stateErr.Error(),
		})
		result.Blocked = true
		return result, nil
	}
	if record != nil {
		result.InstallID = record.InstallID
	}

	discovered := m.discover(ctx, spec)
	result.Findings = append(result.Findings, discovered.Findings...)
	if discovered.CLIError != nil {
		result.Status = StatusUnavailable
		result.Findings = append(result.Findings, Finding{
			Code: "native_cli_missing", Severity: SeverityError, Blocking: true,
			Message: fmt.Sprintf("%s CLI is unavailable: %v", spec.binary, discovered.CLIError),
		})
	}
	if discovered.ListError != nil {
		result.Status = StatusUnavailable
		result.Findings = append(result.Findings, Finding{
			Code: "native_state_unreadable", Severity: SeverityError, Blocking: true,
			Message: discovered.ListError.Error(),
		})
	}

	marketplace := discovered.marketplace(MarketplaceName)
	plugin := discovered.installedPlugin(PluginID)
	helperPath := m.helperTarget()
	helperHash, helperErr := fileHash(helperPath)
	if helperErr != nil {
		result.Findings = append(result.Findings, Finding{
			Code: "helper_unsafe", Severity: SeverityError, Blocking: true, Path: helperPath,
			Message: helperErr.Error(),
		})
	}

	if record == nil {
		if marketplace != nil {
			result.Findings = append(result.Findings, Finding{
				Code: "marketplace_name_conflict", Severity: SeverityError, Blocking: true,
				Message: fmt.Sprintf("marketplace %q already exists without an AgentMux ownership record", MarketplaceName),
			})
		}
		if plugin != nil {
			result.Findings = append(result.Findings, Finding{
				Code: "plugin_name_conflict", Severity: SeverityError, Blocking: true,
				Message: fmt.Sprintf("plugin %q is already installed without an AgentMux ownership record", PluginID),
			})
		}
		if helperHash != "" {
			result.Findings = append(result.Findings, Finding{
				Code: "helper_path_conflict", Severity: SeverityError, Blocking: true, Path: helperPath,
				Message: "the AgentMux hook helper path already exists without an ownership record",
			})
		}
	} else {
		if record.Host != spec.host || record.PluginID != PluginID || record.Marketplace != MarketplaceName {
			result.Findings = append(result.Findings, Finding{
				Code: "ownership_identity_mismatch", Severity: SeverityError, Blocking: true, Path: m.statePath(spec.host),
				Message: "the ownership manifest identity does not match this integration",
			})
		}
		if marketplace != nil && !marketplaceMatchesRecord(*marketplace, record, spec, assets) {
			result.Findings = append(result.Findings, Finding{
				Code: "marketplace_drift", Severity: SeverityError, Blocking: true,
				Message: "the native marketplace with AgentMux's name now points to an unrecognized resource",
			})
		}
		if plugin != nil {
			if plugin.Marketplace != "" && plugin.Marketplace != MarketplaceName {
				result.Findings = append(result.Findings, Finding{
					Code: "plugin_owner_drift", Severity: SeverityError, Blocking: true,
					Message: fmt.Sprintf("plugin %q is now owned by marketplace %q", PluginID, plugin.Marketplace),
				})
			} else if matches, matchErr := pluginFingerprintMatches(*plugin, record, assets); matchErr != nil {
				result.Findings = append(result.Findings, Finding{
					Code: "plugin_fingerprint_unreadable", Severity: SeverityError, Blocking: true, Path: plugin.Path,
					Message: matchErr.Error(),
				})
			} else if !matches {
				result.Findings = append(result.Findings, Finding{
					Code: "plugin_fingerprint_drift", Severity: SeverityError, Blocking: true, Path: plugin.Path,
					Message: "the installed plugin no longer matches AgentMux's recorded or current fingerprint",
				})
			}
		}
		helperResource := findResource(record, "hook-helper")
		if helperResource == nil {
			result.Findings = append(result.Findings, Finding{
				Code: "helper_ownership_missing", Severity: SeverityError, Blocking: true, Path: helperPath,
				Message: "the ownership manifest does not identify the hook helper",
			})
		} else if helperHash != "" && helperHash != helperResource.AfterHash {
			result.Findings = append(result.Findings, Finding{
				Code: "helper_drift", Severity: SeverityError, Blocking: true, Path: helperPath,
				Message: "the hook helper was modified by another process; AgentMux will not overwrite it",
			})
		}
	}

	needsHelper := helperHash == ""
	helperSource := m.resolvedHelperSource()
	var helperSourceHash string
	if helperSource != "" {
		helperSourceHash, err = fileHash(helperSource)
		if err != nil {
			result.Findings = append(result.Findings, Finding{
				Code: "helper_source_invalid", Severity: SeverityError, Blocking: true, Path: helperSource,
				Message: err.Error(),
			})
		}
	}
	if record != nil {
		if helperResource := findResource(record, "hook-helper"); helperResource != nil && helperSourceHash != "" && helperSourceHash != helperResource.AfterHash {
			needsHelper = true
		}
	}
	if needsHelper && helperSourceHash == "" {
		result.Findings = append(result.Findings, Finding{
			Code: "helper_source_missing", Severity: SeverityError, Blocking: true,
			Message: "the companion agentmux-hook binary was not found; provide Options.HelperSource or package it next to AgentMux",
		})
	}

	if marketplace == nil {
		result.Actions = append(result.Actions, m.marketplaceAddAction(spec))
	}
	needsPlugin := plugin == nil
	if record != nil && (record.PluginSHA256 != assets.PluginSHA || record.MarketplaceSHA256 != assets.MarketplaceSHA || record.HandlerFingerprint != assets.HandlerFingerprint) {
		needsPlugin = true
		result.Findings = append(result.Findings, Finding{
			Code: "asset_update_available", Severity: SeverityInfo,
			Message: fmt.Sprintf("observer assets changed from version %s to %s and can be repaired in place", record.Version, assets.Version),
		})
	}
	if needsPlugin {
		kind := "install_plugin"
		reason := "install the additive observer plugin"
		if plugin != nil {
			kind = "repair_plugin"
			reason = "refresh the owned plugin from updated, fingerprinted assets"
		}
		result.Actions = append(result.Actions, m.pluginInstallAction(spec, kind, reason))
	}
	if needsHelper {
		kind := "install_helper"
		if helperHash != "" {
			kind = "repair_helper"
		}
		result.Actions = append(result.Actions, Action{
			Kind: kind, Target: helperPath, Reason: "install the bounded fail-open Unix socket relay",
		})
	}

	result.Findings = dedupeFindings(result.Findings)
	for _, finding := range result.Findings {
		if finding.Blocking {
			result.Blocked = true
		}
	}
	if result.Blocked {
		if result.Status != StatusUnavailable {
			result.Status = StatusConflict
		}
		return result, nil
	}
	if record == nil || marketplace == nil || plugin == nil || needsHelper || needsPlugin {
		result.Status = StatusNotInstalled
	} else if spec.host == HostCodex {
		result.Status = StatusPendingTrust
		result.Actions = append(result.Actions, Action{
			Kind: "review_trust", Target: "codex:/hooks",
			Reason: "Codex requires the user to review hooks from non-managed plugins; AgentMux never edits hooks.state",
		})
	} else {
		result.Status = StatusHealthy
	}
	return result, nil
}

// Install installs a new integration or idempotently repairs an owned one.
func (m *Manager) Install(ctx context.Context, host Host) (Result, error) {
	return m.mutate(ctx, host, false)
}

// Repair revalidates fingerprints and refreshes only already-owned resources.
// If no ownership record exists it follows the same conflict-safe path as Install.
func (m *Manager) Repair(ctx context.Context, host Host) (Result, error) {
	return m.mutate(ctx, host, true)
}

func (m *Manager) mutate(ctx context.Context, host Host, repair bool) (Result, error) {
	spec, err := m.spec(host)
	if err != nil {
		return Result{}, err
	}
	lock, err := acquireFileLock(ctx, m.lockPath())
	if err != nil {
		return Result{}, err
	}
	defer lock.release()
	if host == HostCodex {
		// Codex rejects an explicit CODEX_HOME when the directory is absent.
		// Creating the host's config directory is the only prerequisite mutation;
		// all actual marketplace/plugin configuration still goes through its CLI.
		if err := os.MkdirAll(filepath.Join(m.home, ".codex"), 0o700); err != nil {
			return Result{}, err
		}
	}
	// All state and native CLI discovery is intentionally repeated only after
	// taking the lock. This is the re-read phase of lock -> CAS -> verify.
	preview, err := m.preview(ctx, spec)
	if err != nil {
		return Result{}, err
	}
	if preview.Blocked {
		return Result{Host: host, Actions: preview.Actions, Findings: preview.Findings}, &OperationError{Kind: ErrConflict, Host: host, Findings: preview.Findings}
	}
	assets, err := m.inspectAssets(spec)
	if err != nil {
		return Result{}, err
	}
	record, stateHash, err := m.loadRecord(host)
	if err != nil {
		return Result{}, err
	}
	discovered := m.discover(ctx, spec)
	marketplace := discovered.marketplace(MarketplaceName)
	plugin := discovered.installedPlugin(PluginID)

	if record != nil && (!repair && preview.Status != StatusNotInstalled || repair && !hasMutationAction(preview.Actions)) {
		return Result{Host: host, Changed: false, Record: record, Actions: preview.Actions, Findings: preview.Findings}, nil
	}

	beforeShared := snapshotSharedFiles(m.home, host)
	var actions []Action
	marketplaceAdded := false
	pluginAdded := false
	if marketplace == nil {
		action := m.marketplaceAddAction(spec)
		if err := m.runAction(ctx, spec, action); err != nil {
			return Result{Host: host, Actions: actions, Findings: preview.Findings}, err
		}
		actions = append(actions, action)
		marketplaceAdded = true
	}

	needsPlugin := plugin == nil || record == nil
	if record != nil && (record.PluginSHA256 != assets.PluginSHA || record.MarketplaceSHA256 != assets.MarketplaceSHA || record.HandlerFingerprint != assets.HandlerFingerprint) {
		needsPlugin = true
		if spec.host == HostClaude && marketplace != nil {
			update := Action{
				Kind: "update_marketplace", Target: MarketplaceName,
				Command: []string{"claude", "plugin", "marketplace", "update", MarketplaceName},
				Reason:  "refresh the owned local marketplace before repairing its plugin",
			}
			if err := m.runAction(ctx, spec, update); err != nil {
				return Result{Host: host, Actions: actions, Findings: preview.Findings}, err
			}
			actions = append(actions, update)
		}
	}
	if needsPlugin {
		action := m.pluginInstallAction(spec, "install_plugin", "install or refresh the owned additive observer plugin")
		if spec.host == HostClaude && plugin != nil {
			action = Action{
				Kind: "repair_plugin", Target: PluginID + "@" + MarketplaceName,
				Command: []string{"claude", "plugin", "update", PluginID + "@" + MarketplaceName},
				Reason:  "refresh the already-owned Claude plugin without replacing shared hooks",
			}
		}
		if err := m.runAction(ctx, spec, action); err != nil {
			if marketplaceAdded {
				_ = m.runAction(context.Background(), spec, m.marketplaceRemoveAction(spec))
			}
			return Result{Host: host, Actions: actions, Findings: preview.Findings}, err
		}
		actions = append(actions, action)
		pluginAdded = plugin == nil
	}

	helperBefore, err := fileHash(m.helperTarget())
	if err != nil {
		if pluginAdded {
			_ = m.runAction(context.Background(), spec, m.pluginRemoveAction(spec))
		}
		if marketplaceAdded {
			_ = m.runAction(context.Background(), spec, m.marketplaceRemoveAction(spec))
		}
		return Result{}, err
	}
	var helperBeforeData []byte
	if helperBefore != "" {
		helperBeforeData, err = os.ReadFile(m.helperTarget())
		if err != nil {
			if pluginAdded {
				_ = m.runAction(context.Background(), spec, m.pluginRemoveAction(spec))
			}
			if marketplaceAdded {
				_ = m.runAction(context.Background(), spec, m.marketplaceRemoveAction(spec))
			}
			return Result{}, err
		}
	}
	helperAfter := helperBefore
	helperChanged := false
	rollbackHelper := func() {
		if !helperChanged {
			return
		}
		if helperBefore == "" {
			_ = removeFileCAS(m.helperTarget(), helperAfter)
			return
		}
		_, _ = atomicWriteCAS(m.helperTarget(), helperBeforeData, 0o700, helperAfter)
	}
	rollbackNewNativeResources := func() {
		if pluginAdded {
			_ = m.runAction(context.Background(), spec, m.pluginRemoveAction(spec))
		}
		if marketplaceAdded {
			_ = m.runAction(context.Background(), spec, m.marketplaceRemoveAction(spec))
		}
	}
	helperSource := m.resolvedHelperSource()
	var helperData []byte
	desiredHelperHash := helperBefore
	if helperSource != "" {
		helperData, err = os.ReadFile(helperSource)
		if err != nil {
			rollbackNewNativeResources()
			return Result{}, fmt.Errorf("read hook helper: %w", err)
		}
		desiredHelperHash = hashBytes(helperData)
	}
	if helperBefore != desiredHelperHash && len(helperData) > 0 {
		expected := helperBefore
		if record != nil {
			owned := findResource(record, "hook-helper")
			if helperBefore != "" && (owned == nil || helperBefore != owned.AfterHash) {
				rollbackNewNativeResources()
				return Result{}, &OperationError{Kind: ErrDrift, Host: host, Findings: []Finding{{
					Code: "helper_drift", Severity: SeverityError, Blocking: true, Path: m.helperTarget(),
					Message: "the hook helper changed after preview; refusing to overwrite it",
				}}}
			}
		}
		helperAfter, err = atomicWriteCAS(m.helperTarget(), helperData, 0o700, expected)
		if err != nil {
			rollbackNewNativeResources()
			return Result{}, err
		}
		helperChanged = true
		actions = append(actions, Action{Kind: "install_helper", Target: m.helperTarget(), Reason: "install the bounded fail-open Unix socket relay"})
	}

	verified := m.discover(ctx, spec)
	if verified.ListError != nil || verified.marketplace(MarketplaceName) == nil || verified.installedPlugin(PluginID) == nil {
		rollbackHelper()
		rollbackNewNativeResources()
		return Result{}, fmt.Errorf("native CLI verification failed after install")
	}
	afterShared := snapshotSharedFiles(m.home, host)
	shared := diffSharedFiles(beforeShared, afterShared)
	now := m.now().UTC()
	if record == nil {
		installID, idErr := randomInstallID(m.random)
		if idErr != nil {
			rollbackHelper()
			rollbackNewNativeResources()
			return Result{}, idErr
		}
		record = &InstallRecord{
			SchemaVersion: 1, InstallID: installID, Host: host, Scope: "user",
			PluginID: PluginID, Marketplace: MarketplaceName, MarketplaceRoot: spec.root,
			InstalledAt: now,
		}
	}
	record.Version = assets.Version
	record.PluginSHA256 = assets.PluginSHA
	record.MarketplaceSHA256 = assets.MarketplaceSHA
	record.HandlerFingerprint = assets.HandlerFingerprint
	record.Status = statusForHost(host)
	record.UpdatedAt = now
	record.Resources = []ResourceOwnership{
		{
			Kind: "native-marketplace", TargetPath: "marketplace://" + string(host) + "/" + MarketplaceName,
			BeforeHash: hashIfPresent(marketplace != nil, assets.MarketplaceSHA), AfterHash: assets.MarketplaceSHA,
		},
		{
			Kind: "native-plugin", TargetPath: "plugin://" + PluginID + "@" + MarketplaceName,
			BeforeHash: hashIfPresent(plugin != nil, assets.PluginSHA), AfterHash: assets.PluginSHA,
			HandlerFingerprint: assets.HandlerFingerprint,
		},
		{
			Kind: "hook-helper", TargetPath: m.helperTarget(), BeforeHash: helperBefore, AfterHash: helperAfter,
		},
	}
	record.SharedFiles = mergeFileObservations(record.SharedFiles, shared)
	if err := m.writeRecord(record, stateHash); err != nil {
		rollbackHelper()
		rollbackNewNativeResources()
		return Result{}, err
	}
	return Result{Host: host, Changed: len(actions) > 0 || stateHash == "", Record: record, Actions: actions, Findings: preview.Findings}, nil
}

// Uninstall removes only exact resources described by the ownership record.
// Any modified resource is preserved and returned as drift instead of restored.
func (m *Manager) Uninstall(ctx context.Context, host Host) (Result, error) {
	spec, err := m.spec(host)
	if err != nil {
		return Result{}, err
	}
	lock, err := acquireFileLock(ctx, m.lockPath())
	if err != nil {
		return Result{}, err
	}
	defer lock.release()
	record, stateHash, err := m.loadRecord(host)
	if err != nil {
		return Result{}, err
	}
	if record == nil {
		return Result{Host: host}, &OperationError{Kind: ErrConflict, Host: host, Findings: []Finding{{
			Code: "ownership_missing", Severity: SeverityError, Blocking: true,
			Message: "no AgentMux ownership record exists; refusing to remove a possibly third-party plugin",
		}}}
	}
	assets, err := m.inspectAssets(spec)
	if err != nil {
		return Result{}, err
	}
	discovered := m.discover(ctx, spec)
	if discovered.CLIError != nil || discovered.ListError != nil {
		return Result{}, fmt.Errorf("cannot verify native ownership before uninstall: %v %v", discovered.CLIError, discovered.ListError)
	}
	var actions []Action
	var findings []Finding
	var preserved []string
	plugin := discovered.installedPlugin(PluginID)
	pluginPreserved := false
	if plugin != nil {
		matches, matchErr := pluginFingerprintMatches(*plugin, record, assets)
		if matchErr != nil || !matches || (plugin.Marketplace != "" && plugin.Marketplace != MarketplaceName) {
			pluginPreserved = true
			preserved = append(preserved, "plugin://"+PluginID+"@"+plugin.Marketplace)
			findings = append(findings, Finding{
				Code: "plugin_drift_preserved", Severity: SeverityWarning, Path: plugin.Path,
				Message: "the installed plugin fingerprint changed; it was preserved rather than removed",
			})
		} else {
			action := m.pluginRemoveAction(spec)
			if err := m.runAction(ctx, spec, action); err != nil {
				return Result{}, err
			}
			actions = append(actions, action)
		}
	}
	marketplace := discovered.marketplace(MarketplaceName)
	if marketplace != nil {
		if pluginPreserved || !marketplaceMatchesRecord(*marketplace, record, spec, assets) {
			preserved = append(preserved, "marketplace://"+string(host)+"/"+MarketplaceName)
			findings = append(findings, Finding{
				Code: "marketplace_drift_preserved", Severity: SeverityWarning,
				Message: "the marketplace fingerprint or source changed; it was preserved rather than removed",
			})
		} else {
			action := m.marketplaceRemoveAction(spec)
			if err := m.runAction(ctx, spec, action); err != nil {
				return Result{}, err
			}
			actions = append(actions, action)
		}
	}
	helperResource := findResource(record, "hook-helper")
	if helperResource != nil {
		current, hashErr := fileHash(helperResource.TargetPath)
		if hashErr != nil || (current != "" && current != helperResource.AfterHash) {
			preserved = append(preserved, helperResource.TargetPath)
			findings = append(findings, Finding{
				Code: "helper_drift_preserved", Severity: SeverityWarning, Path: helperResource.TargetPath,
				Message: "the hook helper changed; it was preserved rather than removed",
			})
		} else if current != "" {
			if err := removeFileCAS(helperResource.TargetPath, helperResource.AfterHash); err != nil {
				return Result{}, err
			}
			actions = append(actions, Action{Kind: "remove_helper", Target: helperResource.TargetPath, Reason: "remove the exact owned helper fingerprint"})
		}
	}
	if len(preserved) == 0 {
		if err := m.removeRecord(host, stateHash); err != nil {
			return Result{}, err
		}
		return Result{Host: host, Changed: len(actions) > 0, Actions: actions, Findings: findings}, nil
	}
	record.Status = StatusDrift
	record.UpdatedAt = m.now().UTC()
	if err := m.writeRecord(record, stateHash); err != nil {
		return Result{}, err
	}
	return Result{Host: host, Changed: len(actions) > 0, Record: record, Actions: actions, Findings: findings, Preserved: preserved}, &OperationError{Kind: ErrDrift, Host: host, Findings: findings}
}

func (m *Manager) loadRecord(host Host) (*InstallRecord, string, error) {
	path := m.statePath(host)
	hash, err := fileHash(path)
	if err != nil || hash == "" {
		return nil, hash, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, hash, err
	}
	var record InstallRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, hash, err
	}
	if record.SchemaVersion != 1 || record.InstallID == "" || record.Host != host {
		return nil, hash, fmt.Errorf("unsupported or mismatched ownership manifest")
	}
	return &record, hash, nil
}

func (m *Manager) writeRecord(record *InstallRecord, expectedHash string) error {
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	_, err = atomicWriteCAS(m.statePath(record.Host), raw, 0o600, expectedHash)
	return err
}

func (m *Manager) removeRecord(host Host, expectedHash string) error {
	return removeFileCAS(m.statePath(host), expectedHash)
}

func statusForHost(host Host) Status {
	if host == HostCodex {
		return StatusPendingTrust
	}
	return StatusHealthy
}

func findResource(record *InstallRecord, kind string) *ResourceOwnership {
	if record == nil {
		return nil
	}
	for i := range record.Resources {
		if record.Resources[i].Kind == kind {
			return &record.Resources[i]
		}
	}
	return nil
}

func hashIfPresent(present bool, hash string) string {
	if present {
		return hash
	}
	return ""
}

func snapshotSharedFiles(home string, host Host) map[string]string {
	result := map[string]string{}
	for _, path := range sharedConfigPaths(home, host) {
		hash, err := fileHash(path)
		if err == nil {
			result[path] = hash
		}
	}
	return result
}

func diffSharedFiles(before, after map[string]string) []FileObservation {
	paths := make([]string, 0, len(before)+len(after))
	seen := map[string]bool{}
	for path := range before {
		seen[path] = true
		paths = append(paths, path)
	}
	for path := range after {
		if !seen[path] {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	result := make([]FileObservation, 0, len(paths))
	for _, path := range paths {
		if before[path] != after[path] {
			result = append(result, FileObservation{Path: path, BeforeHash: before[path], AfterHash: after[path]})
		}
	}
	return result
}

func mergeFileObservations(previous, current []FileObservation) []FileObservation {
	byPath := map[string]FileObservation{}
	for _, observation := range previous {
		byPath[observation.Path] = observation
	}
	for _, observation := range current {
		if existing, ok := byPath[observation.Path]; ok {
			existing.AfterHash = observation.AfterHash
			byPath[observation.Path] = existing
		} else {
			byPath[observation.Path] = observation
		}
	}
	paths := make([]string, 0, len(byPath))
	for path := range byPath {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	result := make([]FileObservation, 0, len(paths))
	for _, path := range paths {
		result = append(result, byPath[path])
	}
	return result
}

func pluginFingerprintMatches(plugin pluginEntry, record *InstallRecord, assets assetInfo) (bool, error) {
	if plugin.Marketplace != "" && plugin.Marketplace != MarketplaceName {
		return false, nil
	}
	if plugin.Path != "" {
		if info, err := os.Stat(plugin.Path); err == nil && info.IsDir() {
			hash, err := hashTree(plugin.Path)
			if err != nil {
				return false, err
			}
			return hash == record.PluginSHA256 || hash == assets.PluginSHA, nil
		} else if err != nil && !os.IsNotExist(err) {
			return false, err
		}
	}
	// Some Claude CLI versions omit the cache path. Exact selector + version
	// remains a stable native fingerprint; an empty version is not sufficient.
	return plugin.Version != "" && (plugin.Version == record.Version || plugin.Version == assets.Version), nil
}

func marketplaceMatchesRecord(entry marketplaceEntry, record *InstallRecord, spec hostSpec, assets assetInfo) bool {
	paths := []string{entry.Root, entry.InstallLocation}
	for _, path := range paths {
		if path == "" || !filepath.IsAbs(path) {
			continue
		}
		clean := filepath.Clean(path)
		manifests := []string{
			filepath.Join(clean, ".agents", "plugins", "marketplace.json"),
			filepath.Join(clean, ".claude-plugin", "marketplace.json"),
		}
		if clean == filepath.Clean(record.MarketplaceRoot) || clean == filepath.Clean(spec.root) {
			manifests = append(manifests, spec.marketplacePath)
		}
		for _, manifest := range manifests {
			if hash, err := fileHash(manifest); err == nil && (hash == record.MarketplaceSHA256 || hash == assets.MarketplaceSHA) {
				return true
			}
		}
	}
	return false
}

func (m *Manager) marketplaceAddAction(spec hostSpec) Action {
	command := []string{spec.binary, "plugin", "marketplace", "add", spec.root}
	if spec.host == HostCodex {
		command = append(command, "--json")
	} else {
		command = append(command, "--scope", "user")
	}
	return Action{Kind: "add_marketplace", Target: spec.root, Command: command, Reason: "register the isolated AgentMux local marketplace through the native CLI"}
}

func (m *Manager) marketplaceRemoveAction(spec hostSpec) Action {
	command := []string{spec.binary, "plugin", "marketplace", "remove", MarketplaceName}
	if spec.host == HostCodex {
		command = append(command, "--json")
	} else {
		command = append(command, "--scope", "user")
	}
	return Action{Kind: "remove_marketplace", Target: MarketplaceName, Command: command, Reason: "remove the exact owned marketplace through the native CLI"}
}

func (m *Manager) pluginInstallAction(spec hostSpec, kind, reason string) Action {
	selector := PluginID + "@" + MarketplaceName
	command := []string{spec.binary, "plugin", "add", selector, "--json"}
	if spec.host == HostClaude {
		command = []string{spec.binary, "plugin", "install", selector, "--scope", "user"}
	}
	return Action{Kind: kind, Target: selector, Command: command, Reason: reason}
}

func (m *Manager) pluginRemoveAction(spec hostSpec) Action {
	selector := PluginID + "@" + MarketplaceName
	command := []string{spec.binary, "plugin", "remove", selector, "--json"}
	if spec.host == HostClaude {
		command = []string{spec.binary, "plugin", "uninstall", selector, "--scope", "user"}
	}
	return Action{Kind: "remove_plugin", Target: selector, Command: command, Reason: "remove the exact owned plugin selector through the native CLI"}
}

func (m *Manager) runAction(ctx context.Context, spec hostSpec, action Action) error {
	if len(action.Command) == 0 {
		return nil
	}
	if action.Command[0] != spec.binary {
		return fmt.Errorf("refusing unexpected native CLI %q for %s", action.Command[0], spec.host)
	}
	_, err := m.runner.Run(ctx, Command{Name: action.Command[0], Args: action.Command[1:], Env: m.env()})
	return err
}

// Doctor performs a read-only coverage and ownership audit. It never reads or
// edits Codex hooks.state; trust remains pending/manual until the host exposes
// a stable public trust API.
func (m *Manager) Doctor(ctx context.Context, host Host) (DoctorReport, error) {
	spec, err := m.spec(host)
	if err != nil {
		return DoctorReport{}, err
	}
	preview, err := m.preview(ctx, spec)
	if err != nil {
		return DoctorReport{}, err
	}
	discovered := m.discover(ctx, spec)
	record, _, _ := m.loadRecord(host)
	report := DoctorReport{
		Host: host, Status: preview.Status, Findings: preview.Findings,
		Coverage: map[string]string{
			"plugin":     "missing",
			"helper":     "missing",
			"trust":      "not_applicable",
			"otel":       detectOTelCoverage(m.home, host),
			"transcript": detectTranscriptCoverage(m.home, host),
			"proxy":      detectProxyCoverage(m.home, host),
		},
	}
	if record != nil {
		report.InstallID = record.InstallID
	}
	if plugin := discovered.installedPlugin(PluginID); plugin != nil {
		report.Coverage["plugin"] = "installed"
	}
	if helperHash, hashErr := fileHash(m.helperTarget()); hashErr == nil && helperHash != "" {
		report.Coverage["helper"] = "installed"
	}
	if host == HostCodex && report.Coverage["plugin"] == "installed" {
		report.Coverage["trust"] = "pending_manual_review"
	} else if host == HostClaude && report.Coverage["plugin"] == "installed" {
		report.Coverage["trust"] = "managed_by_claude"
	}
	ownerSet := map[string]bool{}
	for _, finding := range report.Findings {
		if finding.Owner != "" && finding.Owner != "unknown" {
			ownerSet[finding.Owner] = true
		}
	}
	for owner := range ownerSet {
		report.Owners = append(report.Owners, owner)
	}
	sort.Strings(report.Owners)
	return report, nil
}

func detectOTelCoverage(home string, host Host) string {
	for _, path := range sharedConfigPaths(home, host) {
		if raw, err := readSmallFile(path); err == nil {
			text := strings.ToLower(string(raw))
			if strings.Contains(text, "otel_exporter_otlp") || strings.Contains(text, "[otel]") || strings.Contains(text, "otel.") {
				return "configured"
			}
		}
	}
	return "not_detected"
}

func detectTranscriptCoverage(home string, host Host) string {
	paths := []string{filepath.Join(home, ".claude", "projects")}
	if host == HostCodex {
		paths = []string{filepath.Join(home, ".codex", "sessions"), filepath.Join(home, ".codex", "archived_sessions")}
	}
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return "available"
		}
	}
	return "not_detected"
}

func detectProxyCoverage(home string, host Host) string {
	for _, path := range sharedConfigPaths(home, host) {
		if raw, err := readSmallFile(path); err == nil && containsLoopback(strings.ToLower(string(raw))) {
			return "loopback_detected"
		}
	}
	return "not_detected"
}

func hasMutationAction(actions []Action) bool {
	for _, action := range actions {
		if action.Kind != "review_trust" {
			return true
		}
	}
	return false
}
