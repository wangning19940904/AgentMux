package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeHostState struct {
	marketplaceRoot string
	pluginPath      string
	pluginVersion   string
}

type fakeRunner struct {
	mu       sync.Mutex
	states   map[Host]*fakeHostState
	commands []Command
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{states: map[Host]*fakeHostState{
		HostClaude: {},
		HostCodex:  {},
	}}
}

func (r *fakeRunner) LookPath(name string) (string, error) {
	if name != "claude" && name != "codex" {
		return "", fmt.Errorf("not found: %s", name)
	}
	return "/fake/bin/" + name, nil
}

func (r *fakeRunner) Run(_ context.Context, command Command) (CommandOutput, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands = append(r.commands, command)
	host := Host(command.Name)
	state := r.states[host]
	args := command.Args
	if reflect.DeepEqual(args, []string{"plugin", "marketplace", "list", "--json"}) {
		if state.marketplaceRoot == "" {
			if host == HostCodex {
				return jsonOutput(map[string]any{"marketplaces": []any{}})
			}
			return jsonOutput([]any{})
		}
		if host == HostCodex {
			return jsonOutput(map[string]any{"marketplaces": []any{map[string]any{
				"name": MarketplaceName, "root": state.marketplaceRoot,
			}}})
		}
		return jsonOutput([]any{map[string]any{
			"name": MarketplaceName, "source": "directory", "installLocation": state.marketplaceRoot,
		}})
	}
	if reflect.DeepEqual(args, []string{"plugin", "list", "--available", "--json"}) {
		if state.pluginPath == "" {
			if host == HostCodex {
				return jsonOutput(map[string]any{"installed": []any{}})
			}
			return jsonOutput([]any{})
		}
		entry := map[string]any{
			"pluginId": PluginID + "@" + MarketplaceName, "id": PluginID + "@" + MarketplaceName,
			"name": PluginID, "marketplaceName": MarketplaceName, "version": state.pluginVersion,
			"installed": true, "enabled": true, "installPath": state.pluginPath,
			"source": map[string]any{"source": "local", "path": state.pluginPath},
		}
		if host == HostCodex {
			return jsonOutput(map[string]any{"installed": []any{entry}})
		}
		return jsonOutput([]any{entry})
	}
	if len(args) >= 4 && args[0] == "plugin" && args[1] == "marketplace" && args[2] == "add" {
		state.marketplaceRoot = args[3]
		return jsonOutput(map[string]any{"ok": true})
	}
	if len(args) >= 4 && args[0] == "plugin" && args[1] == "marketplace" && args[2] == "remove" {
		state.marketplaceRoot = ""
		return jsonOutput(map[string]any{"ok": true})
	}
	if len(args) >= 3 && args[0] == "plugin" && args[1] == "marketplace" && args[2] == "update" {
		return jsonOutput(map[string]any{"ok": true})
	}
	if len(args) >= 3 && args[0] == "plugin" && (args[1] == "add" || args[1] == "install" || args[1] == "update") {
		if state.marketplaceRoot == "" {
			return CommandOutput{}, errors.New("marketplace is missing")
		}
		state.pluginPath = filepath.Join(state.marketplaceRoot, "plugins", PluginID)
		state.pluginVersion = PluginVersion
		return jsonOutput(map[string]any{"ok": true})
	}
	if len(args) >= 3 && args[0] == "plugin" && (args[1] == "remove" || args[1] == "uninstall") {
		state.pluginPath = ""
		state.pluginVersion = ""
		return jsonOutput(map[string]any{"ok": true})
	}
	return CommandOutput{}, fmt.Errorf("unexpected command: %s %s", command.Name, strings.Join(args, " "))
}

func jsonOutput(value any) (CommandOutput, error) {
	raw, err := json.Marshal(value)
	return CommandOutput{Stdout: string(raw)}, err
}

func (r *fakeRunner) mutations() []Command {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []Command
	for _, command := range r.commands {
		joined := strings.Join(command.Args, " ")
		if !strings.Contains(joined, " list ") {
			result = append(result, command)
		}
	}
	return result
}

func newTestManager(t *testing.T, home string, runner *fakeRunner) *Manager {
	t.Helper()
	helperSource := filepath.Join(t.TempDir(), "agentmux-hook")
	if err := os.WriteFile(helperSource, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	assets, err := filepath.Abs(filepath.Join("..", "marketplaces"))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(Options{
		HomeDir: home, AssetsDir: assets, HelperSource: helperSource, Runner: runner,
		Now:    func() time.Time { return time.Date(2026, 7, 11, 1, 2, 3, 0, time.UTC) },
		Random: strings.NewReader("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestCodexInstallIsAdditiveIdempotentAndUninstallPreservesThirdParty(t *testing.T) {
	home := t.TempDir()
	runner := newFakeRunner()
	manager := newTestManager(t, home, runner)
	configPath := filepath.Join(home, ".codex", "config.toml")
	hooksPath := filepath.Join(home, ".codex", "hooks.json")
	fluxManifest := filepath.Join(home, ".flux", "hooks", "codex-manifest.json")
	ccSwitchDB := filepath.Join(home, ".cc-switch", "cc-switch.db")
	for _, path := range []string{configPath, hooksPath, fluxManifest, ccSwitchDB} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	configBefore := []byte("model = \"gpt-test\"\n# user-owned\n")
	hooksBefore := []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/Applications/Flux Island.app/Contents/Resources/bin/flux-hooks"}]}]}}`)
	if err := os.WriteFile(configPath, configBefore, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksPath, hooksBefore, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fluxManifest, []byte(`{"owner":"flux"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ccSwitchDB, []byte("third-party-db"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := manager.Install(context.Background(), HostCodex)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Record == nil || result.Record.Status != StatusPendingTrust || result.Record.InstallID == "" {
		t.Fatalf("unexpected install result: %+v", result)
	}
	installID := result.Record.InstallID
	assertFileEquals(t, configPath, configBefore)
	assertFileEquals(t, hooksPath, hooksBefore)
	helper, err := os.Stat(manager.helperTarget())
	if err != nil {
		t.Fatal(err)
	}
	if helper.Mode().Perm() != 0o700 {
		t.Fatalf("helper mode = %o, want 700", helper.Mode().Perm())
	}

	beforeMutations := len(runner.mutations())
	again, err := manager.Install(context.Background(), HostCodex)
	if err != nil {
		t.Fatal(err)
	}
	if again.Changed || again.Record.InstallID != installID {
		t.Fatalf("idempotent install changed state: %+v", again)
	}
	if got := len(runner.mutations()); got != beforeMutations {
		t.Fatalf("idempotent install ran %d new mutation(s)", got-beforeMutations)
	}
	doctor, err := manager.Doctor(context.Background(), HostCodex)
	if err != nil {
		t.Fatal(err)
	}
	if doctor.Status != StatusPendingTrust || doctor.Coverage["trust"] != "pending_manual_review" {
		t.Fatalf("unexpected doctor report: %+v", doctor)
	}
	if !containsString(doctor.Owners, "flux-island") || !containsString(doctor.Owners, "cc-switch") {
		t.Fatalf("third-party owners missing: %+v", doctor.Owners)
	}

	uninstalled, err := manager.Uninstall(context.Background(), HostCodex)
	if err != nil {
		t.Fatal(err)
	}
	if !uninstalled.Changed || len(uninstalled.Preserved) != 0 {
		t.Fatalf("unexpected uninstall result: %+v", uninstalled)
	}
	assertFileEquals(t, configPath, configBefore)
	assertFileEquals(t, hooksPath, hooksBefore)
	if _, err := os.Stat(manager.helperTarget()); !os.IsNotExist(err) {
		t.Fatalf("helper still exists after exact uninstall: %v", err)
	}
	if _, err := os.Stat(manager.statePath(HostCodex)); !os.IsNotExist(err) {
		t.Fatalf("ownership state still exists: %v", err)
	}
}

func TestInstallRefusesUnownedSameName(t *testing.T) {
	home := t.TempDir()
	runner := newFakeRunner()
	assets, err := filepath.Abs(filepath.Join("..", "marketplaces", "claude"))
	if err != nil {
		t.Fatal(err)
	}
	runner.states[HostClaude] = &fakeHostState{
		marketplaceRoot: assets,
		pluginPath:      filepath.Join(assets, "plugins", PluginID),
		pluginVersion:   PluginVersion,
	}
	manager := newTestManager(t, home, runner)
	_, err = manager.Install(context.Background(), HostClaude)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
	if len(runner.mutations()) != 0 {
		t.Fatalf("conflict path mutated native state: %+v", runner.mutations())
	}
	if _, err := os.Stat(manager.helperTarget()); !os.IsNotExist(err) {
		t.Fatalf("conflict path created helper: %v", err)
	}
}

func TestUninstallPreservesDriftedHelper(t *testing.T) {
	home := t.TempDir()
	runner := newFakeRunner()
	manager := newTestManager(t, home, runner)
	installed, err := manager.Install(context.Background(), HostClaude)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.helperTarget(), []byte("third-party-replacement"), 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := manager.Uninstall(context.Background(), HostClaude)
	if !errors.Is(err, ErrDrift) {
		t.Fatalf("error = %v, want ErrDrift", err)
	}
	if !containsString(result.Preserved, manager.helperTarget()) {
		t.Fatalf("drifted helper not reported as preserved: %+v", result)
	}
	assertFileEquals(t, manager.helperTarget(), []byte("third-party-replacement"))
	record, _, err := manager.loadRecord(HostClaude)
	if err != nil {
		t.Fatal(err)
	}
	if record == nil || record.InstallID != installed.Record.InstallID || record.Status != StatusDrift {
		t.Fatalf("unexpected retained record: %+v", record)
	}
}

func TestRepairRefusesPluginFingerprintDrift(t *testing.T) {
	home := t.TempDir()
	runner := newFakeRunner()
	manager := newTestManager(t, home, runner)
	if _, err := manager.Install(context.Background(), HostCodex); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(t.TempDir(), PluginID)
	if err := os.MkdirAll(filepath.Join(foreign, ".codex-plugin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreign, ".codex-plugin", "plugin.json"), []byte(`{"name":"agentmux-observer","version":"9.9.9"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	runner.states[HostCodex].pluginPath = foreign
	runner.states[HostCodex].pluginVersion = "9.9.9"
	runner.mu.Unlock()
	before := len(runner.mutations())
	_, err := manager.Repair(context.Background(), HostCodex)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
	if got := len(runner.mutations()); got != before {
		t.Fatalf("drift repair ran %d mutations", got-before)
	}
}

func TestPreviewRefusesUnownedSharedAgentMuxHandler(t *testing.T) {
	home := t.TempDir()
	runner := newFakeRunner()
	manager := newTestManager(t, home, runner)
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"hooks":{"Stop":[{"hooks":[{"command":"$HOME/.agentmux/bin/agentmux-hook --source claude"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	preview, err := manager.Preview(context.Background(), HostClaude)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Blocked || !hasFinding(preview.Findings, "same_handler_unowned") {
		t.Fatalf("expected same-handler conflict: %+v", preview)
	}
}

func TestAtomicWriteCASRefusesConcurrentChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected, err := fileHash(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := atomicWriteCAS(path, []byte("three"), 0o600, expected); !errors.Is(err, ErrCAS) {
		t.Fatalf("error = %v, want ErrCAS", err)
	}
	assertFileEquals(t, path, []byte("two"))
}

func TestNativeCLIEndToEnd(t *testing.T) {
	if os.Getenv("AMUX_RUN_NATIVE_CLI_TEST") != "1" {
		t.Skip("set AMUX_RUN_NATIVE_CLI_TEST=1 to exercise installed Claude and Codex CLIs")
	}
	assets, err := filepath.Abs(filepath.Join("..", "marketplaces"))
	if err != nil {
		t.Fatal(err)
	}
	helperSource, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []Host{HostCodex, HostClaude} {
		t.Run(string(host), func(t *testing.T) {
			home := t.TempDir()
			manager, err := NewManager(Options{
				HomeDir: home, AssetsDir: assets, HelperSource: helperSource, Runner: ExecRunner{},
			})
			if err != nil {
				t.Fatal(err)
			}
			installed, err := manager.Install(context.Background(), host)
			if err != nil {
				t.Fatal(err)
			}
			if installed.Record == nil || installed.Record.InstallID == "" {
				t.Fatalf("missing ownership record: %+v", installed)
			}
			if _, err := os.Stat(filepath.Join(home, ".codex", "hooks.state")); !os.IsNotExist(err) {
				t.Fatalf("manager must not create hooks.state: %v", err)
			}
			if _, err := manager.Uninstall(context.Background(), host); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func assertFileEquals(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s changed:\n got %q\nwant %q", path, got, want)
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func hasFinding(findings []Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
