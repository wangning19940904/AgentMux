package framework

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const cliUpdateTimeout = 15 * time.Minute

// InstallResult reports the outcome of an install attempt.
type InstallResult struct {
	Kind    string `json:"kind"`
	Action  string `json:"action"`
	OK      bool   `json:"ok"`
	Command string `json:"command,omitempty"`
	Log     string `json:"log,omitempty"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Install installs a catalogued framework with its catalog-owned command. CLI
// packages are installed globally while SDK packages live in the sidecar. The
// caller selects only the framework kind, so this cannot execute arbitrary
// packages or commands.
func Install(ctx context.Context, kind string) InstallResult {
	return install(ctx, kind, "install")
}

// Update updates an installed framework only when its catalog-owned version
// source reports a newer release. The update is checked again server-side so a
// stale UI cannot trigger an unnecessary command.
func Update(ctx context.Context, kind string) InstallResult {
	check := CheckUpdate(ctx, kind)
	if check.Error != "" {
		return InstallResult{Kind: kind, Action: "update", Error: check.Error}
	}
	if !check.UpdateAvailable {
		return InstallResult{
			Kind:    kind,
			Action:  "update",
			OK:      true,
			Version: check.CurrentVersion,
			Log: fmt.Sprintf(
				"%s is already up to date (current %s, latest %s)",
				check.Display, check.CurrentVersion, check.LatestVersion,
			),
		}
	}
	spec, ok := Lookup(kind)
	if !ok {
		return InstallResult{Kind: kind, Action: "update", Error: fmt.Sprintf("unknown framework %q", kind)}
	}
	if spec.KindType == KindCLI {
		return updateCLI(ctx, spec, check)
	}
	return install(ctx, kind, "update")
}

func updateCLI(ctx context.Context, spec Spec, check UpdateCheck) InstallResult {
	res := InstallResult{Kind: spec.Kind, Action: "update"}
	command, err := resolvedCLIUpdateCommand(spec)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Command = strings.Join(command, " ")

	runCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		// Homebrew may need to refresh its API metadata before upgrading a Cask.
		// On a stale installation that first refresh can legitimately exceed the
		// shorter package-install timeout used by SDK frameworks.
		runCtx, cancel = context.WithTimeout(ctx, cliUpdateTimeout)
		defer cancel()
	}
	cmd := frameworkCommandContext(runCtx, command[0], command[1:]...)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	res.Log = string(out)
	if err != nil {
		res.Error = fmt.Sprintf("update failed: %v", err)
		return res
	}

	status := Detect(spec, DetectPrereqs())
	res.Version = normalizeSDKVersion(status.Version)
	if !status.Installed {
		res.Error = "update command completed but CLI was not found on PATH"
		return res
	}
	if res.Version == "" {
		res.Error = "update command completed but the installed version could not be verified"
		return res
	}
	advanced := sdkVersionGreater(res.Version, check.CurrentVersion)
	if spec.LatestURL != "" {
		// Native builds such as Cursor use date+hash identifiers. Hashes are not
		// ordered, so success means the installer selected the exact build exposed
		// by the official latest-version endpoint.
		advanced = res.Version == check.LatestVersion
	}
	if !advanced {
		res.Error = fmt.Sprintf(
			"update command completed but version did not advance (still %s; latest %s)",
			res.Version, check.LatestVersion,
		)
		return res
	}
	res.OK = true
	return res
}

func install(ctx context.Context, kind, action string) InstallResult {
	res := InstallResult{Kind: kind, Action: action}

	spec, ok := Lookup(kind)
	if !ok {
		res.Error = fmt.Sprintf("unknown framework %q", kind)
		return res
	}
	if !spec.Supported {
		res.Error = fmt.Sprintf("framework %q is not yet supported for automatic install", kind)
		return res
	}
	if !spec.InstallSupported {
		res.Error = fmt.Sprintf("framework %q does not support automatic install", kind)
		return res
	}
	if spec.KindType == KindCLI {
		return installCLI(ctx, spec)
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

	packages := append([]string(nil), spec.Packages...)
	if action == "update" {
		for i, pkg := range packages {
			packages[i] = pkg + "@latest"
		}
	}
	args := append([]string{"install", "--no-audit", "--no-fund"}, packages...)
	res.Command = "npm " + strings.Join(args, " ")

	runCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
	}

	cmd := frameworkCommandContext(runCtx, "npm", args...)
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

func installCLI(ctx context.Context, spec Spec) InstallResult {
	res := InstallResult{Kind: spec.Kind, Action: "install"}
	if status := Detect(spec, DetectPrereqs()); status.Installed {
		res.OK = true
		res.Version = normalizeSDKVersion(status.Version)
		res.Log = fmt.Sprintf("%s is already installed", spec.Display)
		return res
	}

	command := append([]string(nil), spec.InstallCommand...)
	if len(command) == 0 && spec.NPMPackage != "" {
		command = []string{"npm", "install", "-g", spec.NPMPackage + "@latest"}
	}
	if len(command) == 0 {
		res.Error = fmt.Sprintf("framework %q has no install command", spec.Kind)
		return res
	}
	if _, err := exec.LookPath(command[0]); err != nil {
		if command[0] == "npm" {
			res.Error = "npm not found on PATH; install Node.js first"
		} else {
			res.Error = fmt.Sprintf("%s not found on PATH", command[0])
		}
		return res
	}
	res.Command = strings.Join(command, " ")

	runCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, cliUpdateTimeout)
		defer cancel()
	}
	cmd := frameworkCommandContext(runCtx, command[0], command[1:]...)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	res.Log = string(out)
	if err != nil {
		res.Error = fmt.Sprintf("install failed: %v", err)
		return res
	}

	status := Detect(spec, DetectPrereqs())
	res.Version = normalizeSDKVersion(status.Version)
	if !status.Installed {
		res.Error = "install command completed but CLI was not found on PATH"
		return res
	}
	if res.Version == "" {
		res.Error = "install command completed but the installed version could not be verified"
		return res
	}
	res.OK = true
	return res
}
