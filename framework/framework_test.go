package framework

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCatalogHasKnownFrameworks(t *testing.T) {
	want := map[string]KindType{
		"claudecode":       KindCLI,
		"claude-agent-sdk": KindSDK,
		"openai-agents":    KindSDK,
		"deepagents":       KindSDK,
	}
	for kind, kt := range want {
		spec, ok := Lookup(kind)
		if !ok {
			t.Fatalf("catalog missing %q", kind)
		}
		if spec.KindType != kt {
			t.Fatalf("%q kind_type = %q, want %q", kind, spec.KindType, kt)
		}
	}
}

func TestLookupUnknownReturnsFalse(t *testing.T) {
	if _, ok := Lookup("does-not-exist"); ok {
		t.Fatal("expected unknown framework lookup to fail")
	}
}

func TestDetectCLIUsesLookPath(t *testing.T) {
	// A CLI whose binary is almost certainly not present must report as not
	// installed rather than erroring.
	spec := Spec{Kind: "fake-cli", KindType: KindCLI, Bin: "definitely-not-a-real-binary-xyz"}
	st := Detect(spec, DetectPrereqs())
	if st.Installed {
		t.Fatal("expected fake CLI to be not installed")
	}
}

func TestDetectCLIReadsNormalizedVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	bin := t.TempDir()
	writeFrameworkExecutable(t, filepath.Join(bin, "claude"), "#!/bin/sh\necho '2.1.211 (Claude Code)'\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	spec, ok := Lookup("claudecode")
	if !ok {
		t.Fatal("claudecode missing from catalog")
	}
	st := Detect(spec, DetectPrereqs())
	if !st.Installed || st.Version != "2.1.211" || st.Detail != "" {
		t.Fatalf("status = %+v", st)
	}
}

func TestInstallRejectsUnknown(t *testing.T) {
	res := Install(context.Background(), "not-a-framework")
	if res.OK || !strings.Contains(res.Error, "unknown framework") {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestInstallCLIUsesCatalogNPMCommandAndVerifiesBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	bin := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	npmScript := "#!/bin/sh\n" +
		"case \" $* \" in *' install -g @google/gemini-cli@latest '*) ;; *) exit 2 ;; esac\n" +
		"cat > '" + filepath.Join(bin, "gemini") + "' <<'EOS'\n" +
		"#!/bin/sh\necho 'gemini 1.2.3'\nEOS\n" +
		"chmod +x '" + filepath.Join(bin, "gemini") + "'\n" +
		"echo installed\n"
	writeFrameworkExecutable(t, filepath.Join(bin, "npm"), npmScript)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	res := Install(context.Background(), "gemini")
	if !res.OK || res.Error != "" || res.Version != "1.2.3" || res.Command != "npm install -g @google/gemini-cli@latest" {
		t.Fatalf("install result = %+v", res)
	}
}

func TestDetectCLIFindsUserLocalBinaryAndRefreshesPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix user-local binary test")
	}
	home := t.TempDir()
	localBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFrameworkExecutable(t, filepath.Join(localBin, "cursor-agent"), "#!/bin/sh\necho '2026.07.16-899851b'\n")
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())

	spec, ok := Lookup("cursor")
	if !ok {
		t.Fatal("cursor missing from catalog")
	}
	status := Detect(spec, DetectPrereqs())
	if !status.Installed || status.Version != "2026.07.16-899851b" {
		t.Fatalf("status = %+v", status)
	}
	if _, err := exec.LookPath("cursor-agent"); err != nil {
		t.Fatalf("user-local bin was not added to PATH: %v", err)
	}
}

func TestInstallRejectsUnsupportedFramework(t *testing.T) {
	// deepagents is catalogued but not supported for automatic install.
	res := Install(context.Background(), "deepagents")
	if res.OK {
		t.Fatalf("expected deepagents install to be refused, got: %+v", res)
	}
	if !strings.Contains(res.Error, "not yet supported") && !strings.Contains(res.Error, "runtime") {
		t.Fatalf("unexpected deepagents error: %q", res.Error)
	}
}

func TestCheckUpdateDetectsNewerSDKVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	packageDir := filepath.Join(home, ".agentnexus", "sidecar", "node_modules", "@anthropic-ai", "claude-agent-sdk")
	if err := os.MkdirAll(packageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageDir, "package.json"), []byte(`{"version":"1.2.3"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	writeFrameworkExecutable(t, filepath.Join(bin, "npm"), "#!/bin/sh\nif [ \"$1\" = \"view\" ]; then echo '1.3.0'; exit 0; fi\nexit 1\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	res := CheckUpdate(context.Background(), "claude-agent-sdk")
	if res.Error != "" || !res.Installed || !res.UpdateAvailable || res.CurrentVersion != "1.2.3" || res.LatestVersion != "1.3.0" {
		t.Fatalf("check result = %+v", res)
	}
}

func TestCheckUpdateDetectsNewerCLIVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	bin := t.TempDir()
	writeFrameworkExecutable(t, filepath.Join(bin, "claude"), "#!/bin/sh\necho 'claude 2.1.210'\n")
	writeFrameworkExecutable(t, filepath.Join(bin, "npm"), "#!/bin/sh\nif [ \"$1\" = \"view\" ]; then echo '2.1.211'; exit 0; fi\nexit 1\n")
	t.Setenv("PATH", bin)

	res := CheckUpdate(context.Background(), "claudecode")
	if res.Error != "" || !res.Installed || !res.UpdateAvailable || res.CurrentVersion != "2.1.210" || res.LatestVersion != "2.1.211" {
		t.Fatalf("check result = %+v", res)
	}
}

func TestUpdateRunsCataloguedCLICommandAndVerifiesVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	bin := t.TempDir()
	versionFile := filepath.Join(bin, "claude-version")
	if err := os.WriteFile(versionFile, []byte("2.1.210\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	claudeScript := "#!/bin/sh\n" +
		"if [ \"$1\" = \"update\" ]; then echo '2.1.211' > '" + versionFile + "'; echo updated; exit 0; fi\n" +
		"cat '" + versionFile + "'\n"
	writeFrameworkExecutable(t, filepath.Join(bin, "claude"), claudeScript)
	writeFrameworkExecutable(t, filepath.Join(bin, "npm"), "#!/bin/sh\nif [ \"$1\" = \"view\" ]; then echo '2.1.211'; exit 0; fi\nexit 1\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	res := Update(context.Background(), "claudecode")
	if !res.OK || res.Error != "" || res.Action != "update" || res.Command != "claude update" || res.Version != "2.1.211" {
		t.Fatalf("update result = %+v", res)
	}
}

func TestUpdateCodexUsesHomebrewCommandThroughSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script and Homebrew layout test")
	}
	root := t.TempDir()
	prefix := filepath.Join(root, "homebrew")
	brewBin := filepath.Join(prefix, "bin")
	caskBin := filepath.Join(prefix, "Caskroom", "codex", "0.144.1", "codex-aarch64-apple-darwin")
	frontBin := filepath.Join(root, "front-bin")
	if err := os.MkdirAll(brewBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(caskBin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(frontBin, 0o755); err != nil {
		t.Fatal(err)
	}

	versionFile := filepath.Join(root, "codex-version")
	if err := os.WriteFile(versionFile, []byte("0.144.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFrameworkExecutable(t, caskBin, "#!/bin/sh\ncat '"+versionFile+"'\n")
	brewPath := filepath.Join(brewBin, "brew")
	brewScript := "#!/bin/sh\n" +
		"if [ \"$1 $2 $3\" != \"upgrade --cask codex\" ]; then exit 2; fi\n" +
		"echo '0.144.6' > '" + versionFile + "'\n" +
		"echo upgraded\n"
	writeFrameworkExecutable(t, brewPath, brewScript)
	writeFrameworkExecutable(t, filepath.Join(frontBin, "npm"), "#!/bin/sh\nif [ \"$1 $2 $3\" = \"view @openai/codex version\" ]; then echo '0.144.6'; exit 0; fi\nexit 1\n")

	homebrewCodex := filepath.Join(brewBin, "codex")
	if err := os.Symlink(caskBin, homebrewCodex); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(homebrewCodex, filepath.Join(frontBin, "codex")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", frontBin+string(os.PathListSeparator)+"/usr/bin:/bin")

	res := Update(context.Background(), "codex")
	resolvedBrewPath, err := filepath.EvalSymlinks(brewPath)
	if err != nil {
		t.Fatal(err)
	}
	wantCommand := resolvedBrewPath + " upgrade --cask codex"
	if !res.OK || res.Error != "" || res.Command != wantCommand || res.Version != "0.144.6" || !strings.Contains(res.Log, "upgraded") {
		t.Fatalf("update result = %+v, want command %q", res, wantCommand)
	}
}

func TestCodexOutsideHomebrewKeepsNativeUpdateCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	bin := t.TempDir()
	writeFrameworkExecutable(t, filepath.Join(bin, "codex"), "#!/bin/sh\necho 'codex-cli 0.144.1'\n")
	t.Setenv("PATH", bin)

	spec, ok := Lookup("codex")
	if !ok {
		t.Fatal("codex missing from catalog")
	}
	command, err := resolvedCLIUpdateCommand(spec)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(command, " "); got != "codex update" {
		t.Fatalf("update command = %q, want %q", got, "codex update")
	}
}

func TestCursorInstallerVersion(t *testing.T) {
	raw := `DOWNLOAD_URL="https://downloads.cursor.com/lab/2026.07.09-a3815c0/darwin/arm64/agent-cli-package.tar.gz"`
	if got := cursorInstallerVersion(raw); got != "2026.07.09-a3815c0" {
		t.Fatalf("cursor installer version = %q", got)
	}
	if !frameworkUpdateAvailable(Spec{LatestURL: "https://cursor.com/install"}, "2026.07.09-a3815c0", "2026.07.09-fffffff") {
		t.Fatal("different native build identifiers should be treated as an available update")
	}
}

func TestUpdateSkipsInstallWhenSDKIsCurrent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	packageDir := filepath.Join(home, ".agentnexus", "sidecar", "node_modules", "@anthropic-ai", "claude-agent-sdk")
	if err := os.MkdirAll(packageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageDir, "package.json"), []byte(`{"version":"1.2.3"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	marker := filepath.Join(bin, "installed")
	writeFrameworkExecutable(t, filepath.Join(bin, "npm"), "#!/bin/sh\nif [ \"$1\" = \"view\" ]; then echo '1.2.3'; exit 0; fi\ntouch '"+marker+"'\n")
	t.Setenv("PATH", bin)

	res := Update(context.Background(), "claude-agent-sdk")
	if !res.OK || res.Error != "" || res.Command != "" || res.Action != "update" {
		t.Fatalf("update result = %+v", res)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("npm install should not have run, marker err = %v", err)
	}
}

func TestUpdateInstallsLatestSDKVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	packageDir := filepath.Join(home, ".agentnexus", "sidecar", "node_modules", "@anthropic-ai", "claude-agent-sdk")
	if err := os.MkdirAll(packageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	packageJSON := filepath.Join(packageDir, "package.json")
	if err := os.WriteFile(packageJSON, []byte(`{"version":"1.2.3"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"view\" ]; then echo '1.2.4'; exit 0; fi\n" +
		"case \" $* \" in *' @anthropic-ai/claude-agent-sdk@latest '*) ;; *) exit 2 ;; esac\n" +
		"echo '{\"version\":\"1.2.4\"}' > '" + packageJSON + "'\n"
	writeFrameworkExecutable(t, filepath.Join(bin, "npm"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	res := Update(context.Background(), "claude-agent-sdk")
	if !res.OK || res.Error != "" || res.Version != "1.2.4" || !strings.Contains(res.Command, "@anthropic-ai/claude-agent-sdk@latest") {
		t.Fatalf("update result = %+v", res)
	}
}

func TestSDKVersionGreaterHandlesPrerelease(t *testing.T) {
	if !sdkVersionGreater("1.2.3", "1.2.3-beta.1") {
		t.Fatal("stable release should be newer than matching prerelease")
	}
	if !sdkVersionGreater("1.2.3-beta.2", "1.2.3-beta.1") {
		t.Fatal("later prerelease should compare newer")
	}
	if sdkVersionGreater("1.2.3-beta.1", "1.2.3") {
		t.Fatal("prerelease should not be newer than stable release")
	}
}

func TestEnsureSidecarPreservesInstalledDependencies(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	manifestPath := filepath.Join(home, ".agentnexus", "sidecar", "package.json")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	want := []byte(`{"dependencies":{"@anthropic-ai/claude-agent-sdk":"^1.2.3"}}`)
	if err := os.WriteFile(manifestPath, want, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := EnsureSidecar(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("package.json was overwritten: got %s", got)
	}
}

func writeFrameworkExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}
