// Package tools implements installable local tool catalogs for AgentMux.
package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const agentMuxSkillsDirToken = "{agentmux_skills_dir}"

// CLILinkedSkillSpec describes a skill whose lifecycle is managed with a CLI.
// Install commands are catalog-owned and may use agentMuxSkillsDirToken to
// target AgentMux's global skill library without invoking a shell.
type CLILinkedSkillSpec struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	Source             string   `json:"source,omitempty"`
	InstallCommand     []string `json:"install_command,omitempty"`
	MatchCLIVersion    bool     `json:"match_cli_version,omitempty"`
	VersionPolicyLabel string   `json:"version_policy_label,omitempty"`
	Note               string   `json:"note,omitempty"`
}

// CLISpec describes one managed local CLI.
type CLISpec struct {
	ID                 string               `json:"id"`
	Name               string               `json:"name"`
	Bin                string               `json:"bin"`
	Package            string               `json:"package"`
	Registry           string               `json:"registry,omitempty"`
	InstallCommand     []string             `json:"install_command,omitempty"`
	PostInstallCommand []string             `json:"post_install_command,omitempty"`
	UpdateCommand      []string             `json:"update_command,omitempty"`
	UninstallCommand   []string             `json:"-"`
	LatestVersionURL   string               `json:"-"`
	LinkedSkills       []CLILinkedSkillSpec `json:"linked_skills,omitempty"`
	LoginSupported     bool                 `json:"login_supported,omitempty"`
	UninstallSupported bool                 `json:"uninstall_supported"`
	InternalOnly       bool                 `json:"internal_only,omitempty"`
	Note               string               `json:"note,omitempty"`
}

// CLILinkedSkillStatus is the detected state of a CLI-managed skill in the
// AgentMux global library.
type CLILinkedSkillStatus struct {
	Spec      CLILinkedSkillSpec `json:"spec"`
	Installed bool               `json:"installed"`
	InSync    bool               `json:"in_sync"`
	Path      string             `json:"path,omitempty"`
	Version   string             `json:"version,omitempty"`
	Detail    string             `json:"detail,omitempty"`
}

// CLIStatus is the detected state for a local CLI.
type CLIStatus struct {
	Spec         CLISpec                `json:"spec"`
	Installed    bool                   `json:"installed"`
	Path         string                 `json:"path,omitempty"`
	Version      string                 `json:"version,omitempty"`
	Detail       string                 `json:"detail,omitempty"`
	LinkedSkills []CLILinkedSkillStatus `json:"linked_skills,omitempty"`
}

// CLILinkedSkillResult reports one linked-skill sync step.
type CLILinkedSkillResult struct {
	ID      string `json:"id"`
	OK      bool   `json:"ok"`
	Command string `json:"command,omitempty"`
	Log     string `json:"log,omitempty"`
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

// CLIInstallResult reports the install/update outcome.
type CLIInstallResult struct {
	ID           string                 `json:"id"`
	Action       string                 `json:"action"`
	OK           bool                   `json:"ok"`
	Command      string                 `json:"command,omitempty"`
	Log          string                 `json:"log,omitempty"`
	Version      string                 `json:"version,omitempty"`
	LinkedSkills []CLILinkedSkillResult `json:"linked_skills,omitempty"`
	Error        string                 `json:"error,omitempty"`
}

// CLIUpdateCheck reports whether an installed CLI has a newer package version.
type CLIUpdateCheck struct {
	ID              string    `json:"id"`
	Installed       bool      `json:"installed"`
	CurrentVersion  string    `json:"current_version,omitempty"`
	LatestVersion   string    `json:"latest_version,omitempty"`
	UpdateAvailable bool      `json:"update_available"`
	CheckedAt       time.Time `json:"checked_at"`
	Error           string    `json:"error,omitempty"`
}

// ProgressFunc reports durable installation stages. Percent describes stage
// completion, not package-manager download bytes (which npm and Homebrew do not
// expose consistently).
type ProgressFunc func(phase, detail string, percent int)

// CLIInstallOptions carries explicit acknowledgement for restricted catalog
// entries.
type CLIInstallOptions struct {
	AcknowledgeInternal bool
}

var cliCatalog = []CLISpec{
	{
		ID: "lark-cli", Name: "Lark CLI", Bin: "lark-cli", Package: "@larksuite/cli",
		InstallCommand:     []string{"npm", "install", "-g", "@larksuite/cli@latest"},
		UpdateCommand:      []string{"lark-cli", "update"},
		LoginSupported:     true,
		UninstallSupported: true,
		Note:               "Feishu/Lark Open Platform CLI and official skills updater.",
	},
	{
		ID: "bytedcli", Name: "bytedcli", Bin: "bytedcli", Package: "@bytedance-dev/bytedcli",
		Registry:           "https://bnpm.byted.org/",
		InstallCommand:     []string{"npm", "install", "-g", "@bytedance-dev/bytedcli@latest", "--registry=https://bnpm.byted.org/"},
		UpdateCommand:      []string{"npm", "install", "-g", "@bytedance-dev/bytedcli@latest", "--registry=https://bnpm.byted.org/"},
		UninstallSupported: true,
		InternalOnly:       true,
		Note:               "ByteDance internal developer CLI.",
	},
	{
		ID: "opencli", Name: "OpenCLI", Bin: "opencli", Package: "@jackwener/opencli",
		InstallCommand:     []string{"npm", "install", "-g", "@jackwener/opencli@latest"},
		UpdateCommand:      []string{"npm", "install", "-g", "@jackwener/opencli@latest"},
		UninstallSupported: true,
		Note:               "AI-native runtime and CLI hub that turns websites and browser sessions into command-line tools.",
	},
	{
		ID: "agent-browser", Name: "agent-browser", Bin: "agent-browser", Package: "agent-browser",
		InstallCommand:     []string{"npm", "install", "-g", "agent-browser@latest"},
		PostInstallCommand: []string{"agent-browser", "install"},
		UpdateCommand:      []string{"agent-browser", "upgrade"},
		UninstallSupported: true,
		Note:               "Fast browser automation CLI for AI agents with version-matched bundled skills.",
	},
	{
		ID: "cis-cli", Name: "CIS CLI", Bin: "cis-cli", Package: "@byted/cis-cli",
		Registry:           "https://bnpm.byted.org/",
		InstallCommand:     []string{"npm", "install", "-g", "@byted/cis-cli@latest", "--registry=https://bnpm.byted.org/"},
		UpdateCommand:      []string{"npm", "install", "-g", "@byted/cis-cli@latest", "--registry=https://bnpm.byted.org/"},
		UninstallSupported: true,
		LinkedSkills: []CLILinkedSkillSpec{
			{
				ID: "cis-cli", Name: "cis-cli Skill", Source: "skills.byted.org/default/public/cis-cli",
				InstallCommand:     []string{"cis-cli", "install-skills", "--dir", agentMuxSkillsDirToken, "--force"},
				MatchCLIVersion:    true,
				VersionPolicyLabel: "version-matched with CIS CLI",
				Note:               "Bundled enterprise-service instructions distributed from the installed CLI into AgentMux.",
			},
		},
		InternalOnly: true,
		Note:         "ByteDance enterprise services CLI with a version-matched companion Skill managed as one unit.",
	},
	{
		ID: "github-cli", Name: "GitHub CLI", Bin: "gh", Package: "gh",
		InstallCommand:     []string{"brew", "install", "gh"},
		UpdateCommand:      []string{"brew", "upgrade", "gh"},
		UninstallCommand:   []string{"brew", "uninstall", "gh"},
		LatestVersionURL:   "https://api.github.com/repos/cli/cli/releases/latest",
		LoginSupported:     true,
		UninstallSupported: true,
		Note:               "GitHub's official CLI for pull requests, issues, Actions, and repositories.",
	},
}

// DetectCLIs detects all managed CLIs.
func DetectCLIs(ctx context.Context) []CLIStatus {
	out := make([]CLIStatus, 0, len(cliCatalog))
	for _, spec := range cliCatalog {
		out = append(out, DetectCLI(ctx, spec))
	}
	return out
}

// DetectCLI detects one CLI.
func DetectCLI(ctx context.Context, spec CLISpec) CLIStatus {
	st := CLIStatus{Spec: spec}
	path, err := resolveCLIExecutable(spec.Bin)
	if err != nil {
		st.Detail = "not found on PATH"
		st.LinkedSkills = detectLinkedSkills(spec, "")
		return st
	}
	st.Installed = true
	st.Path = path
	if version, err := commandOutput(ctx, spec.Bin, "--version"); err == nil {
		st.Version = strings.TrimSpace(version)
	} else {
		st.Detail = err.Error()
	}
	st.LinkedSkills = detectLinkedSkills(spec, st.Version)
	return st
}

// CheckCLIUpdate compares the installed CLI version with the registry latest.
func CheckCLIUpdate(ctx context.Context, id string) CLIUpdateCheck {
	id = strings.TrimSpace(id)
	res := CLIUpdateCheck{ID: id, CheckedAt: time.Now()}
	spec, ok := lookupCLI(id)
	if !ok {
		res.Error = fmt.Sprintf("unknown CLI %q", id)
		return res
	}
	status := DetectCLI(ctx, spec)
	res.Installed = status.Installed
	if !status.Installed {
		res.Error = fmt.Sprintf("CLI %q is not installed", id)
		return res
	}
	current := normalizeVersion(status.Version)
	if current == "" {
		res.Error = fmt.Sprintf("could not parse installed version from %q", status.Version)
		return res
	}
	res.CurrentVersion = current

	latest, err := latestCLIVersion(ctx, spec)
	if err != nil {
		res.Error = fmt.Sprintf("check update failed: %v", err)
		return res
	}
	latest = normalizeVersion(latest)
	if latest == "" {
		res.Error = "registry did not return a latest version"
		return res
	}
	res.LatestVersion = latest
	res.UpdateAvailable = versionGreater(latest, current)
	return res
}

// InstallCLIWithOptions installs or updates a whitelisted CLI with explicit
// restricted-entry acknowledgement.
func InstallCLIWithOptions(ctx context.Context, id, action string, options CLIInstallOptions) CLIInstallResult {
	return InstallCLIWithProgressOptions(ctx, id, action, options, nil)
}

// InstallCLIWithProgressOptions is the progress-reporting InstallCLIWithOptions.
func InstallCLIWithProgressOptions(ctx context.Context, id, action string, options CLIInstallOptions, progress ProgressFunc) CLIInstallResult {
	id = strings.TrimSpace(id)
	action = strings.TrimSpace(action)
	if action == "" {
		action = "install"
	}
	res := CLIInstallResult{ID: id, Action: action}
	spec, ok := lookupCLI(id)
	if !ok {
		res.Error = fmt.Sprintf("unknown CLI %q", id)
		return res
	}
	if action != "install" && action != "update" && action != "uninstall" {
		res.Error = "action must be install, update, or uninstall"
		return res
	}
	if action == "uninstall" {
		return uninstallCLIWithProgress(ctx, spec, progress)
	}
	if action == "install" && spec.InternalOnly && !options.AcknowledgeInternal {
		res.Error = fmt.Sprintf("CLI %q is only available inside ByteDance; explicit acknowledgement is required", id)
		return res
	}

	reportProgress(progress, "preparing", "", 5)
	statusBefore := DetectCLI(ctx, spec)
	cmdArgs := spec.InstallCommand
	if action == "update" {
		if !statusBefore.Installed {
			res.Error = fmt.Sprintf("CLI %q is not installed; install it first", id)
			return res
		}
		reportProgress(progress, "checking", "", 15)
		check := CheckCLIUpdate(ctx, id)
		if check.Error != "" {
			res.Error = check.Error
			return res
		}
		if !check.UpdateAvailable {
			res.Version = statusBefore.Version
			res.Log = fmt.Sprintf("%s is already up to date (current %s, latest %s)", spec.Name, check.CurrentVersion, check.LatestVersion)
			return syncAfterCLIChange(ctx, spec, res, progress)
		}
		if len(spec.UpdateCommand) > 0 {
			cmdArgs = spec.UpdateCommand
		}
	}
	if len(cmdArgs) == 0 {
		res.Error = fmt.Sprintf("CLI %q has no install command", id)
		return res
	}
	commands := [][]string{cmdArgs}
	if action == "install" && len(spec.PostInstallCommand) > 0 {
		commands = append(commands, spec.PostInstallCommand)
	}
	commandLabels := make([]string, 0, len(commands))
	for _, args := range commands {
		commandLabels = append(commandLabels, strings.Join(args, " "))
	}
	res.Command = strings.Join(commandLabels, " && ")

	runCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
	}
	commandPhase := "installing"
	if action == "update" {
		commandPhase = "updating"
	}
	logOutput, err := runCLICommandsWithProgress(runCtx, spec, commands, progress, commandPhase, 30, 72)
	res.Log = appendLog(res.Log, logOutput)
	if err != nil {
		res.Error = fmt.Sprintf("%s failed: %v", action, err)
		return res
	}
	reportProgress(progress, "verifying", "", 76)
	status := DetectCLI(ctx, spec)
	res.Version = status.Version
	if !status.Installed {
		res.Error = "command completed but CLI was not found on PATH"
		return res
	}
	if spec.ID == "bytedcli" {
		if out, err := commandOutput(ctx, "npm", "outdated", "-g", spec.Package, "--json"); err == nil && strings.TrimSpace(out) != "" && strings.TrimSpace(out) != "{}" {
			res.Log += "\n" + out
		}
	}
	return syncAfterCLIChange(ctx, spec, res, progress)
}

// uninstallCLIWithProgress removes a managed CLI using a catalog-owned command,
// verifies that the executable disappeared, and only then removes any linked
// skills managed as part of the same lifecycle unit. User configuration and
// caches are intentionally left untouched.
func uninstallCLIWithProgress(ctx context.Context, spec CLISpec, progress ProgressFunc) CLIInstallResult {
	res := CLIInstallResult{ID: spec.ID, Action: "uninstall"}
	reportProgress(progress, "preparing", "", 5)
	if !spec.UninstallSupported {
		res.Error = fmt.Sprintf("CLI %q does not support automatic uninstall", spec.ID)
		return res
	}

	status := DetectCLI(ctx, spec)
	if status.Installed {
		command := append([]string(nil), spec.UninstallCommand...)
		if len(command) == 0 && spec.Package != "" {
			command = []string{"npm", "uninstall", "-g", spec.Package}
		}
		if len(command) == 0 {
			res.Error = fmt.Sprintf("CLI %q has no uninstall command", spec.ID)
			return res
		}
		res.Command = strings.Join(command, " ")
		reportProgress(progress, "uninstalling", res.Command, 30)

		runCtx := ctx
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			runCtx, cancel = context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
		}
		logOutput, err := runCLICommandsWithProgress(runCtx, spec, [][]string{command}, progress, "uninstalling", 30, 74)
		res.Log = appendLog(res.Log, logOutput)
		if err != nil {
			res.Error = fmt.Sprintf("uninstall failed: %v", err)
			return res
		}
		reportProgress(progress, "verifying", "", 78)
		if after := DetectCLI(ctx, spec); after.Installed {
			res.Error = "uninstall command completed but CLI is still available on PATH"
			return res
		}
	} else {
		res.Log = fmt.Sprintf("%s is already uninstalled", spec.Name)
	}

	linkedResults, err := removeLinkedSkills(spec.LinkedSkills, progress)
	res.LinkedSkills = linkedResults
	if err != nil {
		res.Error = fmt.Sprintf("CLI was removed, but linked Skills could not be fully removed: %v", err)
		return res
	}
	res.OK = true
	return res
}

func removeLinkedSkills(linkedSkills []CLILinkedSkillSpec, progress ProgressFunc) ([]CLILinkedSkillResult, error) {
	if len(linkedSkills) == 0 {
		return nil, nil
	}
	root, err := agentMuxSkillsDir()
	if err != nil {
		return nil, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve managed Skill root: %w", err)
	}
	results := make([]CLILinkedSkillResult, 0, len(linkedSkills))
	for index, linked := range linkedSkills {
		reportProgress(progress, "uninstalling", linked.Name, 86+index*10/max(1, len(linkedSkills)))
		result := CLILinkedSkillResult{ID: linked.ID}
		candidate := filepath.Join(root, linked.ID)
		relative, relErr := filepath.Rel(root, candidate)
		if relErr != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			result.Error = "linked Skill path escapes the managed Skill root"
			results = append(results, result)
			return results, fmt.Errorf("%s: %s", linked.Name, result.Error)
		}
		result.Path = filepath.Join(candidate, "SKILL.md")
		if removeErr := os.RemoveAll(candidate); removeErr != nil {
			result.Error = removeErr.Error()
			results = append(results, result)
			return results, fmt.Errorf("%s: %w", linked.Name, removeErr)
		}
		result.OK = true
		result.Log = "removed linked Skill from the AgentMux library"
		results = append(results, result)
	}
	return results, nil
}

// SyncCLILinkedSkills repairs or refreshes the skills managed by a CLI without
// reinstalling the CLI itself.
func SyncCLILinkedSkills(ctx context.Context, id string) CLIInstallResult {
	return SyncCLILinkedSkillsWithProgress(ctx, id, nil)
}

// SyncCLILinkedSkillsWithProgress refreshes managed skills and reports the
// command and verification stages to the caller.
func SyncCLILinkedSkillsWithProgress(ctx context.Context, id string, progress ProgressFunc) CLIInstallResult {
	id = strings.TrimSpace(id)
	res := CLIInstallResult{ID: id, Action: "sync-skills"}
	spec, ok := lookupCLI(id)
	if !ok {
		res.Error = fmt.Sprintf("unknown CLI %q", id)
		return res
	}
	if len(spec.LinkedSkills) == 0 {
		res.Error = fmt.Sprintf("CLI %q has no linked skills", id)
		return res
	}
	reportProgress(progress, "preparing", "", 5)
	status := DetectCLI(ctx, spec)
	if !status.Installed {
		res.Error = fmt.Sprintf("CLI %q is not installed; install it first", id)
		return res
	}
	res.Version = status.Version
	return syncAfterCLIChange(ctx, spec, res, progress)
}

func syncAfterCLIChange(ctx context.Context, spec CLISpec, res CLIInstallResult, progress ProgressFunc) CLIInstallResult {
	if len(spec.LinkedSkills) == 0 {
		res.OK = true
		return res
	}

	runCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
	}
	linkedResults := make([]CLILinkedSkillResult, 0, len(spec.LinkedSkills))
	for i, linked := range spec.LinkedSkills {
		percent := 80 + i*12/max(1, len(spec.LinkedSkills))
		reportProgress(progress, "syncing", linked.Name, percent)
		result := syncLinkedSkill(runCtx, spec, linked, res.Version, progress)
		linkedResults = append(linkedResults, result)
		res.Command = appendCommand(res.Command, result.Command)
		res.Log = appendLog(res.Log, result.Log)
		if !result.OK && res.Error == "" {
			res.Error = fmt.Sprintf("%s is installed, but linked skill %q could not be synced: %s", spec.Name, linked.Name, result.Error)
		}
	}
	res.LinkedSkills = linkedResults
	res.OK = res.Error == ""
	return res
}

func syncLinkedSkill(ctx context.Context, cli CLISpec, linked CLILinkedSkillSpec, cliVersion string, progress ProgressFunc) CLILinkedSkillResult {
	res := CLILinkedSkillResult{ID: linked.ID}
	command, err := resolveLinkedSkillCommand(linked.InstallCommand)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if len(command) == 0 {
		res.Error = "no sync command configured"
		return res
	}
	res.Command = strings.Join(command, " ")
	logOutput, err := runCLICommandsWithProgress(ctx, cli, [][]string{command}, progress, "syncing", 80, 92)
	res.Log = logOutput
	if err != nil {
		res.Error = err.Error()
		return res
	}
	status := detectLinkedSkill(linked, cliVersion)
	res.Path = status.Path
	res.Version = status.Version
	res.OK = status.Installed && status.InSync
	if !res.OK {
		res.Error = status.Detail
		if res.Error == "" {
			res.Error = "sync command completed but the linked skill is not ready"
		}
	}
	return res
}

func runCLICommandsWithProgress(
	ctx context.Context,
	spec CLISpec,
	commands [][]string,
	progress ProgressFunc,
	phase string,
	startPercent int,
	endPercent int,
) (string, error) {
	var logOutput string
	for i, args := range commands {
		if len(args) == 0 {
			return logOutput, fmt.Errorf("empty command")
		}
		executable, err := resolveCLIExecutable(args[0])
		if err != nil {
			return logOutput, missingCLIExecutableError(args[0])
		}
		percent := startPercent
		if len(commands) > 1 {
			percent += (endPercent - startPercent) * i / len(commands)
		}
		reportProgress(progress, phase, strings.Join(args, " "), percent)
		cmd := exec.CommandContext(ctx, executable, args[1:]...)
		cmd.Env = cliEnv(spec)
		out, err := cmd.CombinedOutput()
		logOutput = appendLog(logOutput, string(out))
		if err != nil {
			return logOutput, fmt.Errorf("running %q: %w", strings.Join(args, " "), err)
		}
	}
	return logOutput, nil
}

func reportProgress(progress ProgressFunc, phase, detail string, percent int) {
	if progress != nil {
		progress(phase, detail, percent)
	}
}

func latestCLIVersion(ctx context.Context, spec CLISpec) (string, error) {
	if spec.LatestVersionURL != "" {
		return latestReleaseVersion(ctx, spec.LatestVersionURL)
	}
	args := []string{"view", spec.Package, "version", "--silent"}
	if spec.Registry != "" {
		args = append(args, "--registry="+spec.Registry)
	}
	return commandOutputWithEnv(ctx, cliEnv(spec), "npm", args...)
}

func latestReleaseVersion(ctx context.Context, url string) (string, error) {
	runCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(runCtx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("latest-version endpoint returned %s", resp.Status)
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release); err != nil {
		return "", fmt.Errorf("decode latest release: %w", err)
	}
	if strings.TrimSpace(release.TagName) == "" {
		return "", fmt.Errorf("latest-version endpoint returned no tag_name")
	}
	return release.TagName, nil
}

func detectLinkedSkills(spec CLISpec, cliVersion string) []CLILinkedSkillStatus {
	if len(spec.LinkedSkills) == 0 {
		return nil
	}
	out := make([]CLILinkedSkillStatus, 0, len(spec.LinkedSkills))
	for _, linked := range spec.LinkedSkills {
		out = append(out, detectLinkedSkill(linked, cliVersion))
	}
	return out
}

func detectLinkedSkill(spec CLILinkedSkillSpec, cliVersion string) CLILinkedSkillStatus {
	st := CLILinkedSkillStatus{Spec: spec}
	root, err := agentMuxSkillsDir()
	if err != nil {
		st.Detail = err.Error()
		return st
	}
	st.Path = filepath.Join(root, spec.ID, "SKILL.md")
	if _, err := os.Stat(st.Path); err != nil {
		if os.IsNotExist(err) {
			st.Detail = "not installed in the AgentMux skill library"
		} else {
			st.Detail = err.Error()
		}
		return st
	}
	st.Installed = true
	st.Version = readSkillVersion(st.Path)
	st.InSync = true
	if spec.MatchCLIVersion {
		cliNormalized := normalizeVersion(cliVersion)
		skillNormalized := normalizeVersion(st.Version)
		if cliNormalized == "" || skillNormalized == "" {
			st.InSync = false
			st.Detail = "could not verify the linked skill version"
		} else if cliNormalized != skillNormalized {
			st.InSync = false
			st.Detail = fmt.Sprintf("skill version %s does not match CLI version %s", skillNormalized, cliNormalized)
		}
	}
	return st
}

func readSkillVersion(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	inFrontMatter := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "---" {
			if inFrontMatter {
				break
			}
			inFrontMatter = true
			continue
		}
		if inFrontMatter && strings.HasPrefix(line, "version:") {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "version:")), "\"'")
		}
	}
	return ""
}

func resolveLinkedSkillCommand(command []string) ([]string, error) {
	if len(command) == 0 {
		return nil, nil
	}
	root, err := agentMuxSkillsDir()
	if err != nil {
		return nil, err
	}
	resolved := make([]string, len(command))
	for i, arg := range command {
		resolved[i] = strings.ReplaceAll(arg, agentMuxSkillsDirToken, root)
	}
	return resolved, nil
}

func agentMuxSkillsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		if err == nil {
			err = fmt.Errorf("home directory is empty")
		}
		return "", fmt.Errorf("resolve AgentMux skill library: %w", err)
	}
	return filepath.Join(home, ".agentmux", "tools", "skills"), nil
}

func appendCommand(existing, next string) string {
	existing = strings.TrimSpace(existing)
	next = strings.TrimSpace(next)
	if existing == "" {
		return next
	}
	if next == "" {
		return existing
	}
	return existing + " && " + next
}

func appendLog(existing, next string) string {
	if next == "" {
		return existing
	}
	if existing != "" && !strings.HasSuffix(existing, "\n") {
		existing += "\n"
	}
	return existing + next
}

// LookupCLI returns the catalog spec for a CLI id.
func LookupCLI(id string) (CLISpec, bool) {
	return lookupCLI(id)
}

func lookupCLI(id string) (CLISpec, bool) {
	for _, spec := range cliCatalog {
		if spec.ID == id {
			return spec, true
		}
	}
	return CLISpec{}, false
}

func cliEnv(spec CLISpec) []string {
	env := os.Environ()
	if spec.ID == "bytedcli" {
		env = append(env, "PUPPETEER_SKIP_DOWNLOAD=true")
	}
	return env
}

func commandOutput(ctx context.Context, name string, args ...string) (string, error) {
	return commandOutputWithEnv(ctx, nil, name, args...)
}

func commandOutputWithEnv(ctx context.Context, env []string, name string, args ...string) (string, error) {
	runCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	executable, err := resolveCLIExecutable(name)
	if err != nil {
		return "", missingCLIExecutableError(name)
	}
	cmd := exec.CommandContext(runCtx, executable, args...)
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), err
	}
	return string(out), nil
}

var versionRE = regexp.MustCompile(`\d+(?:\.\d+){0,3}(?:[-+][0-9A-Za-z.-]+)?`)

func normalizeVersion(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if match := versionRE.FindString(raw); match != "" {
		return match
	}
	return ""
}

func versionGreater(candidate, current string) bool {
	candidateParts, ok := numericVersionParts(candidate)
	if !ok {
		return false
	}
	currentParts, ok := numericVersionParts(current)
	if !ok {
		return false
	}
	for i := range candidateParts {
		if candidateParts[i] > currentParts[i] {
			return true
		}
		if candidateParts[i] < currentParts[i] {
			return false
		}
	}
	return false
}

func numericVersionParts(version string) ([4]int, bool) {
	var parts [4]int
	version = strings.TrimSpace(version)
	if version == "" {
		return parts, false
	}
	base := strings.SplitN(version, "-", 2)[0]
	base = strings.SplitN(base, "+", 2)[0]
	rawParts := strings.Split(base, ".")
	for i, raw := range rawParts {
		if i >= len(parts) {
			break
		}
		if raw == "" {
			return parts, false
		}
		value, err := strconv.Atoi(raw)
		if err != nil {
			return parts, false
		}
		parts[i] = value
	}
	return parts, true
}
