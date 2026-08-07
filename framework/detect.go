package framework

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Status is the runtime state of a framework: whether it is installed and, for
// SDK frameworks, the installed package version.
type Status struct {
	Spec      Spec   `json:"spec"`
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	// Detail carries a human-readable hint when detection is inconclusive
	// (e.g. missing node/npm for SDK frameworks).
	Detail string `json:"detail,omitempty"`
}

// Prereqs reports whether the host tools needed to install/run SDK frameworks
// are present.
type Prereqs struct {
	Node    bool   `json:"node"`
	NodePth string `json:"node_path,omitempty"`
	NPM     bool   `json:"npm"`
	NPMPath string `json:"npm_path,omitempty"`
}

var processPathMu sync.Mutex

// DetectPrereqs probes for node and npm on PATH.
func DetectPrereqs() Prereqs {
	// SDK registration invokes prerequisite detection during daemon startup.
	// Refresh all existing user executable directories here so CLI adapters are
	// routable even before the frameworks page performs per-binary detection.
	refreshUserExecutablePath()
	var p Prereqs
	if path, err := resolveCLIExecutable("node"); err == nil {
		p.Node = true
		p.NodePth = path
	}
	if path, err := resolveCLIExecutable("npm"); err == nil {
		p.NPM = true
		p.NPMPath = path
	}
	return p
}

func refreshUserExecutablePath() {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return
	}
	processPathMu.Lock()
	defer processPathMu.Unlock()
	ensurePNPMHome(home)
	for _, dir := range userExecutableDirs(home) {
		if info, statErr := os.Stat(dir); statErr == nil && info.IsDir() {
			appendProcessPath(dir)
		}
	}
}

// Detect returns the status of a single framework spec.
func Detect(s Spec, pre Prereqs) Status {
	st := Status{Spec: s}
	switch s.KindType {
	case KindCLI:
		if s.Bin == "" {
			return st
		}
		binary, err := resolveCLIExecutable(s.Bin)
		if err != nil {
			return st
		}
		st.Installed = true
		args := s.VersionArgs
		if len(args) == 0 {
			args = []string{"--version"}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := frameworkCommandContext(ctx, binary, args...).CombinedOutput()
		if err != nil {
			st.Detail = strings.TrimSpace(string(out))
			if st.Detail == "" {
				st.Detail = err.Error()
			}
			return st
		}
		st.Version = normalizeSDKVersion(string(out))
		if st.Version == "" {
			st.Detail = "version command returned no recognizable version"
		}
	case KindSDK:
		if s.Language == "node" {
			if !pre.Node || !pre.NPM {
				st.Detail = "requires node and npm on PATH"
			}
			st.Installed, st.Version = nodePackageInstalled(s.Packages)
		} else {
			st.Detail = "requires a Python runtime (not yet supported)"
		}
	}
	return st
}

// IsInstalled reports whether a catalogued framework can be launched on the
// current host. Unlike Detect, it deliberately avoids running version commands
// so callers can use it on latency-sensitive paths such as Agent creation.
func IsInstalled(kind string) bool {
	s, ok := Lookup(kind)
	if !ok || !s.Supported {
		return false
	}
	switch s.KindType {
	case KindCLI:
		if s.Bin == "" {
			return false
		}
		_, err := resolveCLIExecutable(s.Bin)
		return err == nil
	case KindSDK:
		if s.Language != "node" {
			return false
		}
		if _, err := resolveCLIExecutable("node"); err != nil {
			return false
		}
		installed, _ := nodePackageInstalled(s.Packages)
		return installed
	default:
		return false
	}
}

// resolveCLIExecutable also recognizes common user-level executable
// directories. Background services do not load interactive shell startup
// files, so their PATH commonly omits native-installer and version-manager
// locations even though the same commands work in an SSH terminal. Adding the
// matching directory to the current process PATH also makes the CLI immediately
// routable without requiring the daemon to restart.
func resolveCLIExecutable(bin string) (string, error) {
	processPathMu.Lock()
	defer processPathMu.Unlock()

	if path, err := exec.LookPath(bin); err == nil {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", exec.ErrNotFound
	}
	ensurePNPMHome(home)
	for _, dir := range userExecutableDirs(home) {
		candidate, lookErr := exec.LookPath(filepath.Join(dir, bin))
		if lookErr != nil {
			continue
		}
		appendProcessPath(dir)
		return candidate, nil
	}
	return "", exec.ErrNotFound
}

func appendProcessPath(dir string) {
	pathValue := os.Getenv("PATH")
	for _, existing := range filepath.SplitList(pathValue) {
		if filepath.Clean(existing) == filepath.Clean(dir) {
			return
		}
	}
	if pathValue == "" {
		_ = os.Setenv("PATH", dir)
	} else {
		_ = os.Setenv("PATH", pathValue+string(os.PathListSeparator)+dir)
	}
}

// ensurePNPMHome repairs the environment inherited by background services.
// pnpm's shell setup normally exports PNPM_HOME, but service managers do not
// source the user's interactive shell profile. Merely finding a pnpm-installed
// CLI on PATH is not enough: a native updater such as `codex update` invokes
// `pnpm add -g`, which refuses to run without a global bin directory.
func ensurePNPMHome(home string) {
	if strings.TrimSpace(os.Getenv("PNPM_HOME")) != "" {
		return
	}
	candidates := make([]string, 0, 2)
	if dataHome := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); dataHome != "" {
		candidates = append(candidates, filepath.Join(dataHome, "pnpm"))
	}
	candidates = append(candidates, filepath.Join(home, ".local", "share", "pnpm"))
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			_ = os.Setenv("PNPM_HOME", candidate)
			return
		}
	}
}

func userExecutableDirs(home string) []string {
	dirs := []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".npm-global", "bin"),
		filepath.Join(home, ".npm", "bin"),
		filepath.Join(home, ".volta", "bin"),
		filepath.Join(home, ".bun", "bin"),
		filepath.Join(home, ".asdf", "shims"),
		filepath.Join(home, ".nodenv", "shims"),
		filepath.Join(home, ".local", "share", "mise", "shims"),
		filepath.Join(home, ".local", "share", "fnm", "aliases", "default", "bin"),
		filepath.Join(home, ".fnm", "aliases", "default", "bin"),
	}
	if pnpmHome := strings.TrimSpace(os.Getenv("PNPM_HOME")); pnpmHome != "" {
		dirs = append(dirs, pnpmHome)
	} else {
		dirs = append(dirs, filepath.Join(home, ".local", "share", "pnpm"))
	}
	if nvmDir := strings.TrimSpace(os.Getenv("NVM_DIR")); nvmDir != "" {
		dirs = append(dirs, nvmNodeBinDirs(nvmDir)...)
	}
	dirs = append(dirs, nvmNodeBinDirs(filepath.Join(home, ".nvm"))...)

	seen := make(map[string]bool, len(dirs))
	unique := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		cleaned := filepath.Clean(dir)
		if seen[cleaned] {
			continue
		}
		seen[cleaned] = true
		unique = append(unique, cleaned)
	}
	return unique
}

func nvmNodeBinDirs(nvmDir string) []string {
	dirs, _ := filepath.Glob(filepath.Join(nvmDir, "versions", "node", "*", "bin"))
	// Prefer the newest installed Node runtime when a service has no selected
	// nvm version. nvm release directory names are semantic versions prefixed by
	// "v", so use the existing version comparator with a lexical fallback.
	sort.SliceStable(dirs, func(i, j int) bool {
		left := strings.TrimPrefix(filepath.Base(filepath.Dir(dirs[i])), "v")
		right := strings.TrimPrefix(filepath.Base(filepath.Dir(dirs[j])), "v")
		switch {
		case sdkVersionGreater(left, right):
			return true
		case sdkVersionGreater(right, left):
			return false
		default:
			return dirs[i] > dirs[j]
		}
	})
	return dirs
}

// DetectAll returns the status of every framework in the catalog.
func DetectAll() []Status {
	pre := DetectPrereqs()
	out := make([]Status, 0, len(catalog))
	for _, s := range catalog {
		out = append(out, Detect(s, pre))
	}
	return out
}

// nodePackageInstalled reports whether all packages are present in the
// sidecar's node_modules, returning the primary package's version if readable.
func nodePackageInstalled(packages []string) (bool, string) {
	if len(packages) == 0 {
		return false, ""
	}
	dir := SidecarDir()
	version := ""
	for i, pkg := range packages {
		pkgJSON := filepath.Join(dir, "node_modules", filepath.FromSlash(pkg), "package.json")
		data, err := os.ReadFile(pkgJSON)
		if err != nil {
			return false, ""
		}
		if i == 0 {
			var meta struct {
				Version string `json:"version"`
			}
			if json.Unmarshal(data, &meta) == nil {
				version = meta.Version
			}
		}
	}
	return true, version
}
