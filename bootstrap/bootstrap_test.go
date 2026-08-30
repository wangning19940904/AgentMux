package bootstrap

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/wangning19940904/AgentMux/framework"
)

func TestPrepareRuntimeEnvironmentFindsUserExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix executable layout test")
	}

	home := t.TempDir()
	nvmBin := filepath.Join(home, ".nvm", "versions", "node", "v24.13.0", "bin")
	if err := os.MkdirAll(nvmBin, 0o755); err != nil {
		t.Fatal(err)
	}
	codexPath := filepath.Join(nvmBin, "codex")
	if err := os.WriteFile(codexPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", home)
	t.Setenv("NVM_DIR", "")
	t.Setenv("PNPM_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("PATH", t.TempDir())

	framework.PrepareRuntimeEnvironment()

	path, err := exec.LookPath("codex")
	if err != nil {
		t.Fatalf("Codex was not exposed on daemon PATH: %v", err)
	}
	if path != codexPath {
		t.Fatalf("Codex path = %q, want %q", path, codexPath)
	}
}
