package postgressetup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const defaultURL = "postgresql:///agentmux?host=/tmp&sslmode=disable"

func TestLocalURLPreservesCustomConnections(t *testing.T) {
	for _, raw := range []string{
		"postgresql://db.example/agentmux", "postgresql://localhost/agentmux?sslmode=disable",
		"postgresql://alice@/agentmux?host=/tmp&sslmode=disable", "postgresql:///other?host=/tmp&sslmode=disable",
		defaultURL + "&port=5433", defaultURL + "&options=-csearch_path=other", defaultURL + "&host=/tmp",
		"postgresql:///agentmux?host=/custom&sslmode=disable", defaultURL + "&bad=%zz",
	} {
		resolved, managed := LocalURL(raw, "linux")
		if managed || resolved != raw {
			t.Fatalf("custom connection modified: %q -> %q (%v)", raw, resolved, managed)
		}
	}
	for _, platform := range []string{"darwin", "linux"} {
		for _, raw := range []string{defaultURL, defaultURL + "&port=5432", "postgres:///agentmux?sslmode=disable&host=%2Ftmp"} {
			resolved, managed := LocalURL(raw, platform)
			wantHost := "%2Ftmp"
			if platform == "linux" {
				wantHost = "%2Fvar%2Frun%2Fpostgresql"
			}
			if !managed || !strings.Contains(resolved, "host="+wantHost) {
				t.Fatalf("%s %s -> %s (%v)", platform, raw, resolved, managed)
			}
		}
	}
}

func TestEnsureOnlyInstallsForMissingLocalDatabase(t *testing.T) {
	for _, tc := range []struct {
		name, raw            string
		ready                bool
		probeErr, installErr error
		wantProbes, wantRuns int
		wantErr              bool
	}{
		{name: "external", raw: "postgresql://db.example/agentmux"},
		{name: "healthy", raw: defaultURL, ready: true, wantProbes: 1},
		{name: "missing", raw: defaultURL, wantProbes: 2, wantRuns: 1},
		{name: "rejected", raw: defaultURL, probeErr: errors.New("credentials rejected"), wantProbes: 1, wantErr: true},
		{name: "failed install", raw: defaultURL, installErr: errors.New("package download failed"), wantProbes: 1, wantRuns: 1, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probes, runs := 0, 0
			check := func(context.Context, string) (bool, error) { probes++; return tc.ready || runs > 0, tc.probeErr }
			run := func(_ context.Context, script string, _ io.Writer) error {
				runs++
				if script != darwinScript {
					t.Fatal("wrong installer")
				}
				return tc.installErr
			}
			err := ensure(context.Background(), tc.raw, "darwin", nil, check, run)
			if (err != nil) != tc.wantErr || probes != tc.wantProbes || runs != tc.wantRuns {
				t.Fatalf("err=%v probes=%d runs=%d", err, probes, runs)
			}
		})
	}
}

func TestEnsureChecksReadinessAfterInstallAndAllowsRetry(t *testing.T) {
	attempts := 0
	run := func(context.Context, string, io.Writer) error { attempts++; return nil }
	check := func(context.Context, string) (bool, error) { return attempts >= 2, nil }
	if err := ensure(context.Background(), defaultURL, "darwin", nil, check, run); err == nil {
		t.Fatal("unreachable database reported ready")
	}
	if err := ensure(context.Background(), defaultURL, "darwin", nil, check, run); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("installer attempts=%d", attempts)
	}
}

func TestInstallerCancellationAndFailureOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runScript(ctx, "exit 0", io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
	var output bytes.Buffer
	err := runScript(context.Background(), "echo 'administrator access required' >&2; exit 7", &output)
	if err == nil || !strings.Contains(err.Error(), "administrator access required") || !strings.Contains(output.String(), "administrator access required") {
		t.Fatalf("error=%v output=%s", err, output.String())
	}
}

func TestMacInstallerLifecycleWithoutChangingHost(t *testing.T) {
	for _, tc := range []struct {
		name                                    string
		installed, ready, database, failInstall bool
		wantInstall, wantStart, wantCreate      bool
	}{
		{name: "fresh", wantInstall: true, wantStart: true, wantCreate: true},
		{name: "stopped", installed: true, wantStart: true, wantCreate: true},
		{name: "ready", installed: true, ready: true, database: true},
		{name: "missing database", installed: true, ready: true, wantCreate: true},
		{name: "failed download", failInstall: true, wantInstall: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, exists := range map[string]bool{"installed": tc.installed, "ready": tc.ready, "database": tc.database, "fail-install": tc.failInstall} {
				if exists {
					if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			harness := `
set -eu
brew() {
 printf 'brew %s\n' "$*" >> "$FIXTURE/commands"
 case "$1" in
  list) [ "$3" = postgresql@16 ] && [ -f "$FIXTURE/installed" ];;
  install) [ ! -f "$FIXTURE/fail-install" ] || { echo 'download failed' >&2; return 1; }; touch "$FIXTURE/installed";;
  services) touch "$FIXTURE/ready";;
  --prefix) printf '%s\n' "$FIXTURE";;
  *) return 99;;
 esac
}
pg_isready() { [ -f "$FIXTURE/ready" ]; }
psql() {
 case "$*" in
  *server_version_num*) echo 160004;;
  *pg_database*) [ ! -f "$FIXTURE/database" ] || echo 1;;
  *) return 99;;
 esac
}
createdb() { echo createdb >> "$FIXTURE/commands"; touch "$FIXTURE/database"; }
sleep() { :; }
`
			cmd := exec.Command("/bin/bash", "-c", harness+darwinScript)
			cmd.Env = append(os.Environ(), "FIXTURE="+dir)
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.failInstall {
				t.Fatalf("error=%v output=%s", err, out)
			}
			log, _ := os.ReadFile(filepath.Join(dir, "commands"))
			commands := string(log)
			for marker, want := range map[string]bool{"brew install": tc.wantInstall, "brew services": tc.wantStart, "createdb": tc.wantCreate} {
				if strings.Contains(commands, marker) != want {
					t.Fatalf("%s want=%v commands=%s", marker, want, commands)
				}
			}
		})
	}
}

func TestLinuxFreshInstallAndPermissionFailureWithoutChangingHost(t *testing.T) {
	for _, privileged := range []bool{true, false} {
		name := "allowed"
		if !privileged {
			name = "denied"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			harness := `
set -eu
id() { if [ "$1" = -un ]; then echo agentuser; else echo 1000; fi; }
sudo() {
 echo "sudo $*" >> "$FIXTURE/commands"
 [ "$ALLOW_ROOT" = 1 ] || return 1
 [ "$1" != -n ] || shift
 if [ "${1:-}" = -u ]; then shift 2; fi
 "$@"
}
apt-get() { echo "apt-get $*" >> "$FIXTURE/commands"; }
apt-cache() { return 0; }
pg_isready() { [ -f "$FIXTURE/ready" ]; }
systemctl() { echo systemctl >> "$FIXTURE/commands"; touch "$FIXTURE/ready"; }
psql() {
 case "$*" in
  *server_version_num*) echo 160004;;
  *pg_roles*) [ ! -f "$FIXTURE/role" ] || echo 1;;
  *pg_database*) [ ! -f "$FIXTURE/database" ] || echo 1;;
  *'SELECT 1'*) [ -f "$FIXTURE/database" ];;
  *) return 99;;
 esac
}
createuser() { echo createuser >> "$FIXTURE/commands"; touch "$FIXTURE/role"; }
createdb() { echo createdb >> "$FIXTURE/commands"; touch "$FIXTURE/database"; }
env() { while [[ "${1:-}" = *=* ]]; do shift; done; "$@"; }
sleep() { :; }
`
			// Redirect filesystem discovery to an empty fixture. All package/service/SQL
			// commands are shell functions above; no host state or privileges are used.
			script := strings.NewReplacer("/usr/lib/postgresql/", dir+"/postgresql/", "/usr/pgsql-", dir+"/pgsql-", "/usr/bin/postgres", dir+"/postgres").Replace(linuxScript)
			// Keep cluster-discovery utilities absent, including on Linux CI hosts.
			harness += `command() { case "${2:-}" in postgresql-setup|pg_lsclusters|pg_ctlcluster) return 1;; *) builtin command "$@";; esac; }
`
			cmd := exec.Command("/bin/bash", "-c", harness+script)
			allowed := "0"
			if privileged {
				allowed = "1"
			}
			cmd.Env = append(os.Environ(), "FIXTURE="+dir, "ALLOW_ROOT="+allowed)
			out, err := cmd.CombinedOutput()
			if (err == nil) != privileged {
				t.Fatalf("error=%v output=%s", err, out)
			}
			log, _ := os.ReadFile(filepath.Join(dir, "commands"))
			commands := string(log)
			if privileged {
				for _, want := range []string{"apt-get install -y postgresql-16 postgresql-client-16", "systemctl", "createuser", "createdb"} {
					if !strings.Contains(commands, want) {
						t.Fatalf("missing %s in %s", want, commands)
					}
				}
			} else if strings.Contains(commands, "apt-get install") || !strings.Contains(string(out), "needs root or passwordless sudo") {
				t.Fatalf("commands=%s output=%s", commands, out)
			}
		})
	}
}

func TestMacMissingHomebrewReportsBootstrapFailure(t *testing.T) {
	dir := t.TempDir()
	// Redirect the two system Homebrew paths; no live installation is accessed.
	script := strings.NewReplacer("/opt/homebrew/bin/brew", dir+"/arm/bin/brew", "/usr/local/bin/brew", dir+"/intel/bin/brew").Replace(darwinScript)
	harness := `
command() { if [ "${2:-}" = brew ]; then return 1; fi; builtin command "$@"; }
curl() {
 echo "$*" > "$FIXTURE/download"
 for arg in "$@"; do destination="$arg"; done
 printf 'echo "Need sudo access" >&2\nexit 1\n' > "$destination"
}
`
	cmd := exec.Command("/bin/bash", "-c", harness+script)
	cmd.Env = append(os.Environ(), "FIXTURE="+dir)
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "Homebrew installation failed") || !strings.Contains(string(out), "amux database setup") {
		t.Fatalf("error=%v output=%s", err, out)
	}
	download, _ := os.ReadFile(filepath.Join(dir, "download"))
	if !strings.Contains(string(download), "https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh") {
		t.Fatalf("unexpected installer source: %s", download)
	}
}

func TestEnsureUnsupportedPlatformDoesNotRunInstaller(t *testing.T) {
	check := func(context.Context, string) (bool, error) { return false, nil }
	run := func(context.Context, string, io.Writer) error {
		t.Fatal("installer ran on unsupported platform")
		return nil
	}
	err := ensure(context.Background(), defaultURL, "windows", nil, check, run)
	if err == nil || !strings.Contains(err.Error(), "not supported on windows") {
		t.Fatalf("error=%v", err)
	}
}

func TestReleaseInstallerPreparesDatabaseAndPropagatesFailure(t *testing.T) {
	installer, err := filepath.Abs("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		skip, fail bool
	}{
		{name: "setup"}, {name: "download only", skip: true}, {name: "database error", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			payload := filepath.Join(dir, "payload")
			mockBin := filepath.Join(dir, "bin")
			for _, path := range []string{payload, mockBin} {
				if err := os.MkdirAll(path, 0755); err != nil {
					t.Fatal(err)
				}
			}
			fakeCLI := `#!/bin/sh
printf '%s\n' "$*" >> "$TEST_COMMANDS"
if [ "$1" = version ]; then echo 'amux test'; exit 0; fi
[ "$*" = 'database setup' ] || exit 99
[ "$TEST_SETUP_FAIL" != 1 ] || { echo 'fixture database setup failed' >&2; exit 1; }
`
			for _, binary := range []string{"amux", "agentmux-hook"} {
				if err := os.WriteFile(filepath.Join(payload, binary), []byte(fakeCLI), 0755); err != nil {
					t.Fatal(err)
				}
			}
			archiveName := "agentmux_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
			archive := filepath.Join(dir, archiveName)
			if out, err := exec.Command("tar", "-czf", archive, "-C", payload, "amux", "agentmux-hook").CombinedOutput(); err != nil {
				t.Fatalf("tar: %v %s", err, out)
			}
			data, err := os.ReadFile(archive)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(data)
			if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(fmt.Sprintf("%x  %s\n", digest, archiveName)), 0600); err != nil {
				t.Fatal(err)
			}
			fakeCurl := `#!/bin/sh
while [ "$#" -gt 0 ]; do
 case "$1" in -o) destination="$2"; shift 2;; *) url="$1"; shift;; esac
done
cp "$TEST_RELEASE/${url##*/}" "$destination"
`
			if err := os.WriteFile(filepath.Join(mockBin, "curl"), []byte(fakeCurl), 0755); err != nil {
				t.Fatal(err)
			}
			skip, fail := "0", "0"
			if tc.skip {
				skip = "1"
			}
			if tc.fail {
				fail = "1"
			}
			logPath := filepath.Join(dir, "commands")
			cmd := exec.Command("/bin/sh", installer)
			cmd.Env = append(os.Environ(), "PATH="+mockBin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"TEST_RELEASE="+dir, "TEST_COMMANDS="+logPath, "TEST_SETUP_FAIL="+fail,
				"AMUX_SKIP_DATABASE_SETUP="+skip, "AMUX_INSTALL_DIR="+filepath.Join(dir, "installed with spaces"), "AMUX_DOWNLOAD_ROOT=https://example.invalid/releases")
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.fail {
				t.Fatalf("error=%v output=%s", err, out)
			}
			commands, _ := os.ReadFile(logPath)
			if strings.Contains(string(commands), "database setup") == tc.skip {
				t.Fatalf("commands=%s", commands)
			}
			if tc.fail && !strings.Contains(string(out), "binaries are installed, but database setup failed") {
				t.Fatalf("output=%s", out)
			}
		})
	}
}
