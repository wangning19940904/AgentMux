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
	if spec.Name != "GitHub CLI" || spec.Bin != "gh" || spec.Package != "gh" {
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

	res := InstallCLI(context.Background(), "github-cli", "install")
	if !res.OK || res.Command != "brew install gh" || !strings.Contains(res.Version, "2.93.0") {
		t.Fatalf("install result = %+v", res)
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

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}
