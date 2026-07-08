package framework

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
		if _, err := exec.LookPath(s.Bin); err == nil {
			st.Installed = true
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
