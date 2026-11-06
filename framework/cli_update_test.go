package framework

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestCLIUpdateEnvironment(t *testing.T) {
	for _, test := range []struct {
		name string
		goos string
		env  []string
		want []string
	}{
		{
			name: "Finder PATH retains wrapper precedence", goos: "darwin",
			env:  []string{"HOME=/users/test", "PATH=/usr/bin:/bin:/users/test/.local/bin", "PNPM_HOME=/pnpm"},
			want: []string{"HOME=/users/test", "PATH=/usr/bin:/bin:/users/test/.local/bin:/opt/homebrew/bin:/usr/local/bin", "PNPM_HOME=/pnpm"},
		},
		{
			name: "existing Homebrew order and aliases are preserved", goos: "darwin",
			env:  []string{"PATH=/usr/local/bin:/opt/homebrew/bin/:/bin"},
			want: []string{"PATH=/usr/local/bin:/opt/homebrew/bin/:/bin"},
		},
		{
			name: "missing PATH", goos: "darwin",
			env: []string{"HOME=/users/test"}, want: []string{"HOME=/users/test", "PATH=/opt/homebrew/bin:/usr/local/bin"},
		},
		{
			name: "Linux unchanged", goos: "linux",
			env: []string{"PATH=/bin:/custom"}, want: []string{"PATH=/bin:/custom"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if runtime.GOOS == "windows" && test.goos == "darwin" {
				t.Skip("POSIX path syntax")
			}
			before := append([]string(nil), test.env...)
			got := cliUpdateEnvironment(test.env, test.goos)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("update environment = %v, want %v", got, test.want)
			}
			if !reflect.DeepEqual(test.env, before) {
				t.Fatalf("modified caller environment: %v", test.env)
			}
		})
	}
}

func TestUpdateCodexWrapperReceivesHomebrewPathFromDesktop(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS desktop updater environment")
	}
	mockFrameworkLatestVersion(t, "codex", "0.166.0")
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	versionFile := filepath.Join(home, "version")
	if err := os.WriteFile(versionFile, []byte("0.159.2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A regular wrapper defeats symlink-based Homebrew detection. Its native
	// updater still needs brew on PATH, just as a Finder-launched app does.
	writeFrameworkExecutable(t, filepath.Join(bin, "codex"), "#!/bin/sh\n"+
		"if [ \"$1\" = update ]; then\n"+
		"  case \":$PATH:\" in *:/opt/homebrew/bin:*) ;; *) echo 'No such file or directory (os error 2)' >&2; exit 1;; esac\n"+
		"  case \":$PATH:\" in *:/usr/local/bin:*) ;; *) exit 2;; esac\n"+
		"  echo '0.166.0' > '"+versionFile+"'\n"+
		"  exit 0\n"+
		"fi\n"+
		"read version < '"+versionFile+"'\n"+
		"echo \"codex-cli $version\"\n")
	writeFrameworkExecutable(t, filepath.Join(bin, "npm"), "#!/bin/sh\nif [ \"$1\" = view ]; then echo '0.166.0'; exit 0; fi\nexit 9\n")
	t.Setenv("HOME", home)
	t.Setenv("NVM_DIR", "")
	t.Setenv("PNPM_HOME", "")
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin:"+bin)
	res := UpdateWithProgress(context.Background(), "codex", nil)
	if !res.OK || res.Version != "0.166.0" || res.Command != "codex update" {
		t.Fatalf("update from desktop environment = %+v", res)
	}
	if strings.Contains(os.Getenv("PATH"), "/opt/homebrew/bin") || strings.Contains(os.Getenv("PATH"), "/usr/local/bin") {
		t.Fatalf("update changed the parent search path: %s", os.Getenv("PATH"))
	}
}

func mockFrameworkLatestVersion(t *testing.T, kind, version string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"version":%q}`, version)
	}))
	t.Cleanup(server.Close)
	for i := range catalog {
		if catalog[i].Kind == kind {
			previous := catalog[i].LatestURL
			catalog[i].LatestURL = server.URL
			t.Cleanup(func() { catalog[i].LatestURL = previous })
			return
		}
	}
	t.Fatalf("%s catalog entry missing", kind)
}

// A registry mirror can serve a new @deepseek-ai/dsh before the exact-pinned
// @deepseek-ai/* siblings it depends on, failing with ETARGET or a tarball
// E404. Install and update must resolve and download that scope from the
// publisher (npm otherwise rewrites npmjs tarball URLs to the configured
// registry), and the version check must read the same registry so it never
// offers a release the installer can't get.
func TestDSHResolvesDeepSeekScopeFromPublisherRegistry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	spec, ok := Lookup("dsh")
	if !ok || !strings.HasPrefix(spec.LatestURL, "https://registry.npmjs.org/") {
		t.Fatalf("dsh latest-version source = %q", spec.LatestURL)
	}
	mockFrameworkLatestVersion(t, "dsh", "0.2.1-rc.5")

	bin := t.TempDir()
	versionFile := filepath.Join(bin, "dsh-version")
	writeFrameworkExecutable(t, filepath.Join(bin, "dsh-template"), "#!/bin/sh\ncat '"+versionFile+"'\n")
	npmScript := "#!/bin/sh\n" +
		"case \" $* \" in *' install -g @deepseek-ai/dsh@latest --@deepseek-ai:registry=https://registry.npmjs.org/ --replace-registry-host=never '*) ;; *) echo \"unexpected npm $*\" >&2; exit 2 ;; esac\n" +
		"echo \"$NEXT_DSH_VERSION\" > '" + versionFile + "'\n" +
		"cp '" + filepath.Join(bin, "dsh-template") + "' '" + filepath.Join(bin, "dsh") + "'\n"
	writeFrameworkExecutable(t, filepath.Join(bin, "npm"), npmScript)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("NVM_DIR", "")
	t.Setenv("PNPM_HOME", "")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin:/bin")

	const want = "npm install -g @deepseek-ai/dsh@latest --@deepseek-ai:registry=https://registry.npmjs.org/ --replace-registry-host=never"
	t.Setenv("NEXT_DSH_VERSION", "0.2.1-rc.4")
	res := InstallWithProgressOptions(context.Background(), "dsh", InstallOptions{}, nil)
	if !res.OK || res.Command != want || res.Version != "0.2.1-rc.4" {
		t.Fatalf("dsh install = %+v", res)
	}

	t.Setenv("NEXT_DSH_VERSION", "0.2.1-rc.5")
	res = UpdateWithProgress(context.Background(), "dsh", nil)
	if !res.OK || res.Command != want || res.Version != "0.2.1-rc.5" {
		t.Fatalf("dsh update = %+v", res)
	}
}

func TestCheckCodexUpdateWithoutNPM(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	mockFrameworkLatestVersion(t, "codex", "0.166.0")
	home := t.TempDir()
	bin := t.TempDir()
	writeFrameworkExecutable(t, filepath.Join(bin, "codex"), "#!/bin/sh\necho 'codex-cli 0.161.0'\n")
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin)
	t.Setenv("NVM_DIR", "")
	t.Setenv("PNPM_HOME", "")
	if DetectPrereqs().NPM {
		t.Fatal("fixture unexpectedly has npm")
	}
	check := CheckUpdate(context.Background(), "codex")
	if check.Error != "" || !check.UpdateAvailable || check.CurrentVersion != "0.161.0" || check.LatestVersion != "0.166.0" {
		t.Fatalf("standalone Codex update check = %+v", check)
	}
	// Even when npm is present, its configured registry must not control the
	// latest version of a native Codex installation.
	writeFrameworkExecutable(t, filepath.Join(bin, "npm"), "#!/bin/sh\necho 'private registry unavailable' >&2\nexit 9\n")
	check = CheckUpdate(context.Background(), "codex")
	if check.Error != "" || check.LatestVersion != "0.166.0" {
		t.Fatalf("Codex check with unavailable private registry = %+v", check)
	}
}
