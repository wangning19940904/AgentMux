package framework

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// InstallResult reports the outcome of an install attempt.
type InstallResult struct {
	Kind    string `json:"kind"`
	OK      bool   `json:"ok"`
	Command string `json:"command,omitempty"`
	Log     string `json:"log,omitempty"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Install installs an SDK framework by npm-installing its packages into the
// sidecar directory. Only catalogued, supported, node SDK frameworks can be
// installed; the package list comes from the catalog (never from caller
// input), so this cannot be used to install arbitrary packages.
func Install(ctx context.Context, kind string) InstallResult {
	res := InstallResult{Kind: kind}

	spec, ok := Lookup(kind)
	if !ok {
		res.Error = fmt.Sprintf("unknown framework %q", kind)
		return res
	}
	if spec.KindType != KindSDK {
		res.Error = fmt.Sprintf("framework %q is a CLI; install its binary manually", kind)
		return res
	}
	if !spec.Supported {
		res.Error = fmt.Sprintf("framework %q is not yet supported for automatic install", kind)
		return res
	}
	if spec.Language != "node" {
		res.Error = fmt.Sprintf("framework %q requires a %s runtime (not yet supported)", kind, spec.Language)
		return res
	}
	if len(spec.Packages) == 0 {
		res.Error = fmt.Sprintf("framework %q has no packages to install", kind)
		return res
	}

	pre := DetectPrereqs()
	if !pre.NPM {
		res.Error = "npm not found on PATH; install Node.js first"
		return res
	}
	if err := EnsureSidecar(); err != nil {
		res.Error = fmt.Sprintf("prepare sidecar: %v", err)
		return res
	}

	args := append([]string{"install", "--no-audit", "--no-fund"}, spec.Packages...)
	res.Command = "npm " + strings.Join(args, " ")

	runCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
	}

	cmd := exec.CommandContext(runCtx, "npm", args...)
	cmd.Dir = SidecarDir()
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	res.Log = string(out)
	if err != nil {
		res.Error = fmt.Sprintf("npm install failed: %v", err)
		return res
	}

	installed, version := nodePackageInstalled(spec.Packages)
	res.OK = installed
	res.Version = version
	if !installed {
		res.Error = "npm reported success but package not found in node_modules"
	}
	return res
}
