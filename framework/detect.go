package framework

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
	var p Prereqs
	if path, err := exec.LookPath("node"); err == nil {
		p.Node = true
		p.NodePth = path
	}
	if path, err := exec.LookPath("npm"); err == nil {
		p.NPM = true
		p.NPMPath = path
	}
	return p
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
		out, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
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

// resolveCLIExecutable also recognizes the standard user-local bin directory
// used by native installers such as Cursor. Adding that directory to the
// current process PATH makes a freshly installed CLI immediately routable
// without requiring the daemon to restart.
func resolveCLIExecutable(bin string) (string, error) {
	if path, err := exec.LookPath(bin); err == nil {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", exec.ErrNotFound
	}
	candidate := filepath.Join(home, ".local", "bin", bin)
	info, err := os.Stat(candidate)
	if err != nil || info.IsDir() {
		return "", exec.ErrNotFound
	}

	processPathMu.Lock()
	defer processPathMu.Unlock()
	dir := filepath.Dir(candidate)
	pathValue := os.Getenv("PATH")
	for _, existing := range filepath.SplitList(pathValue) {
		if filepath.Clean(existing) == filepath.Clean(dir) {
			return candidate, nil
		}
	}
	if pathValue == "" {
		_ = os.Setenv("PATH", dir)
	} else {
		_ = os.Setenv("PATH", dir+string(os.PathListSeparator)+pathValue)
	}
	return candidate, nil
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
