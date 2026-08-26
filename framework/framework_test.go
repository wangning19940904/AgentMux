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
		"claudecode": KindCLI,
		"traecli":    KindCLI,
		"deepagents": KindSDK,
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
	for _, removed := range []string{"claude-agent-sdk", "openai-agents"} {
		if _, ok := Lookup(removed); ok {
			t.Fatalf("removed Node SDK %q is still catalogued", removed)
		}
	}
}

func TestCatalogOmitsHiddenFrameworks(t *testing.T) {
	visible := map[string]bool{}
	for _, spec := range Catalog() {
		visible[spec.Kind] = true
	}
	for _, kind := range []string{"iflow", "kimi"} {
		if visible[kind] {
			t.Fatalf("hidden framework %q is present in the public catalog", kind)
		}
		spec, ok := Lookup(kind)
		if !ok || !spec.Hidden {
			t.Fatalf("hidden framework %q is unavailable for persisted configuration compatibility", kind)
		}
	}
}

func TestTraeCatalogIsInternalAndPlatformLimited(t *testing.T) {
	spec, ok := Lookup("traecli")
	if !ok || !spec.InternalOnly || !spec.InstallSupported || !spec.UpdateSupported || spec.Bin != "traecli" {
		t.Fatalf("TRAE spec = %+v", spec)
	}
	if !installPlatformSupported(spec, "darwin") || !installPlatformSupported(spec, "linux") || installPlatformSupported(spec, "windows") {
		t.Fatalf("TRAE install platforms = %v", spec.InstallPlatforms)
	}
	if got := strings.Join(spec.UpdateCommand, " "); got != "traecli update" {
		t.Fatalf("TRAE update command = %q", got)
	}
}

func TestTraeInstallRequiresInternalAcknowledgement(t *testing.T) {
	res := Install(context.Background(), "traecli")
	if res.OK || !strings.Contains(res.Error, "explicit acknowledgement") {
		t.Fatalf("install result = %+v", res)
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

func TestIsInstalledCLIOnlyChecksExecutableAvailability(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	home := t.TempDir()
	bin := t.TempDir()
	writeFrameworkExecutable(t, filepath.Join(bin, "codex"), "#!/bin/sh\nexit 9\n")
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin)
	t.Setenv("NVM_DIR", filepath.Join(home, ".nvm-missing"))
	t.Setenv("PNPM_HOME", filepath.Join(home, ".pnpm-missing"))

	if !IsInstalled("codex") {
		t.Fatal("executable Codex runtime was reported as not installed")
	}
	if IsInstalled("claudecode") {
		t.Fatal("missing Claude Code runtime was reported as installed")
	}
	if IsInstalled("does-not-exist") {
		t.Fatal("unknown runtime was reported as installed")
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

func TestCheckAuthDistinguishesLoggedInAndLoggedOutCLIs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	home := t.TempDir()
	bin := t.TempDir()
	writeFrameworkExecutable(t, filepath.Join(bin, "claude"), `#!/bin/sh
if [ "$1 $2 $3" = "auth status --json" ]; then
  printf '%s\n' '{"loggedIn":true,"email":"must-not-leak@example.test"}'
  exit 0
fi
exit 2
`)
	writeFrameworkExecutable(t, filepath.Join(bin, "codex"), `#!/bin/sh
if [ "$1 $2" = "login status" ]; then
  echo 'Not logged in'
  exit 1
fi
exit 2
`)
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin)
	t.Setenv("NVM_DIR", filepath.Join(home, ".nvm-missing"))
	t.Setenv("PNPM_HOME", filepath.Join(home, ".pnpm-missing"))
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("CODEX_API_KEY", "")

	claude := CheckAuth(context.Background(), "claudecode")
	if claude.State != AuthStateAuthenticated || !claude.LoginSupported || strings.Contains(claude.Detail, "example.test") {
		t.Fatalf("Claude auth status = %+v", claude)
	}
	codex := CheckAuth(context.Background(), "codex")
	if codex.State != AuthStateUnauthenticated || !codex.LoginSupported {
		t.Fatalf("Codex auth status = %+v", codex)
	}
}

func TestCheckTraeAuthUsesNativeStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	bin := t.TempDir()
	writeFrameworkExecutable(t, filepath.Join(bin, "traecli"), `#!/bin/sh
if [ "$1" = "login" ] && [ "$2" = "status" ]; then
  echo 'Logged in using Trae'
  exit 0
fi
exit 2
`)
	t.Setenv("PATH", bin)
	status := CheckAuth(context.Background(), "traecli")
	if status.State != AuthStateAuthenticated || !status.LoginSupported {
		t.Fatalf("TRAE auth = %+v", status)
	}
}

func TestStartLoginReturnsBrowserURLAndVerificationCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	home := t.TempDir()
	bin := t.TempDir()
	writeFrameworkExecutable(t, filepath.Join(bin, "codex"), `#!/bin/sh
if [ "$1 $2" = "login --device-auth" ]; then
  echo 'Open https://auth.example.test/device'
  printf 'Code: \033[94mABCD-EFGH\033[0m\n'
  exit 0
fi
exit 2
`)
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin)
	t.Setenv("NVM_DIR", filepath.Join(home, ".nvm-missing"))
	t.Setenv("PNPM_HOME", filepath.Join(home, ".pnpm-missing"))

	result, err := StartLogin("codex")
	if err != nil {
		t.Fatal(err)
	}
	if result.LoginURL != "https://auth.example.test/device" || result.VerificationCode != "ABCD-EFGH" || result.SessionID == "" {
		t.Fatalf("login result = %+v", result)
	}
}

func TestCompleteLoginWritesPastedCodeToWaitingCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	home := t.TempDir()
	bin := t.TempDir()
	writeFrameworkExecutable(t, filepath.Join(bin, "claude"), `#!/bin/sh
if [ "$1 $2" = "auth login" ]; then
  echo 'Visit https://claude.example.test/oauth'
  read code
  [ "$code" = "returned-code" ]
  exit $?
fi
exit 2
`)
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin)
	t.Setenv("NVM_DIR", filepath.Join(home, ".nvm-missing"))
	t.Setenv("PNPM_HOME", filepath.Join(home, ".pnpm-missing"))

	result, err := StartLogin("claudecode")
	if err != nil {
		t.Fatal(err)
	}
	if !result.InputRequired || result.LoginURL != "https://claude.example.test/oauth" {
		t.Fatalf("login result = %+v", result)
	}
	if err := CompleteLogin(result.SessionID, "returned-code"); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeSDKVersionIgnoresWarningNumbers(t *testing.T) {
	raw := "WARNING: could not inspect cwd (os error 2)\ncodex-cli 0.145.0\n"
	if got := normalizeSDKVersion(raw); got != "0.145.0" {
		t.Fatalf("normalized version = %q, want 0.145.0", got)
	}
	if got := normalizeSDKVersion("failed after 30 seconds with error 2"); got != "" {
		t.Fatalf("warning-only output normalized to %q", got)
	}
}

func TestFrameworkCommandsSurviveDeletedDaemonWorkingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("deleted working directory test")
	}
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFrameworkExecutable(t, filepath.Join(bin, "codex"), "#!/bin/sh\npwd >/dev/null || exit 7\necho 'codex-cli 0.145.0'\n")
	writeFrameworkExecutable(t, filepath.Join(bin, "npm"), "#!/bin/sh\npwd >/dev/null || exit 7\necho '0.146.0'\n")
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin)

	staleDir, err := os.MkdirTemp("", "agentmux-deleted-cwd-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(staleDir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(originalDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	}()
	if err := os.Remove(staleDir); err != nil {
		t.Fatal(err)
	}

	status := Detect(Spec{Kind: "codex", KindType: KindCLI, Bin: "codex"}, Prereqs{})
	if !status.Installed || status.Version != "0.145.0" || status.Detail != "" {
		t.Fatalf("status from deleted daemon cwd = %+v", status)
	}
	latest, err := npmPackageVersion(context.Background(), "@openai/codex")
	if err != nil || latest != "0.146.0" {
		t.Fatalf("npm version from deleted daemon cwd = %q, err = %v", latest, err)
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

	var phases []string
	res := InstallWithProgress(context.Background(), "gemini", func(phase, _ string, _ int) {
		phases = append(phases, phase)
	})
	if !res.OK || res.Error != "" || res.Version != "1.2.3" || res.Command != "npm install -g @google/gemini-cli@latest" {
		t.Fatalf("install result = %+v", res)
	}
	if got := strings.Join(phases, ","); got != "preparing,preparing,installing,verifying" {
		t.Fatalf("progress phases = %q", got)
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

func TestDetectCLIsAndPrereqsFromNVMForBackgroundService(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix nvm layout test")
	}
	home := t.TempDir()
	nvmBin := filepath.Join(home, ".nvm", "versions", "node", "v24.13.0", "bin")
	if err := os.MkdirAll(nvmBin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFrameworkExecutable(t, filepath.Join(nvmBin, "node"), "#!/bin/sh\necho 'v24.13.0'\n")
	writeFrameworkExecutable(t, filepath.Join(nvmBin, "npm"), "#!/bin/sh\necho '11.6.2'\n")
	writeFrameworkExecutable(t, filepath.Join(nvmBin, "claude"), "#!/bin/sh\necho '2.1.211 (Claude Code)'\n")
	writeFrameworkExecutable(t, filepath.Join(nvmBin, "codex"), "#!/bin/sh\necho 'codex-cli 0.144.1'\n")
	t.Setenv("HOME", home)
	t.Setenv("NVM_DIR", "")
	t.Setenv("PNPM_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("PATH", t.TempDir())

	pre := DetectPrereqs()
	if !pre.Node || !pre.NPM || pre.NodePth != filepath.Join(nvmBin, "node") || pre.NPMPath != filepath.Join(nvmBin, "npm") {
		t.Fatalf("prereqs = %+v", pre)
	}
	for _, bin := range []string{"claude", "codex"} {
		if path, err := exec.LookPath(bin); err != nil || path != filepath.Join(nvmBin, bin) {
			t.Fatalf("startup PATH did not expose %s: path=%q err=%v", bin, path, err)
		}
	}
	for kind, version := range map[string]string{"claudecode": "2.1.211", "codex": "0.144.1"} {
		spec, ok := Lookup(kind)
		if !ok {
			t.Fatalf("%s missing from catalog", kind)
		}
		status := Detect(spec, pre)
		if !status.Installed || status.Version != version {
			t.Fatalf("%s status = %+v", kind, status)
		}
	}
}

func TestUpdateCodexConfiguresPNPMHomeForBackgroundService(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script and pnpm layout test")
	}
	home := t.TempDir()
	localBin := filepath.Join(home, ".local", "bin")
	pnpmHome := filepath.Join(home, ".local", "share", "pnpm")
	for _, dir := range []string{localBin, pnpmHome} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	versionFile := filepath.Join(home, "codex-version")
	if err := os.WriteFile(versionFile, []byte("0.145.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFrameworkExecutable(t, filepath.Join(pnpmHome, "codex"), "#!/bin/sh\n"+
		"if [ \"$1\" = \"update\" ]; then\n"+
		"  if [ \"$PNPM_HOME\" != '"+pnpmHome+"' ]; then echo 'PNPM_HOME missing' >&2; exit 8; fi\n"+
		"  echo '0.146.0' > '"+versionFile+"'\n"+
		"  echo updated\n"+
		"  exit 0\n"+
		"fi\n"+
		"read version < '"+versionFile+"'\n"+
		"printf '%s\\n' \"$version\"\n")
	writeFrameworkExecutable(t, filepath.Join(localBin, "npm"), "#!/bin/sh\nif [ \"$1\" = \"view\" ]; then echo '0.146.0'; exit 0; fi\nexit 1\n")
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("PNPM_HOME", "")
	t.Setenv("PATH", t.TempDir())

	res := Update(context.Background(), "codex")
	if !res.OK || res.Error != "" || res.Version != "0.146.0" || res.Command != "codex update" {
		t.Fatalf("update result = %+v", res)
	}
	if got := os.Getenv("PNPM_HOME"); got != pnpmHome {
		t.Fatalf("PNPM_HOME = %q, want %q", got, pnpmHome)
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
	if !frameworkUpdateAvailable(Spec{LatestURL: "https://cursor.com/install", ExactLatest: true}, "2026.07.09-a3815c0", "2026.07.09-fffffff") {
		t.Fatal("different native build identifiers should be treated as an available update")
	}
}

func TestOfficialVersionFromJSONManifest(t *testing.T) {
	if got := officialVersionFromBody([]byte(`{"version":"0.201.6","channel":"stable"}`)); got != "0.201.6" {
		t.Fatalf("manifest version = %q", got)
	}
	if frameworkUpdateAvailable(Spec{}, "0.201.5", "0.201.6") {
		t.Fatal("ordered versions must not downgrade TRAE")
	}
}

func TestCursorUpdateUsesOfficialInstallerAndExactNativeBuild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	home := t.TempDir()
	localBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatal(err)
	}
	versionFile := filepath.Join(home, "cursor-version")
	if err := os.WriteFile(versionFile, []byte("2026.07.23-fffffff\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFrameworkExecutable(t, filepath.Join(localBin, "cursor-agent"), "#!/bin/sh\nread version < '"+versionFile+"'\nprintf '%s\\n' \"$version\"\n")
	fakeBin := t.TempDir()
	writeFrameworkExecutable(t, filepath.Join(fakeBin, "bash"), "#!/bin/sh\necho '2026.07.23-0000000' > '"+versionFile+"'\necho installed\n")
	t.Setenv("HOME", home)
	t.Setenv("PATH", fakeBin)

	spec, ok := Lookup("cursor")
	if !ok {
		t.Fatal("cursor missing from catalog")
	}
	res := updateCLI(context.Background(), spec, UpdateCheck{
		CurrentVersion: "2026.07.23-fffffff",
		LatestVersion:  "2026.07.23-0000000",
	})
	if !res.OK || res.Error != "" || res.Version != "2026.07.23-0000000" ||
		res.Command != "bash -c curl https://cursor.com/install -fsS | bash" {
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

func writeFrameworkExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}
