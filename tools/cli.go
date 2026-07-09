// Package tools implements installable local tool catalogs for AgentNexus.
package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// CLISpec describes one managed local CLI.
type CLISpec struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Bin            string   `json:"bin"`
	Package        string   `json:"package"`
	Registry       string   `json:"registry,omitempty"`
	InstallCommand []string `json:"install_command,omitempty"`
	UpdateCommand  []string `json:"update_command,omitempty"`
	Note           string   `json:"note,omitempty"`
}

// CLIStatus is the detected state for a local CLI.
type CLIStatus struct {
	Spec      CLISpec `json:"spec"`
	Installed bool    `json:"installed"`
	Path      string  `json:"path,omitempty"`
	Version   string  `json:"version,omitempty"`
	Detail    string  `json:"detail,omitempty"`
}

// CLIInstallResult reports the install/update outcome.
type CLIInstallResult struct {
	ID      string `json:"id"`
	Action  string `json:"action"`
	OK      bool   `json:"ok"`
	Command string `json:"command,omitempty"`
	Log     string `json:"log,omitempty"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
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

var cliCatalog = []CLISpec{
	{
		ID: "lark-cli", Name: "Lark CLI", Bin: "lark-cli", Package: "@larksuite/cli",
		InstallCommand: []string{"npm", "install", "-g", "@larksuite/cli@latest"},
		UpdateCommand:  []string{"lark-cli", "update"},
		Note:           "Feishu/Lark Open Platform CLI and official skills updater.",
	},
	{
		ID: "bytedcli", Name: "bytedcli", Bin: "bytedcli", Package: "@bytedance-dev/bytedcli",
		Registry:       "https://bnpm.byted.org/",
		InstallCommand: []string{"npm", "install", "-g", "@bytedance-dev/bytedcli@latest", "--registry=https://bnpm.byted.org/"},
		UpdateCommand:  []string{"npm", "install", "-g", "@bytedance-dev/bytedcli@latest", "--registry=https://bnpm.byted.org/"},
		Note:           "ByteDance internal developer CLI.",
	},
}

// CLICatalog returns the managed CLI catalog.
func CLICatalog() []CLISpec {
	out := make([]CLISpec, len(cliCatalog))
	copy(out, cliCatalog)
	return out
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
	path, err := exec.LookPath(spec.Bin)
	if err != nil {
		st.Detail = "not found on PATH"
		return st
	}
	st.Installed = true
	st.Path = path
	if version, err := commandOutput(ctx, spec.Bin, "--version"); err == nil {
		st.Version = strings.TrimSpace(version)
	} else {
		st.Detail = err.Error()
	}
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

// InstallCLI installs or updates a whitelisted CLI.
func InstallCLI(ctx context.Context, id, action string) CLIInstallResult {
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
	if action != "install" && action != "update" {
		res.Error = "action must be install or update"
		return res
	}

	statusBefore := DetectCLI(ctx, spec)
	cmdArgs := spec.InstallCommand
	if action == "update" {
		if !statusBefore.Installed {
			res.Error = fmt.Sprintf("CLI %q is not installed; install it first", id)
			return res
		}
		check := CheckCLIUpdate(ctx, id)
		if check.Error != "" {
			res.Error = check.Error
			return res
		}
		if !check.UpdateAvailable {
			res.OK = true
			res.Version = statusBefore.Version
			res.Log = fmt.Sprintf("%s is already up to date (current %s, latest %s)", spec.Name, check.CurrentVersion, check.LatestVersion)
			return res
		}
		if len(spec.UpdateCommand) > 0 {
			cmdArgs = spec.UpdateCommand
		}
	}
	if len(cmdArgs) == 0 {
		res.Error = fmt.Sprintf("CLI %q has no install command", id)
		return res
	}
	res.Command = strings.Join(cmdArgs, " ")

	runCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
	}
	cmd := exec.CommandContext(runCtx, cmdArgs[0], cmdArgs[1:]...)
	cmd.Env = cliEnv(spec)
	out, err := cmd.CombinedOutput()
	res.Log = string(out)
	if err != nil {
		res.Error = fmt.Sprintf("%s failed: %v", action, err)
		return res
	}
	status := DetectCLI(ctx, spec)
	res.OK = status.Installed
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
	return res
}

func latestCLIVersion(ctx context.Context, spec CLISpec) (string, error) {
	args := []string{"view", spec.Package, "version", "--silent"}
	if spec.Registry != "" {
		args = append(args, "--registry="+spec.Registry)
	}
	return commandOutputWithEnv(ctx, cliEnv(spec), "npm", args...)
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
	cmd := exec.CommandContext(runCtx, name, args...)
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
