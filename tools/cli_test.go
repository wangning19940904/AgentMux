package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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

func TestInstallAgentBrowserRunsBrowserSetup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	bin := t.TempDir()
	setupMarker := filepath.Join(bin, "browser-installed")
	agentBrowser := filepath.Join(bin, "agent-browser")
	writeExecutable(t, filepath.Join(bin, "npm"), "#!/bin/sh\ncat > '"+agentBrowser+"' <<'EOS'\n#!/bin/sh\nif [ \"$1\" = \"install\" ]; then touch '"+setupMarker+"'; echo browser-installed; exit 0; fi\necho 'agent-browser 1.2.3'\nEOS\nchmod +x '"+agentBrowser+"'\necho cli-installed\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	res := InstallCLI(context.Background(), "agent-browser", "install")
	if !res.OK || !strings.Contains(res.Version, "1.2.3") {
		t.Fatalf("install result = %+v", res)
	}
	if res.Command != "npm install -g agent-browser@latest && agent-browser install" {
		t.Fatalf("command = %q", res.Command)
	}
	if _, err := os.Stat(setupMarker); err != nil {
		t.Fatalf("browser setup did not run: %v", err)
	}
}

func TestCLICatalogIncludesAgentBrowser(t *testing.T) {
	spec, ok := LookupCLI("agent-browser")
	if !ok {
		t.Fatal("agent-browser missing from CLI catalog")
	}
	if spec.Bin != "agent-browser" || spec.Package != "agent-browser" {
		t.Fatalf("spec = %+v", spec)
	}
	if strings.Join(spec.PostInstallCommand, " ") != "agent-browser install" {
		t.Fatalf("post-install command = %v", spec.PostInstallCommand)
	}
}

func TestCLICatalogIncludesCISCLIWithVersionMatchedSkill(t *testing.T) {
	spec, ok := LookupCLI("cis-cli")
	if !ok {
		t.Fatal("cis-cli missing from CLI catalog")
	}
	if spec.Bin != "cis-cli" || spec.Package != "@byted/cis-cli" || spec.Registry != "https://bnpm.byted.org/" {
		t.Fatalf("spec = %+v", spec)
	}
	if len(spec.LinkedSkills) != 1 {
		t.Fatalf("linked skills = %+v", spec.LinkedSkills)
	}
	linked := spec.LinkedSkills[0]
	if linked.ID != "cis-cli" || !linked.MatchCLIVersion || linked.Source != "skills.byted.org/default/public/cis-cli" {
		t.Fatalf("linked skill = %+v", linked)
	}
	if strings.Join(linked.InstallCommand, " ") != "cis-cli install-skills --dir {agentmux_skills_dir} --force" {
		t.Fatalf("skill install command = %v", linked.InstallCommand)
	}
}

func TestCLICatalogIncludesGitHubCLI(t *testing.T) {
	spec, ok := LookupCLI("github-cli")
	if !ok {
		t.Fatal("github-cli missing from CLI catalog")
	}
	if spec.Name != "GitHub CLI" || spec.Bin != "gh" || spec.Package != "gh" || !spec.LoginSupported {
		t.Fatalf("spec = %+v", spec)
	}
	if strings.Join(spec.InstallCommand, " ") != "brew install gh" {
		t.Fatalf("install command = %v", spec.InstallCommand)
	}
	if strings.Join(spec.UpdateCommand, " ") != "brew upgrade gh" {
		t.Fatalf("update command = %v", spec.UpdateCommand)
	}
	if spec.LatestVersionURL != "https://api.github.com/repos/cli/cli/releases/latest" {
		t.Fatalf("latest version URL = %q", spec.LatestVersionURL)
	}
}

func TestCLICatalogMarksLarkLoginSupported(t *testing.T) {
	spec, ok := LookupCLI("lark-cli")
	if !ok || !spec.LoginSupported {
		t.Fatalf("lark-cli spec = %+v, ok = %v", spec, ok)
	}
}

func TestCLIAuthWorkflowChainsLarkSetupAndLogin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	home := t.TempDir()
	bin := t.TempDir()
	state := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLI_AUTH_TEST_DIR", state)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeExecutable(t, filepath.Join(bin, "lark-cli"), `#!/bin/sh
state="$CLI_AUTH_TEST_DIR"
if [ "$1" = "--version" ]; then
  echo 'lark-cli version 1.0.85'
  exit 0
fi
if [ "$1" = "auth" ] && [ "$2" = "status" ]; then
  if [ -f "$state/authenticated" ]; then
    echo '{"appId":"cli_test","identities":{"bot":{"status":"ready","available":true},"user":{"status":"ready","available":true}}}'
    exit 0
  fi
  if [ -f "$state/configured" ]; then
    echo '{"appId":"cli_test","identities":{"bot":{"status":"ready","available":true},"user":{"status":"not_logged_in","available":false}}}'
    exit 3
  fi
  echo '{"ok":false,"error":{"type":"config","subtype":"not_configured"}}'
  exit 3
fi
if [ "$1" = "config" ] && [ "$2" = "init" ]; then
  echo 'Open https://open.feishu.cn/page/cli?user_code=INIT-CODE to configure'
  while [ ! -f "$state/setup-approved" ]; do sleep 0.05; done
  touch "$state/configured"
  exit 0
fi
if [ "$1" = "auth" ] && [ "$2" = "login" ]; then
  echo '{"verification_uri":"https://open.feishu.cn/device","user_code":"AUTH-CODE"}'
  while [ ! -f "$state/login-approved" ]; do sleep 0.05; done
  touch "$state/authenticated"
  exit 0
fi
exit 2
`)

	before := CheckCLIAuth(context.Background(), "lark-cli")
	if before.State != CLIAuthSetupRequired || !before.LoginSupported {
		t.Fatalf("initial auth status = %+v", before)
	}

	session, err := StartCLIAuth("lark-cli", false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = CancelCLIAuthSession(session.SessionID) }()
	if session.Phase != "setup" || !strings.Contains(session.LoginURL, "open.feishu.cn/page/cli") || session.VerificationCode != "INIT-CODE" {
		t.Fatalf("setup session = %+v", session)
	}
	if err := os.WriteFile(filepath.Join(state, "setup-approved"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}

	login := waitForCLIAuthTest(t, session.SessionID, func(snapshot CLIAuthSession) bool {
		return snapshot.Phase == "login" && snapshot.State == CLIAuthSessionWaiting && strings.Contains(snapshot.LoginURL, "/device")
	})
	if login.VerificationCode != "AUTH-CODE" {
		t.Fatalf("login session = %+v", login)
	}
	if err := os.WriteFile(filepath.Join(state, "login-approved"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitForCLIAuthTest(t, session.SessionID, func(snapshot CLIAuthSession) bool {
		return snapshot.State == CLIAuthSessionSucceeded
	})
	after := CheckCLIAuth(context.Background(), "lark-cli")
	if after.State != CLIAuthAuthenticated {
		t.Fatalf("final auth status = %+v", after)
	}
}

func TestClassifyGitHubCLIAuth(t *testing.T) {
	if got := classifyGitHubCLIAuth([]byte("Logged in to github.com account octocat\nActive account: true"), nil); got != CLIAuthAuthenticated {
		t.Fatalf("authenticated classification = %q", got)
	}
	if got := classifyGitHubCLIAuth([]byte("You are not logged into any GitHub hosts"), fmt.Errorf("exit 1")); got != CLIAuthUnauthenticated {
		t.Fatalf("unauthenticated classification = %q", got)
	}
}

func waitForCLIAuthTest(t *testing.T, sessionID string, ready func(CLIAuthSession) bool) CLIAuthSession {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, ok := GetCLIAuthSession(sessionID)
		if !ok {
			t.Fatalf("auth session %s disappeared", sessionID)
		}
		if ready(snapshot) {
			return snapshot
		}
		if snapshot.State == CLIAuthSessionFailed || snapshot.State == CLIAuthSessionCancelled {
			t.Fatalf("auth session stopped early: %+v", snapshot)
		}
		time.Sleep(25 * time.Millisecond)
	}
	snapshot, _ := GetCLIAuthSession(sessionID)
	t.Fatalf("timed out waiting for auth session: %+v", snapshot)
	return CLIAuthSession{}
}

func TestInstallGitHubCLIUsesHomebrew(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	bin := t.TempDir()
	gh := filepath.Join(bin, "gh")
	writeExecutable(t, filepath.Join(bin, "brew"), fmt.Sprintf(`#!/bin/sh
if [ "$1" != "install" ] || [ "$2" != "gh" ]; then
  exit 2
fi
cat > %q <<'EOS'
#!/bin/sh
echo 'gh version 2.93.0 (test)'
EOS
chmod +x %q
echo installed
`, gh, gh))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	var phases []string
	res := InstallCLIWithProgress(context.Background(), "github-cli", "install", func(phase, _ string, _ int) {
		phases = append(phases, phase)
	})
	if !res.OK || res.Command != "brew install gh" || !strings.Contains(res.Version, "2.93.0") {
		t.Fatalf("install result = %+v", res)
	}
	if got := strings.Join(phases, ","); got != "preparing,installing,verifying" {
		t.Fatalf("progress phases = %q", got)
	}
}

func TestInstallGitHubCLIFindsHomebrewOutsideServicePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	prefix := t.TempDir()
	brewBin := filepath.Join(prefix, "bin")
	if err := os.MkdirAll(brewBin, 0o755); err != nil {
		t.Fatal(err)
	}
	gh := filepath.Join(brewBin, "gh")
	writeExecutable(t, filepath.Join(brewBin, "brew"), fmt.Sprintf(`#!/bin/sh
if [ "$1" != "install" ] || [ "$2" != "gh" ]; then
  exit 2
fi
/bin/cat > %q <<'EOS'
#!/bin/sh
echo 'gh version 2.93.0 (test)'
EOS
/bin/chmod +x %q
echo installed
`, gh, gh))
	t.Setenv("HOMEBREW_PREFIX", prefix)
	t.Setenv("PATH", t.TempDir())

	res := InstallCLI(context.Background(), "github-cli", "install")
	if !res.OK || res.Command != "brew install gh" || !strings.Contains(res.Version, "2.93.0") {
		t.Fatalf("install result = %+v", res)
	}
	if status := DetectCLI(context.Background(), CLISpec{ID: "github-cli", Bin: "gh"}); !status.Installed || status.Path != gh {
		t.Fatalf("status = %+v", status)
	}
}

func TestLatestCLIVersionUsesReleaseEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v2.94.0"}`))
	}))
	defer server.Close()

	got, err := latestCLIVersion(context.Background(), CLISpec{LatestVersionURL: server.URL})
	if err != nil || got != "v2.94.0" {
		t.Fatalf("latest version = %q, err = %v", got, err)
	}
}

func TestDetectCLIReportsLinkedSkillVersionDrift(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	home := t.TempDir()
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "cis-cli"), "#!/bin/sh\necho '0.41.0'\n")
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	skillDir := filepath.Join(home, ".agentmux", "tools", "skills", "cis-cli")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: cis-cli\nversion: \"0.40.0\"\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	spec, _ := LookupCLI("cis-cli")
	st := DetectCLI(context.Background(), spec)
	if len(st.LinkedSkills) != 1 || !st.LinkedSkills[0].Installed || st.LinkedSkills[0].InSync {
		t.Fatalf("status = %+v", st)
	}
	if !strings.Contains(st.LinkedSkills[0].Detail, "does not match") {
		t.Fatalf("detail = %q", st.LinkedSkills[0].Detail)
	}
}

func TestSyncCLILinkedSkillsInstallsIntoAgentMuxLibrary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	home := t.TempDir()
	bin := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeExecutable(t, filepath.Join(bin, "cis-cli"), `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo '0.41.0'
  exit 0
fi
if [ "$1" = "install-skills" ] && [ "$2" = "--dir" ]; then
  mkdir -p "$3/cis-cli"
  printf '%s\n' '---' 'name: cis-cli' 'version: "0.41.0"' '---' > "$3/cis-cli/SKILL.md"
  echo 'skills-synced'
  exit 0
fi
exit 1
`)

	res := SyncCLILinkedSkills(context.Background(), "cis-cli")
	if !res.OK || res.Action != "sync-skills" || len(res.LinkedSkills) != 1 || !res.LinkedSkills[0].OK {
		t.Fatalf("sync result = %+v", res)
	}
	wantDir := filepath.Join(home, ".agentmux", "tools", "skills")
	if !strings.Contains(res.Command, "cis-cli install-skills --dir "+wantDir+" --force") {
		t.Fatalf("command = %q", res.Command)
	}
	if res.LinkedSkills[0].Version != "0.41.0" {
		t.Fatalf("linked skill = %+v", res.LinkedSkills[0])
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

func TestInternalCLIsRequireAcknowledgement(t *testing.T) {
	for _, id := range []string{"bytedcli", "cis-cli"} {
		spec, ok := LookupCLI(id)
		if !ok || !spec.InternalOnly {
			t.Fatalf("internal CLI spec %q = %+v", id, spec)
		}
		res := InstallCLI(context.Background(), id, "install")
		if res.OK || !strings.Contains(res.Error, "explicit acknowledgement") {
			t.Fatalf("install %s = %+v", id, res)
		}
	}
}

func TestByteDanceBundleInstallsAllMissingComponents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	prepareInternalBundleInstall(t, false)

	res := InstallBundle(context.Background(), "bytedance-internal", BundleInstallOptions{AcknowledgeInternal: true})
	if !res.OK || res.Error != "" || len(res.Components) != 3 {
		t.Fatalf("bundle result = %+v", res)
	}
	for _, component := range res.Components {
		if !component.OK || component.Skipped {
			t.Fatalf("component = %+v", component)
		}
	}
	status, _ := LookupBundle("bytedance-internal")
	detected := DetectBundle(context.Background(), status)
	if !detected.Installed || detected.ReadyComponents != 3 {
		t.Fatalf("bundle status = %+v", detected)
	}

	retry := InstallBundle(context.Background(), "bytedance-internal", BundleInstallOptions{AcknowledgeInternal: true})
	if !retry.OK {
		t.Fatalf("retry = %+v", retry)
	}
	for _, component := range retry.Components {
		if !component.Skipped {
			t.Fatalf("ready component was not skipped: %+v", component)
		}
	}
}

func TestByteDanceBundleContinuesAfterComponentFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	prepareInternalBundleInstall(t, true)
	res := InstallBundle(context.Background(), "bytedance-internal", BundleInstallOptions{AcknowledgeInternal: true})
	if res.OK || len(res.Components) != 3 || res.Components[0].OK || !res.Components[1].OK || !res.Components[2].OK {
		t.Fatalf("partial bundle result = %+v", res)
	}
}

func TestByteDanceBundleRejectsMissingAcknowledgementAndWindows(t *testing.T) {
	res := InstallBundle(context.Background(), "bytedance-internal", BundleInstallOptions{})
	if res.OK || !strings.Contains(res.Error, "explicit acknowledgement") {
		t.Fatalf("unacknowledged result = %+v", res)
	}
	spec, _ := LookupBundle("bytedance-internal")
	if bundlePlatformSupported(spec, "windows") || !strings.Contains(bundlePlatformError(spec, "windows"), "WSL") {
		t.Fatalf("Windows platform policy = %+v", spec)
	}
}

func prepareInternalBundleInstall(t *testing.T, failByted bool) {
	t.Helper()
	home := t.TempDir()
	bin := t.TempDir()
	bytedPath := filepath.Join(bin, "bytedcli")
	cisPath := filepath.Join(bin, "cis-cli")
	traePath := filepath.Join(bin, "traecli")
	failCommand := ""
	if failByted {
		failCommand = "echo bytedcli-install-failed >&2; exit 17"
	}
	npmScript := fmt.Sprintf(`#!/bin/sh
case " $* " in
  *" @bytedance-dev/bytedcli@latest "*)
    %s
    /bin/cat > %q <<'EOS'
#!/bin/sh
echo 'bytedcli 1.2.3'
EOS
    /bin/chmod +x %q
    ;;
  *" @byted/cis-cli@latest "*)
    /bin/cat > %q <<'EOS'
#!/bin/sh
if [ "$1" = "install-skills" ] && [ "$2" = "--dir" ]; then
  /bin/mkdir -p "$3/cis-cli"
  /usr/bin/printf '%%s\n' '---' 'name: cis-cli' 'version: "0.41.0"' '---' > "$3/cis-cli/SKILL.md"
  echo skills-synced
  exit 0
fi
echo 'cis-cli 0.41.0'
EOS
    /bin/chmod +x %q
    ;;
  *) echo '{}';;
esac
`, failCommand, bytedPath, bytedPath, cisPath, cisPath)
	writeExecutable(t, filepath.Join(bin, "npm"), npmScript)
	bashScript := fmt.Sprintf(`#!/bin/sh
/bin/cat > %q <<'EOS'
#!/bin/sh
echo 'traecli 0.201.6(internal edition)'
EOS
/bin/chmod +x %q
echo trae-installed
`, traePath, traePath)
	writeExecutable(t, filepath.Join(bin, "bash"), bashScript)
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin)
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}
