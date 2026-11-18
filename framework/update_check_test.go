package framework

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDetectCLIInstallation(t *testing.T) {
	for _, test := range []struct {
		name     string
		resolved string
		pkg      string
		goos     string
		want     cliInstallation
	}{
		{"Homebrew cask", "/opt/homebrew/Caskroom/codex/0.167.0/bin/codex", "@openai/codex", "darwin",
			cliInstallation{Method: cliInstallHomebrewCask, Prefix: "/opt/homebrew", Token: "codex"}},
		{"Homebrew cask with arch binary", "/usr/local/Caskroom/codex/0.144.1/codex-x86_64-apple-darwin", "@openai/codex", "darwin",
			cliInstallation{Method: cliInstallHomebrewCask, Prefix: "/usr/local", Token: "codex"}},
		{"Claude Code cask", "/opt/homebrew/Caskroom/claude-code/2.1.211/claude", "@anthropic-ai/claude-code", "darwin",
			cliInstallation{Method: cliInstallHomebrewCask, Prefix: "/opt/homebrew", Token: "claude-code"}},
		{"Homebrew formula", "/opt/homebrew/Cellar/codex/0.167.0/bin/codex", "@openai/codex", "darwin",
			cliInstallation{Method: cliInstallHomebrewFormula, Prefix: "/opt/homebrew", Token: "codex"}},
		{"Linuxbrew formula", "/home/linuxbrew/.linuxbrew/Cellar/opencode/1.0.0/bin/opencode", "opencode-ai", "linux",
			cliInstallation{Method: cliInstallHomebrewFormula, Prefix: "/home/linuxbrew/.linuxbrew", Token: "opencode"}},
		{"npm global", "/usr/local/lib/node_modules/@openai/codex/bin/codex.js", "@openai/codex", "darwin",
			cliInstallation{Method: cliInstallNPMGlobal, Prefix: "/usr/local"}},
		{"nvm global", "/home/u/.nvm/versions/node/v22.1.0/lib/node_modules/@openai/codex/bin/codex.js", "@openai/codex", "linux",
			cliInstallation{Method: cliInstallNPMGlobal, Prefix: "/home/u/.nvm/versions/node/v22.1.0"}},
		{"vendored native binary inside npm package", "/opt/homebrew/lib/node_modules/@openai/codex/node_modules/@openai/codex-darwin-arm64/vendor/codex", "@openai/codex", "darwin",
			cliInstallation{Method: cliInstallNPMGlobal, Prefix: "/opt/homebrew"}},
		{"npm under a Homebrew node keg is not the node formula", "/opt/homebrew/Cellar/node/22.1.0/lib/node_modules/@openai/codex/bin/codex.js", "@openai/codex", "darwin",
			cliInstallation{Method: cliInstallNPMGlobal, Prefix: "/opt/homebrew/Cellar/node/22.1.0"}},
		{"pnpm store", "/Users/u/Library/pnpm/global/5/.pnpm/@openai+codex@0.1.0/node_modules/@openai/codex/bin/codex.js", "@openai/codex", "darwin", cliInstallation{}},
		{"bun global", "/Users/u/.bun/install/global/node_modules/@openai/codex/bin/codex.js", "@openai/codex", "darwin", cliInstallation{}},
		{"volta image", "/Users/u/.volta/tools/image/packages/@openai/codex/lib/node_modules/@openai/codex/bin/codex.js", "@openai/codex", "darwin", cliInstallation{}},
		{"project-local dependency", "/srv/app/node_modules/@openai/codex/bin/codex.js", "@openai/codex", "linux", cliInstallation{}},
		{"other package", "/usr/local/lib/node_modules/@openai/codex-sdk/bin/x.js", "@openai/codex", "darwin", cliInstallation{}},
		{"standalone or wrapper", "/Users/u/.local/bin/codex", "@openai/codex", "darwin", cliInstallation{}},
		{"standalone package root", "/Users/u/.codex/packages/standalone/0.167.0/bin/codex", "@openai/codex", "darwin", cliInstallation{}},
		{"npm without package", "/usr/local/lib/node_modules/@openai/codex/bin/codex.js", "", "darwin", cliInstallation{}},
		{"Windows npm global", `C:/Users/u/AppData/Roaming/npm/node_modules/@openai/codex/bin/codex.js`, "@openai/codex", "windows",
			cliInstallation{Method: cliInstallNPMGlobal, Prefix: filepath.FromSlash("C:/Users/u/AppData/Roaming/npm")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if runtime.GOOS == "windows" && test.goos != "windows" {
				t.Skip("POSIX path syntax")
			}
			got := detectCLIInstallation(test.resolved, test.pkg, test.goos)
			if got != test.want {
				t.Fatalf("detectCLIInstallation(%q) = %+v, want %+v", test.resolved, got, test.want)
			}
		})
	}
}

func TestCLIUpdateCommandFor(t *testing.T) {
	codex, _ := Lookup("codex")
	only := func(paths ...string) func(string) (string, error) {
		return func(name string) (string, error) {
			for _, path := range paths {
				if name == path || filepath.Base(path) == name && !strings.Contains(name, "/") {
					return path, nil
				}
			}
			return "", errors.New("not found")
		}
	}
	cask := cliInstallation{Method: cliInstallHomebrewCask, Prefix: "/opt/homebrew", Token: "codex"}
	formula := cliInstallation{Method: cliInstallHomebrewFormula, Prefix: "/opt/homebrew", Token: "codex"}
	for _, test := range []struct {
		name    string
		inst    cliInstallation
		lookup  func(string) (string, error)
		want    []string
		wantOK  bool
		wantErr bool
	}{
		{"cask uses its prefix's brew", cask, only("/opt/homebrew/bin/brew", "/usr/local/bin/brew"),
			[]string{"/opt/homebrew/bin/brew", "upgrade", "--cask", "codex"}, true, false},
		{"cask falls back to brew on PATH", cask, only("/usr/local/bin/brew"),
			[]string{"/usr/local/bin/brew", "upgrade", "--cask", "codex"}, true, false},
		{"formula", formula, only("/opt/homebrew/bin/brew"),
			[]string{"/opt/homebrew/bin/brew", "upgrade", "codex"}, true, false},
		{"missing brew", cask, only(), nil, false, true},
		{"npm global pins the prefix", cliInstallation{Method: cliInstallNPMGlobal, Prefix: "/home/u/.nvm/versions/node/v22"}, only(),
			[]string{"npm", "install", "-g", "@openai/codex@latest", "--prefix=/home/u/.nvm/versions/node/v22"}, true, false},
		{"unknown keeps native updater", cliInstallation{}, only(), nil, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok, err := cliUpdateCommandFor(codex, test.inst, test.lookup)
			if (err != nil) != test.wantErr || ok != test.wantOK || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("cliUpdateCommandFor = %q, %v, %v; want %q, %v, err %v", got, ok, err, test.want, test.wantOK, test.wantErr)
			}
		})
	}
}

func TestNativeUpdaterCannotDetectInstall(t *testing.T) {
	codex, _ := Lookup("codex")
	log := "Error: Could not detect the Codex installation method. Please update manually: https://developers.openai.com/codex/cli/\n"
	if !nativeUpdaterCannotDetectInstall(log) {
		t.Fatal("Codex detection failure not recognised")
	}
	if nativeUpdaterCannotDetectInstall("error: network unreachable") {
		t.Fatal("unrelated failure treated as detection failure")
	}
	native := CommandPlan{Argv: []string{"codex", "update"}, Command: "codex update"}
	plan, ok := npmFallbackUpdatePlan(codex, native, log, InstallSettings{DefaultRegistry: "https://bnpm.byted.org/"})
	if !ok || plan.Command != "npm install -g @openai/codex@latest --registry=https://bnpm.byted.org/" {
		t.Fatalf("fallback plan = %+v, %v", plan, ok)
	}
	if _, ok := npmFallbackUpdatePlan(codex, CommandPlan{Argv: []string{"/bin/sh", "-c", "x"}, Command: "x", Custom: true}, log, InstallSettings{}); ok {
		t.Fatal("custom commands must not be replaced")
	}
	if _, ok := npmFallbackUpdatePlan(codex, native, "boom", InstallSettings{}); ok {
		t.Fatal("unrelated failures must not trigger the npm fallback")
	}
}

func TestCodexPrereleaseUpdateComparison(t *testing.T) {
	if got := normalizeSDKVersion("codex-cli 0.169.0-alpha.3"); got != "0.169.0-alpha.3" {
		t.Fatalf("normalized prerelease = %q", got)
	}
	for _, test := range []struct {
		latest, current string
		want            bool
	}{
		{"0.170.0", "0.169.0-alpha.3", true},
		{"0.170.0", "0.170.0-alpha.1", true},
		{"0.169.0", "0.170.0-alpha.1", false},
		{"0.170.0", "0.170.0", false},
		{"0.170.0-alpha.2", "0.170.0-alpha.10", false},
	} {
		if got := frameworkUpdateAvailable(Spec{}, test.latest, test.current); got != test.want {
			t.Errorf("update available latest=%s current=%s = %v, want %v", test.latest, test.current, got, test.want)
		}
	}
	check := UpdateCheck{CurrentVersion: "0.169.0-alpha.3", LatestVersion: "0.170.0"}
	if !updateAdvanced(Spec{}, check, "0.170.0") || updateAdvanced(Spec{}, check, "0.169.0-alpha.3") {
		t.Fatal("prerelease to stable verification is wrong")
	}
	if !updateAdvanced(Spec{}, UpdateCheck{CurrentVersion: "0.170.0"}, "0.170.0") {
		t.Fatal("unknown latest must trust a successful package-manager run")
	}
}

// eofThenVersionServer drops the first `drops` connections without a response,
// which the client sees as EOF, then serves the version.
func eofThenVersionServer(t *testing.T, drops int32, version string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) <= drops {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		fmt.Fprintf(w, `{"version":%q}`, version)
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func fastLatestVersionRetries(t *testing.T) {
	t.Helper()
	previous := latestVersionRetryDelays
	latestVersionRetryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { latestVersionRetryDelays = previous })
}

func TestOfficialCLIVersionRetriesDroppedConnections(t *testing.T) {
	fastLatestVersionRetries(t)
	server, requests := eofThenVersionServer(t, 2, "0.170.0")
	version, err := officialCLIVersion(context.Background(), server.URL)
	if err != nil || version != "0.170.0" || requests.Load() != 3 {
		t.Fatalf("version = %q, err = %v, requests = %d", version, err, requests.Load())
	}

	always, requests := eofThenVersionServer(t, 100, "0.170.0")
	if _, err := officialCLIVersion(context.Background(), always.URL); err == nil || requests.Load() != 3 {
		t.Fatalf("persistent EOF: err = %v, requests = %d", err, requests.Load())
	}

	var notFound atomic.Int32
	missing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		notFound.Add(1)
		http.NotFound(w, r)
	}))
	defer missing.Close()
	if _, err := officialCLIVersion(context.Background(), missing.URL); err == nil || notFound.Load() != 1 {
		t.Fatalf("404 must fail without retrying: err = %v, requests = %d", err, notFound.Load())
	}
}

func TestLatestVersionFallsBackToNPMWhenEndpointFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	fastLatestVersionRetries(t)
	server, _ := eofThenVersionServer(t, 100, "unused")
	bin := t.TempDir()
	writeFrameworkExecutable(t, filepath.Join(bin, "npm"), "#!/bin/sh\n"+
		"case \" $* \" in *' view @openai/codex version '*) echo 0.170.0 ;; *) exit 3 ;; esac\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin:/bin")
	spec, _ := Lookup("codex")
	spec.LatestURL = server.URL
	version, err := latestFrameworkVersion(context.Background(), spec, Prereqs{NPM: true}, InstallSettings{})
	if err != nil || version != "0.170.0" {
		t.Fatalf("npm fallback = %q, %v", version, err)
	}
	if _, err := latestFrameworkVersion(context.Background(), spec, Prereqs{}, InstallSettings{}); err == nil {
		t.Fatal("without npm the endpoint error must surface")
	}
}

func TestLatestVersionCache(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	var requests atomic.Int32
	var fail atomic.Bool
	fail.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if fail.Load() {
			http.Error(w, "nope", http.StatusNotFound)
			return
		}
		fmt.Fprint(w, `{"version":"0.170.0"}`)
	}))
	defer server.Close()
	for i := range catalog {
		if catalog[i].Kind == "codex" {
			previous := catalog[i].LatestURL
			catalog[i].LatestURL = server.URL
			t.Cleanup(func() { catalog[i].LatestURL = previous })
		}
	}
	now := time.Now()
	previousNow := latestVersionNow
	latestVersionNow = func() time.Time { return now }
	t.Cleanup(func() { latestVersionNow = previousNow })

	bin := t.TempDir()
	writeFrameworkExecutable(t, filepath.Join(bin, "codex"), "#!/bin/sh\necho 'codex-cli 0.169.0-alpha.3'\n")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("NVM_DIR", "")
	t.Setenv("PNPM_HOME", "")
	t.Setenv("PATH", bin)

	if check := CheckUpdate(context.Background(), "codex"); check.Error == "" {
		t.Fatalf("failing endpoint check = %+v", check)
	}
	fail.Store(false)
	check := CheckUpdate(context.Background(), "codex")
	if check.Error != "" || !check.UpdateAvailable || check.LatestVersion != "0.170.0" || requests.Load() != 2 {
		t.Fatalf("errors must not be cached: check = %+v, requests = %d", check, requests.Load())
	}
	if check = CheckUpdate(context.Background(), "codex"); check.LatestVersion != "0.170.0" || requests.Load() != 2 {
		t.Fatalf("second check should be cached: %+v, requests = %d", check, requests.Load())
	}
	if _, err := checkUpdate(context.Background(), "codex", InstallSettings{}, true); err != nil || requests.Load() != 3 {
		t.Fatalf("fresh check must bypass the cache: err = %v, requests = %d", err, requests.Load())
	}
	// A different registry setting is a different answer.
	settings := InstallSettings{DefaultRegistry: "https://bnpm.byted.org/"}
	if CheckUpdateWithSettings(context.Background(), "codex", settings); requests.Load() != 4 {
		t.Fatalf("registry change must miss the cache: requests = %d", requests.Load())
	}
	now = now.Add(latestVersionCacheTTL + time.Second)
	if CheckUpdate(context.Background(), "codex"); requests.Load() != 5 {
		t.Fatalf("expired entry must be refreshed: requests = %d", requests.Load())
	}
	forgetLatestVersions("codex")
	if CheckUpdate(context.Background(), "codex"); requests.Load() != 6 {
		t.Fatalf("forgotten entry must be refreshed: requests = %d", requests.Load())
	}
}

// installNPMGlobalCodex lays out <prefix>/bin/codex -> ../lib/node_modules/
// @openai/codex/bin/codex.js like `npm install -g`, reporting the version in
// versionFile.
func installNPMGlobalCodex(t *testing.T, prefix, versionFile string) {
	t.Helper()
	pkgBin := filepath.Join(prefix, "lib", "node_modules", "@openai", "codex", "bin")
	if err := os.MkdirAll(pkgBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFrameworkExecutable(t, filepath.Join(pkgBin, "codex.js"), "#!/bin/sh\n"+
		"if [ \"$1\" = update ]; then echo 'Error: Could not detect the Codex installation method.' >&2; exit 1; fi\n"+
		"read version < '"+versionFile+"'\necho \"codex-cli $version\"\n")
	if err := os.Symlink(filepath.Join("..", "lib", "node_modules", "@openai", "codex", "bin", "codex.js"), filepath.Join(prefix, "bin", "codex")); err != nil {
		t.Fatal(err)
	}
}

// A dropped registry connection during the pre-check must not stop an
// explicit update, and an npm-installed Codex is updated with npm into its own
// prefix rather than through Codex's own installation-method detection.
func TestUpdateNPMCodexRunsDespiteRegistryEOF(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script and npm layout test")
	}
	fastLatestVersionRetries(t)
	server, _ := eofThenVersionServer(t, 100, "unused")
	mockFrameworkLatestVersion(t, "codex", "unused")
	for i := range catalog {
		if catalog[i].Kind == "codex" {
			catalog[i].LatestURL = server.URL
		}
	}
	root := t.TempDir()
	prefix := filepath.Join(root, "node")
	versionFile := filepath.Join(root, "version")
	if err := os.WriteFile(versionFile, []byte("0.169.0-alpha.3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	installNPMGlobalCodex(t, prefix, versionFile)
	resolvedPrefix, err := filepath.EvalSymlinks(prefix)
	if err != nil {
		t.Fatal(err)
	}
	tools := filepath.Join(root, "tools")
	if err := os.MkdirAll(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFrameworkExecutable(t, filepath.Join(tools, "npm"), "#!/bin/sh\n"+
		"case \" $* \" in\n"+
		"  *' view '*) echo 'npm ERR! network' >&2; exit 1 ;;\n"+
		"  *' install -g @openai/codex@latest --prefix="+resolvedPrefix+" --registry=https://bnpm.byted.org/ '*) echo 0.170.0 > '"+versionFile+"'; echo 'added 1 package' ;;\n"+
		"  *) echo \"unexpected npm $*\" >&2; exit 2 ;;\n"+
		"esac\n")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("NVM_DIR", "")
	t.Setenv("PNPM_HOME", "")
	t.Setenv("PATH", filepath.Join(prefix, "bin")+string(os.PathListSeparator)+tools+string(os.PathListSeparator)+"/usr/bin:/bin")

	settings := InstallSettings{DefaultRegistry: "https://bnpm.byted.org/"}
	if check := CheckUpdateWithSettings(context.Background(), "codex", settings); !strings.Contains(check.Error, "check update failed") {
		t.Fatalf("check with unreachable registry = %+v", check)
	}
	res := UpdateWithProgressOptions(context.Background(), "codex", InstallOptions{Settings: settings}, nil)
	want := "npm install -g @openai/codex@latest --prefix=" + resolvedPrefix + " --registry=https://bnpm.byted.org/"
	if !res.OK || res.Version != "0.170.0" || res.Command != want || !strings.Contains(res.Log, "ran the update anyway") {
		t.Fatalf("update = %+v, want command %q", res, want)
	}
}

// When Codex's own updater cannot tell how it was installed (a standalone
// copy or a wrapper AgentMux cannot see through), the update falls back to npm.
func TestUpdateFallsBackToNPMWhenNativeUpdaterCannotDetectInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	mockFrameworkLatestVersion(t, "codex", "0.170.0")
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	versionFile := filepath.Join(home, "version")
	if err := os.WriteFile(versionFile, []byte("0.169.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFrameworkExecutable(t, filepath.Join(bin, "codex"), "#!/bin/sh\n"+
		"if [ \"$1\" = update ]; then echo 'Error: Could not detect the Codex installation method. Please update manually: https://developers.openai.com/codex/cli/' >&2; exit 1; fi\n"+
		"read version < '"+versionFile+"'\necho \"codex-cli $version\"\n")
	writeFrameworkExecutable(t, filepath.Join(bin, "npm"), "#!/bin/sh\n"+
		"if [ \"$*\" = 'install -g @openai/codex@latest' ]; then echo 0.170.0 > '"+versionFile+"'; exit 0; fi\n"+
		"echo \"unexpected npm $*\" >&2; exit 2\n")
	t.Setenv("HOME", home)
	t.Setenv("NVM_DIR", "")
	t.Setenv("PNPM_HOME", "")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin:/bin")

	res := UpdateWithProgress(context.Background(), "codex", nil)
	if !res.OK || res.Version != "0.170.0" || res.Command != "npm install -g @openai/codex@latest" ||
		!strings.Contains(res.Log, "Could not detect the Codex installation method") {
		t.Fatalf("update = %+v", res)
	}
}
