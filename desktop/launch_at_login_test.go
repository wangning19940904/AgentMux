//go:build desktop

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchAtLoginDefaultsToEnabledAndPersistsChoice(t *testing.T) {
	root := t.TempDir()
	manager := &launchAtLoginManager{
		supported:      true,
		executablePath: "/Applications/AgentMux & Tools.app/Contents/MacOS/AgentMux",
		agentPath:      filepath.Join(root, "LaunchAgents", launchAtLoginLabel+".plist"),
		preferencePath: filepath.Join(root, "config", launchAtLoginPreferenceFile),
	}

	enabled, exists, err := manager.readPreference()
	if err != nil {
		t.Fatal(err)
	}
	if enabled || exists {
		t.Fatalf("new preference = enabled %t, exists %t", enabled, exists)
	}

	if err := manager.apply(true, true); err != nil {
		t.Fatal(err)
	}
	status, err := manager.status()
	if err != nil {
		t.Fatal(err)
	}
	if !status.Supported || !status.Enabled {
		t.Fatalf("enabled status = %+v", status)
	}
	raw, err := os.ReadFile(manager.agentPath)
	if err != nil {
		t.Fatal(err)
	}
	plist := string(raw)
	if !strings.Contains(plist, "/Applications/AgentMux &amp; Tools.app/Contents/MacOS/AgentMux") {
		t.Fatalf("plist executable is not XML escaped:\n%s", plist)
	}
	enabled, exists, err = manager.readPreference()
	if err != nil || !exists || !enabled {
		t.Fatalf("saved preference = enabled %t, exists %t, err %v", enabled, exists, err)
	}

	if err := manager.apply(false, true); err != nil {
		t.Fatal(err)
	}
	status, err = manager.status()
	if err != nil {
		t.Fatal(err)
	}
	if status.Enabled {
		t.Fatalf("disabled status = %+v", status)
	}
	enabled, exists, err = manager.readPreference()
	if err != nil || !exists || enabled {
		t.Fatalf("disabled preference = enabled %t, exists %t, err %v", enabled, exists, err)
	}
}

func TestLaunchAtLoginReconcilesWithoutOverwritingPreference(t *testing.T) {
	root := t.TempDir()
	manager := &launchAtLoginManager{
		supported:      true,
		executablePath: "/Applications/AgentMux.app/Contents/MacOS/AgentMux",
		agentPath:      filepath.Join(root, "LaunchAgents", launchAtLoginLabel+".plist"),
		preferencePath: filepath.Join(root, "config", launchAtLoginPreferenceFile),
	}
	if err := writeAtomicFile(manager.preferencePath, []byte("false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.apply(false, false); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(manager.preferencePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "false\n" {
		t.Fatalf("preference changed to %q", raw)
	}
}
