// Package tools implements installable local tool catalogs for AgentNexus.
package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
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

	installed := DetectCLI(ctx, spec).Installed
	cmdArgs := spec.InstallCommand
	if action == "update" && installed && len(spec.UpdateCommand) > 0 {
		cmdArgs = spec.UpdateCommand
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
	runCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(runCtx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), err
	}
	return string(out), nil
}
