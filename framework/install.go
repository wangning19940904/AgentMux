package framework

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
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
	// Warning explains a run that succeeded with a problem worth reading,
	// such as a package manager that exited non-zero after the CLI was
	// already upgraded.
	Warning string `json:"warning,omitempty"`
}

// ProgressFunc reports durable installation stages. Percent tracks stage
// completion rather than package-manager download bytes, which are not exposed
// consistently by every supported installer.
type ProgressFunc func(phase, detail string, percent int)

// OutputFunc receives an install or update command's combined output as the
// command produces it, so a long package-manager run shows what it is doing.
type OutputFunc func(chunk []byte)

// InstallOptions carries explicit acknowledgement for restricted catalog
// entries and the machine's install settings. Callers cannot supply a command
// themselves: custom commands come only from the administrator's stored
// settings and must be echoed back in ConfirmedCommand before they run.
type InstallOptions struct {
	AcknowledgeInternal bool
	Settings            InstallSettings
	// ConfirmedCommand is the custom command text the user approved.
	ConfirmedCommand string
	// Forwarded marks a request relayed from another machine's Console. Such
	// requests never run this machine's custom commands.
	Forwarded bool
	// Output, when set, receives command output while the command runs.
	Output OutputFunc
}

// InstallWithProgressOptions installs a framework with explicit restricted-
// entry acknowledgement and optional progress reporting.
func InstallWithProgressOptions(ctx context.Context, kind string, options InstallOptions, progress ProgressFunc) InstallResult {
	return install(ctx, kind, options, progress)
}

// UpdateWithProgress updates a framework with the built-in command.
func UpdateWithProgress(ctx context.Context, kind string, progress ProgressFunc) InstallResult {
	return UpdateWithProgressOptions(ctx, kind, InstallOptions{}, progress)
}

// UpdateWithProgressOptions updates a framework while reporting the update
// check, installer execution, and verification stages.
func UpdateWithProgressOptions(ctx context.Context, kind string, options InstallOptions, progress ProgressFunc) InstallResult {
	reportProgress(progress, "checking", "", 8)
	// An explicit update always asks the registry again rather than trusting
	// the page's cached answer.
	check, latestErr := checkUpdate(ctx, kind, options.Settings, true)
	repair := check.Repairable && latestErr == nil
	if check.Error != "" && latestErr == nil && !repair {
		return InstallResult{Kind: kind, Action: "update", Error: check.Error}
	}
	if latestErr == nil && !repair && !check.UpdateAvailable {
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
	if latestErr != nil {
		// The installation and its version are known; only the registry
		// lookup failed (often a dropped connection). The package manager does
		// its own resolution, so run the update instead of failing on the
		// pre-check, and verify the result without a target version.
		check.Error = ""
		check.LatestVersion = ""
	}
	if repair {
		// The CLI does not run, so its current version is unknown; any
		// version it reports after reinstalling counts as repaired.
		note := fmt.Sprintf("%s\nReinstalling %s to repair it.\n", check.Error, check.Display)
		check.Error = ""
		res := updateCLIWithProgress(ctx, spec, check, options, progress)
		res.Log = note + res.Log
		return res
	}
	res := updateCLIWithProgress(ctx, spec, check, options, progress)
	if latestErr != nil {
		note := fmt.Sprintf("Latest version could not be checked before updating (%v); ran the update anyway.\n", latestErr)
		res.Log = note + res.Log
	}
	return res
}

// UninstallWithProgress removes a CLI only through a catalog-owned command and
// verifies that the executable is no longer discoverable. Configuration and
// session data are preserved whenever the upstream uninstaller supports it.
func UninstallWithProgress(ctx context.Context, kind string, progress ProgressFunc) InstallResult {
	res := InstallResult{Kind: kind, Action: "uninstall"}
	reportProgress(progress, "preparing", "", 5)
	spec, ok := Lookup(kind)
	if !ok {
		res.Error = fmt.Sprintf("unknown framework %q", kind)
		return res
	}
	if spec.KindType != KindCLI || !spec.UninstallSupported {
		res.Error = fmt.Sprintf("framework %q does not support automatic uninstall", kind)
		return res
	}
	if status := Detect(spec, DetectPrereqs()); !status.Installed {
		res.OK = true
		res.Log = fmt.Sprintf("%s is already uninstalled", spec.Display)
		return res
	}
	command := append([]string(nil), spec.UninstallCommand...)
	if len(command) == 0 && spec.NPMPackage != "" {
		command = []string{"npm", "uninstall", "-g", spec.NPMPackage}
	}
	if len(command) == 0 {
		res.Error = fmt.Sprintf("framework %q has no uninstall command", kind)
		return res
	}
	if _, err := exec.LookPath(command[0]); err != nil {
		res.Error = fmt.Sprintf("%s not found on PATH", command[0])
		return res
	}
	res.Command = strings.Join(command, " ")
	reportProgress(progress, "uninstalling", res.Command, 30)

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
		res.Error = fmt.Sprintf("uninstall failed: %v", err)
		return res
	}
	reportProgress(progress, "verifying", "", 88)
	if status := Detect(spec, DetectPrereqs()); status.Installed {
		res.Error = "uninstall command completed but CLI is still available on PATH"
		return res
	}
	res.OK = true
	return res
}

func updateCLIWithProgress(ctx context.Context, spec Spec, check UpdateCheck, options InstallOptions, progress ProgressFunc) InstallResult {
	res := InstallResult{Kind: spec.Kind, Action: "update"}
	plan, err := PlanCommand(spec, "update", options.Settings)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Command = plan.Command
	if err := authorizeCommand(spec, "update", plan, options); err != nil {
		res.Error = err.Error()
		return res
	}
	command := plan.Argv
	if _, err := exec.LookPath(command[0]); err != nil {
		res.Error = fmt.Sprintf("%s not found on PATH", command[0])
		return res
	}
	reportProgress(progress, "updating", res.Command, 30)

	runCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		// Homebrew and native self-updaters may need to refresh metadata before
		// upgrading, so allow a full CLI-update window.
		runCtx, cancel = context.WithTimeout(ctx, cliUpdateTimeout)
		defer cancel()
	}
	out, err := runCLIUpdateCommand(runCtx, command, options.Output)
	res.Log = string(out)
	if err != nil {
		fallback, ok := npmFallbackUpdatePlan(spec, plan, res.Log, options.Settings)
		if ok {
			if _, lookErr := exec.LookPath("npm"); lookErr != nil {
				ok = false
			}
		}
		if !ok {
			// Package managers can fail after the upgrade itself, for example
			// Homebrew when it cannot generate shell completions for the new
			// binary. The installed version is what matters.
			version, advanced, broken := versionAdvancedAfterFailure(spec, check)
			if advanced {
				res.Version = version
				res.Warning = fmt.Sprintf("%s exited with an error (%v), but %s is now %s (was %s).", res.Command, err, spec.Display, version, check.CurrentVersion)
				res.Log += "\n" + res.Warning + "\n"
				res.OK = true
				return res
			}
			res.Error = fmt.Sprintf("update failed: %v", err)
			if broken != "" {
				// The package manager may have removed the old version and
				// left a link to a binary that is gone (for example one
				// macOS Gatekeeper moved to the Trash on its first run).
				res.Error += "; after the update " + broken
			}
			return res
		}
		// The native updater could not tell how the CLI was installed. npm
		// publishes the same CLI, so update through it instead.
		res.Log += fmt.Sprintf("\n%s could not detect its installation method; updating with npm instead.\n", res.Command)
		res.Command = fallback.Command
		reportProgress(progress, "updating", res.Command, 50)
		out, err = runCLIUpdateCommand(runCtx, fallback.Argv, options.Output)
		res.Log += string(out)
		if err != nil {
			res.Error = fmt.Sprintf("update failed: %v", err)
			return res
		}
	}

	reportProgress(progress, "verifying", "", 88)
	forgetLatestVersions(spec.Kind)
	status := detectInstalledVersion(spec)
	res.Version = normalizeSDKVersion(status.Version)
	if !status.Installed {
		res.Error = "update command completed but CLI was not found on PATH"
		return res
	}
	if res.Version == "" {
		res.Error = "update command completed but the installed version could not be verified: " + unrunnableCLIError(spec, status)
		return res
	}
	if !updateAdvanced(spec, check, res.Version) {
		res.Error = fmt.Sprintf(
			"update command completed but version did not advance (still %s; latest %s)",
			res.Version, check.LatestVersion,
		)
		return res
	}
	if check.LatestVersion == "" && res.Version == check.CurrentVersion {
		res.Log += fmt.Sprintf("\n%s is still %s; the package manager found nothing newer.\n", spec.Display, res.Version)
	}
	res.OK = true
	return res
}

// updateAdvanced reports whether the version installed after an update is
// acceptable. Without a known latest version (the registry lookup failed),
// a successful package-manager run is trusted.
func updateAdvanced(spec Spec, check UpdateCheck, installed string) bool {
	switch {
	case check.LatestVersion == "":
		return true
	case spec.ExactLatest:
		// Native builds such as Cursor use date+hash identifiers. Hashes are not
		// ordered, so success means the installer selected the exact build
		// exposed by the official latest-version endpoint.
		return installed == check.LatestVersion
	default:
		return sdkVersionGreater(installed, check.CurrentVersion)
	}
}

// versionAdvancedAfterFailure re-reads the installed version after a failed
// update command and reports whether the CLI was upgraded anyway. When the
// CLI no longer runs at all, broken says why.
func versionAdvancedAfterFailure(spec Spec, check UpdateCheck) (version string, advanced bool, broken string) {
	ForgetVersion(spec.Kind)
	status := Detect(spec, DetectPrereqs())
	version = normalizeSDKVersion(status.Version)
	if !status.Installed {
		return "", false, fmt.Sprintf("%s is no longer found on PATH", spec.Display)
	}
	if version == "" {
		return "", false, unrunnableCLIError(spec, status)
	}
	if check.CurrentVersion == "" {
		return version, false, ""
	}
	if spec.ExactLatest {
		return version, check.LatestVersion != "" && version == check.LatestVersion && version != check.CurrentVersion, ""
	}
	return version, sdkVersionGreater(version, check.CurrentVersion), ""
}

func runCLIUpdateCommand(ctx context.Context, command []string, output OutputFunc) ([]byte, error) {
	run := func() ([]byte, error) {
		cmd := frameworkCommandContext(ctx, command[0], command[1:]...)
		cmd.Env = cliUpdateEnvironment(os.Environ(), runtime.GOOS)
		return runCommandOutput(cmd, output)
	}
	out, err := run()
	if err == nil || filepath.Base(command[0]) != "npm" {
		return out, err
	}
	stale, ok := staleNPMRetiredDir(string(out))
	if !ok {
		return out, err
	}
	// npm moves the old package to a retired sibling before installing the
	// new one, and always picks the same name for it. A run that was
	// interrupted leaves that folder behind, and every later install fails
	// with ENOTEMPTY (and npm's rollback can leave the package half-restored).
	// The folder is npm's own leftover copy, so remove it and try once more.
	if removeErr := os.RemoveAll(stale); removeErr != nil {
		return out, err
	}
	note := []byte(fmt.Sprintf("\nRemoved %s, left behind by an interrupted npm run; retrying.\n", stale))
	if output != nil {
		output(note)
	}
	retryOut, retryErr := run()
	return append(append(out, note...), retryOut...), retryErr
}

var npmRenameNotEmptyRE = regexp.MustCompile(`ENOTEMPTY: directory not empty, rename '([^']+)' -> '([^']+)'`)

// staleNPMRetiredDir finds the retired package folder an npm ENOTEMPTY
// failure names. Only a sibling of the package called .<name>-<8 characters>,
// the form npm uses, is accepted.
func staleNPMRetiredDir(log string) (string, bool) {
	match := npmRenameNotEmptyRE.FindStringSubmatch(log)
	if match == nil {
		return "", false
	}
	pkg, dest := filepath.Clean(match[1]), filepath.Clean(match[2])
	if !filepath.IsAbs(pkg) || filepath.Dir(pkg) != filepath.Dir(dest) {
		return "", false
	}
	retired := regexp.MustCompile(`^\.` + regexp.QuoteMeta(filepath.Base(pkg)) + `-[A-Za-z0-9]{8}$`)
	if !retired.MatchString(filepath.Base(dest)) {
		return "", false
	}
	return dest, true
}

// runCommandOutput is CombinedOutput that also hands each chunk to output.
func runCommandOutput(cmd *exec.Cmd, output OutputFunc) ([]byte, error) {
	if output == nil {
		return cmd.CombinedOutput()
	}
	// One pointer for stdout and stderr makes exec copy both through a single
	// pipe; the lock covers any other caller anyway.
	writer := &outputWriter{output: output}
	cmd.Stdout = writer
	cmd.Stderr = writer
	err := cmd.Run()
	return writer.buffer.Bytes(), err
}

type outputWriter struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	output OutputFunc
}

func (w *outputWriter) Write(chunk []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buffer.Write(chunk)
	w.output(append([]byte(nil), chunk...))
	return len(chunk), nil
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
	return installCLIWithProgress(ctx, spec, options, progress)
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

func installCLIWithProgress(ctx context.Context, spec Spec, options InstallOptions, progress ProgressFunc) InstallResult {
	res := InstallResult{Kind: spec.Kind, Action: "install"}
	reportProgress(progress, "preparing", "", 5)
	if status := Detect(spec, DetectPrereqs()); status.Installed {
		res.OK = true
		res.Version = normalizeSDKVersion(status.Version)
		res.Log = fmt.Sprintf("%s is already installed", spec.Display)
		return res
	}

	plan, err := PlanCommand(spec, "install", options.Settings)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Command = plan.Command
	if err := authorizeCommand(spec, "install", plan, options); err != nil {
		res.Error = err.Error()
		return res
	}
	command := plan.Argv
	if _, err := exec.LookPath(command[0]); err != nil {
		if command[0] == "npm" {
			res.Error = "npm not found on PATH; install Node.js first"
		} else {
			res.Error = fmt.Sprintf("%s not found on PATH", command[0])
		}
		return res
	}
	reportProgress(progress, "installing", res.Command, 30)

	runCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, cliUpdateTimeout)
		defer cancel()
	}
	cmd := frameworkCommandContext(runCtx, command[0], command[1:]...)
	cmd.Env = os.Environ()
	out, err := runCommandOutput(cmd, options.Output)
	res.Log = string(out)
	if err != nil {
		res.Error = fmt.Sprintf("install failed: %v", err)
		return res
	}

	reportProgress(progress, "verifying", "", 88)
	forgetLatestVersions(spec.Kind)
	status := detectInstalledVersion(spec)
	res.Version = normalizeSDKVersion(status.Version)
	if !status.Installed {
		res.Error = "install command completed but CLI was not found on PATH"
		return res
	}
	if res.Version == "" {
		res.Error = "install command completed but the installed version could not be verified: " + unrunnableCLIError(spec, status)
		return res
	}
	res.OK = true
	return res
}

// Right after an install or update, the first run of a CLI can be much slower
// than usual: macOS scans a newly written native binary before it may start,
// and Node-based launchers load a fresh package. Routine detection would time
// out and report the version as unknown, so verification waits longer and
// tries again before concluding that the CLI does not run.
var (
	verifyVersionTimeout  = 45 * time.Second
	verifyVersionAttempts = 3
	verifyVersionDelay    = 2 * time.Second
)

// detectInstalledVersion detects a CLI that was just installed or updated.
// The binary (or a wrapper in front of it) may keep its size and mtime across
// an update, so a cached version is never trusted.
func detectInstalledVersion(spec Spec) Status {
	var status Status
	for attempt := 0; attempt < verifyVersionAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(verifyVersionDelay)
		}
		ForgetVersion(spec.Kind)
		status = detectWithTimeout(spec, verifyVersionTimeout)
		if !status.Installed || normalizeSDKVersion(status.Version) != "" {
			return status
		}
	}
	return status
}

func reportProgress(progress ProgressFunc, phase, detail string, percent int) {
	if progress != nil {
		progress(phase, detail, percent)
	}
}
