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
