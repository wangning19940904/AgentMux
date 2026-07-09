package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDetectCLIReadsVersionFromPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "lark-cli"), "#!/bin/sh\necho 'lark-cli version 9.9.9'\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	st := DetectCLI(context.Background(), CLISpec{ID: "lark-cli", Name: "Lark CLI", Bin: "lark-cli"})
	if !st.Installed || !strings.Contains(st.Version, "9.9.9") {
		t.Fatalf("status = %+v", st)
	}
}

func TestInstallCLIUsesWhitelistAndVerifiesCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "npm"), "#!/bin/sh\ncat > '"+filepath.Join(bin, "lark-cli")+"' <<'EOS'\n#!/bin/sh\nif [ \"$1\" = \"update\" ]; then echo updated; exit 0; fi\necho 'lark-cli version 1.2.3'\nEOS\nchmod +x '"+filepath.Join(bin, "lark-cli")+"'\necho installed\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	res := InstallCLI(context.Background(), "lark-cli", "install")
	if !res.OK || !strings.Contains(res.Version, "1.2.3") {
		t.Fatalf("install result = %+v", res)
	}
}

func TestCheckCLIUpdateDetectsAvailableVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "lark-cli"), "#!/bin/sh\necho 'lark-cli version 1.2.3'\n")
	writeExecutable(t, filepath.Join(bin, "npm"), "#!/bin/sh\nif [ \"$1\" = \"view\" ]; then echo '1.2.4'; exit 0; fi\nexit 1\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	res := CheckCLIUpdate(context.Background(), "lark-cli")
	if res.Error != "" || !res.UpdateAvailable || res.CurrentVersion != "1.2.3" || res.LatestVersion != "1.2.4" {
		t.Fatalf("check result = %+v", res)
	}
}

func TestInstallCLISkipsUpdateWhenLatestMatches(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	bin := t.TempDir()
	marker := filepath.Join(bin, "updated")
	writeExecutable(t, filepath.Join(bin, "lark-cli"), "#!/bin/sh\nif [ \"$1\" = \"update\" ]; then touch '"+marker+"'; echo updated; exit 0; fi\necho 'lark-cli version 1.2.3'\n")
	writeExecutable(t, filepath.Join(bin, "npm"), "#!/bin/sh\nif [ \"$1\" = \"view\" ]; then echo '1.2.3'; exit 0; fi\nexit 1\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	res := InstallCLI(context.Background(), "lark-cli", "update")
	if !res.OK || res.Error != "" || res.Command != "" {
		t.Fatalf("update result = %+v", res)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("update command should not have run, marker err = %v", err)
	}
}

func TestInstallCLIRejectsUnknownID(t *testing.T) {
	res := InstallCLI(context.Background(), "curl", "install")
	if res.OK || !strings.Contains(res.Error, "unknown CLI") {
		t.Fatalf("result = %+v", res)
	}
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}
