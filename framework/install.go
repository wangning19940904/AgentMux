package framework

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
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

// ProgressFunc reports durable installation stages. Percent tracks stage
// completion rather than package-manager download bytes, which are not exposed
// consistently by every supported installer.
type ProgressFunc func(phase, detail string, percent int)

// InstallOptions carries explicit acknowledgement for restricted catalog
// entries. Callers cannot override catalog-owned commands or packages.
type InstallOptions struct {
	AcknowledgeInternal bool
}

// Install installs a public catalogued framework with its catalog-owned
// command. Internal-only entries require InstallWithOptions.
func Install(ctx context.Context, kind string) InstallResult {
	return InstallWithProgressOptions(ctx, kind, InstallOptions{}, nil)
}

// InstallWithProgress installs a framework while reporting preparation,
// command execution, and verification stages.
func InstallWithProgress(ctx context.Context, kind string, progress ProgressFunc) InstallResult {
	return InstallWithProgressOptions(ctx, kind, InstallOptions{}, progress)
}

// InstallWithOptions installs a framework with explicit restricted-entry
// acknowledgement.
func InstallWithOptions(ctx context.Context, kind string, options InstallOptions) InstallResult {
	return InstallWithProgressOptions(ctx, kind, options, nil)
}

// InstallWithProgressOptions is the progress-reporting InstallWithOptions.
func InstallWithProgressOptions(ctx context.Context, kind string, options InstallOptions, progress ProgressFunc) InstallResult {
	return install(ctx, kind, options, progress)
}

// Update updates an installed framework only when its catalog-owned version
// source reports a newer release. The update is checked again server-side so a
// stale UI cannot trigger an unnecessary command.
func Update(ctx context.Context, kind string) InstallResult {
	return UpdateWithProgress(ctx, kind, nil)
}

// UpdateWithProgress updates a framework while reporting the update check,
// installer execution, and verification stages.
func UpdateWithProgress(ctx context.Context, kind string, progress ProgressFunc) InstallResult {
	reportProgress(progress, "checking", "", 8)
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
	if spec.KindType != KindCLI {
		return InstallResult{Kind: kind, Action: "update", Error: fmt.Sprintf("framework %q does not have a runnable adapter", kind)}
	}
	return updateCLIWithProgress(ctx, spec, check, progress)
}

func updateCLI(ctx context.Context, spec Spec, check UpdateCheck) InstallResult {
	return updateCLIWithProgress(ctx, spec, check, nil)
}

func updateCLIWithProgress(ctx context.Context, spec Spec, check UpdateCheck, progress ProgressFunc) InstallResult {
	res := InstallResult{Kind: spec.Kind, Action: "update"}
	command, err := resolvedCLIUpdateCommand(spec)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Command = strings.Join(command, " ")
	reportProgress(progress, "updating", res.Command, 30)

	runCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		// Homebrew and native self-updaters may need to refresh metadata before
		// upgrading, so allow a full CLI-update window.
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

	reportProgress(progress, "verifying", "", 88)
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
	if spec.ExactLatest {
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

func install(ctx context.Context, kind string, options InstallOptions, progress ProgressFunc) InstallResult {
	res := InstallResult{Kind: kind, Action: "install"}
	reportProgress(progress, "preparing", "", 5)

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
	if spec.InternalOnly && !options.AcknowledgeInternal {
		res.Error = fmt.Sprintf("framework %q is only available inside ByteDance; explicit acknowledgement is required", kind)
		return res
	}
	if !installPlatformSupported(spec, runtime.GOOS) {
		if runtime.GOOS == "windows" && spec.Kind == "traecli" {
			res.Error = "TRAE CLI automatic installation is unsupported on native Windows; run AgentMux inside WSL"
		} else {
			res.Error = fmt.Sprintf("framework %q cannot be installed automatically on %s", kind, runtime.GOOS)
		}
		return res
	}
	if spec.KindType != KindCLI {
		res.Error = fmt.Sprintf("framework %q does not have a runnable adapter", kind)
		return res
	}
	return installCLIWithProgress(ctx, spec, progress)
}

func installPlatformSupported(spec Spec, goos string) bool {
	if len(spec.InstallPlatforms) == 0 {
		return true
	}
	for _, candidate := range spec.InstallPlatforms {
		if candidate == goos {
			return true
		}
	}
	return false
}

func installCLI(ctx context.Context, spec Spec) InstallResult {
	return installCLIWithProgress(ctx, spec, nil)
}

func installCLIWithProgress(ctx context.Context, spec Spec, progress ProgressFunc) InstallResult {
	res := InstallResult{Kind: spec.Kind, Action: "install"}
	reportProgress(progress, "preparing", "", 5)
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
	reportProgress(progress, "installing", res.Command, 30)

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

	reportProgress(progress, "verifying", "", 88)
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

func reportProgress(progress ProgressFunc, phase, detail string, percent int) {
	if progress != nil {
		progress(phase, detail, percent)
	}
}
