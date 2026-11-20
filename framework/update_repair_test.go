package framework

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func isolateFrameworkPath(t *testing.T, dirs ...string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("NVM_DIR", "")
	t.Setenv("PNPM_HOME", "")
	previous := packageManagerDirs
	packageManagerDirs = nil
	t.Cleanup(func() { packageManagerDirs = previous })
	t.Setenv("PATH", strings.Join(append(dirs, "/usr/bin", "/bin"), string(os.PathListSeparator)))
	ForgetVersion("codex")
	t.Cleanup(func() { ForgetVersion("codex") })
}

func TestDetectCLIInstallationAppBundle(t *testing.T) {
	got := detectCLIInstallation("/Applications/ChatGPT.app/Contents/Resources/codex-cli/bin/codex", "@openai/codex", "darwin")
	want := cliInstallation{Method: cliInstallAppBundle, Prefix: filepath.FromSlash("/Applications/ChatGPT.app")}
	if got != want {
		t.Fatalf("ChatGPT bundle = %+v, want %+v", got, want)
	}
	// An app installed by a Homebrew cask is still upgraded by Homebrew.
	got = detectCLIInstallation("/opt/homebrew/Caskroom/foo/1.0/Foo.app/Contents/MacOS/foo", "", "darwin")
	if got.Method != cliInstallHomebrewCask || got.Token != "foo" {
		t.Fatalf("cask app = %+v", got)
	}
	codex, _ := Lookup("codex")
	if _, ok, err := cliUpdateCommandFor(codex, want, nil); ok || err == nil || !strings.Contains(err.Error(), "ChatGPT.app") {
		t.Fatalf("app bundle update command: ok=%v err=%v", ok, err)
	}
}

// Codex linked from ChatGPT.app is updated by ChatGPT. AgentMux must not run
// `codex update` and then npm, which installs a copy the link never reaches,
// so the version could never advance.
func TestUpdateAppBundledCodexExplainsInsteadOfRunningNPM(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	mockFrameworkLatestVersion(t, "codex", "0.171.0")
	root := t.TempDir()
	bundled := filepath.Join(root, "ChatGPT.app", "Contents", "Resources", "codex-cli", "bin")
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{bundled, bin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(root, "ran")
	writeFrameworkExecutable(t, filepath.Join(bundled, "codex"), "#!/bin/sh\n"+
		"if [ \"$1\" = update ]; then touch '"+marker+"'; exit 1; fi\necho 'codex-cli 0.169.1'\n")
	writeFrameworkExecutable(t, filepath.Join(bin, "npm"), "#!/bin/sh\ntouch '"+marker+"'\n")
	if err := os.Symlink(filepath.Join(bundled, "codex"), filepath.Join(bin, "codex")); err != nil {
		t.Fatal(err)
	}
	isolateFrameworkPath(t, bin)

	check := CheckUpdate(context.Background(), "codex")
	if check.ManagedBy != "ChatGPT.app" || !check.UpdateAvailable || check.Error != "" {
		t.Fatalf("check = %+v", check)
	}
	res := UpdateWithProgress(context.Background(), "codex", nil)
	if res.OK || !strings.Contains(res.Error, "bundled with ChatGPT.app") {
		t.Fatalf("update = %+v", res)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("update ran a command for an app-bundled CLI")
	}
}

// An npm install that lost its platform package cannot report a version.
// The check offers a repair, and the update reinstalls into the same prefix.
func TestRepairBrokenNPMCodex(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script and npm layout test")
	}
	mockFrameworkLatestVersion(t, "codex", "0.171.0")
	root := t.TempDir()
	prefix := filepath.Join(root, "node")
	versionFile := filepath.Join(root, "version")
	if err := os.WriteFile(versionFile, []byte("broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pkgBin := filepath.Join(prefix, "lib", "node_modules", "@openai", "codex", "bin")
	if err := os.MkdirAll(pkgBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFrameworkExecutable(t, filepath.Join(pkgBin, "codex.js"), "#!/bin/sh\n"+
		"read version < '"+versionFile+"'\n"+
		"if [ \"$version\" = broken ]; then echo 'Error: Missing optional dependency @openai/codex-linux-x64. Reinstall Codex: npm install -g @openai/codex@latest' >&2; exit 1; fi\n"+
		"echo \"codex-cli $version\"\n")
	if err := os.Symlink(filepath.Join("..", "lib", "node_modules", "@openai", "codex", "bin", "codex.js"), filepath.Join(prefix, "bin", "codex")); err != nil {
		t.Fatal(err)
	}
	resolvedPrefix, err := filepath.EvalSymlinks(prefix)
	if err != nil {
		t.Fatal(err)
	}
	tools := filepath.Join(root, "tools")
	if err := os.MkdirAll(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFrameworkExecutable(t, filepath.Join(tools, "npm"), "#!/bin/sh\n"+
		"if [ \"$*\" = 'install -g @openai/codex@latest --prefix="+resolvedPrefix+"' ]; then echo 0.171.0 > '"+versionFile+"'; exit 0; fi\n"+
		"echo \"unexpected npm $*\" >&2; exit 2\n")
	isolateFrameworkPath(t, filepath.Join(prefix, "bin"), tools)

	check := CheckUpdate(context.Background(), "codex")
	if !check.Repairable || !strings.Contains(check.Error, "Missing optional dependency") {
		t.Fatalf("check = %+v", check)
	}
	res := UpdateWithProgress(context.Background(), "codex", nil)
	if !res.OK || res.Version != "0.171.0" || !strings.Contains(res.Log, "Reinstalling Codex") {
		t.Fatalf("repair = %+v", res)
	}
}

// The first run after an update can be slow or fail once (macOS scans a new
// native binary before it may start). Verification tries again instead of
// reporting that the version could not be verified.
func TestUpdateVerificationRetriesFirstRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	mockFrameworkLatestVersion(t, "codex", "0.171.0")
	previousDelay := verifyVersionDelay
	verifyVersionDelay = 10 * time.Millisecond
	t.Cleanup(func() { verifyVersionDelay = previousDelay })
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	versionFile := filepath.Join(root, "version")
	firstRun := filepath.Join(root, "first-run")
	if err := os.WriteFile(versionFile, []byte("0.169.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// `codex update` installs the new version; its first --version run fails.
	writeFrameworkExecutable(t, filepath.Join(bin, "codex"), "#!/bin/sh\n"+
		"if [ \"$1\" = update ]; then echo 0.171.0 > '"+versionFile+"'; touch '"+firstRun+"'; exit 0; fi\n"+
		"if [ -e '"+firstRun+"' ]; then rm '"+firstRun+"'; echo 'killed' >&2; exit 137; fi\n"+
		"read version < '"+versionFile+"'\necho \"codex-cli $version\"\n")
	isolateFrameworkPath(t, bin)

	res := UpdateWithProgress(context.Background(), "codex", nil)
	if !res.OK || res.Version != "0.171.0" {
		t.Fatalf("update = %+v", res)
	}
}

func TestStaleNPMRetiredDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX paths")
	}
	log := "npm error ENOTEMPTY: directory not empty, rename '/h/.local/lib/node_modules/@openai/codex' -> '/h/.local/lib/node_modules/@openai/.codex-jiwhAiYz'\n"
	if got, ok := staleNPMRetiredDir(log); !ok || got != "/h/.local/lib/node_modules/@openai/.codex-jiwhAiYz" {
		t.Fatalf("stale dir = %q, %v", got, ok)
	}
	for _, bad := range []string{
		"npm error ENOTEMPTY: directory not empty, rename '/a/node_modules/codex' -> '/b/node_modules/.codex-jiwhAiYz'",
		"npm error ENOTEMPTY: directory not empty, rename '/a/node_modules/codex' -> '/a/node_modules/codex-old'",
		"npm error ENOTEMPTY: directory not empty, rename '/a/node_modules/codex' -> '/a/node_modules/.other-jiwhAiYz'",
		"npm error ENOTEMPTY: directory not empty, rename 'codex' -> '.codex-jiwhAiYz'",
		"npm error EACCES",
	} {
		if got, ok := staleNPMRetiredDir(bad); ok {
			t.Fatalf("accepted %q as %q", bad, got)
		}
	}
}

// An interrupted npm run leaves .codex-XXXXXXXX behind, and npm fails every
// later install with ENOTEMPTY. The update removes that leftover and retries.
func TestNPMUpdateRetriesAfterStaleRetiredFolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	root := t.TempDir()
	scope := filepath.Join(root, "lib", "node_modules", "@openai")
	stale := filepath.Join(scope, ".codex-jiwhAiYz")
	if err := os.MkdirAll(filepath.Join(stale, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	tools := filepath.Join(root, "tools")
	if err := os.MkdirAll(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFrameworkExecutable(t, filepath.Join(tools, "npm"), "#!/bin/sh\n"+
		"if [ -d '"+stale+"' ]; then echo \"npm error ENOTEMPTY: directory not empty, rename '"+filepath.Join(scope, "codex")+"' -> '"+stale+"'\" >&2; exit 217; fi\n"+
		"echo 'changed 2 packages'\n")
	t.Setenv("PATH", tools+string(os.PathListSeparator)+"/usr/bin:/bin")

	out, err := runCLIUpdateCommand(context.Background(), []string{"npm", "install", "-g", "@openai/codex@latest"}, nil)
	if err != nil || !strings.Contains(string(out), "changed 2 packages") || !strings.Contains(string(out), "interrupted npm run") {
		t.Fatalf("output %q, err %v", out, err)
	}
	if _, statErr := os.Stat(stale); !os.IsNotExist(statErr) {
		t.Fatalf("stale folder still present: %v", statErr)
	}
}
