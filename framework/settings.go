package framework

import (
	"errors"
	"fmt"
	"net/url"
	"runtime"
	"sort"
	"strings"
	"unicode"
)

// InstallSettings are a machine's administrator-chosen overrides for how
// catalogued CLIs are installed and updated. They are stored per machine and
// only change the commands AgentMux runs on that machine.
type InstallSettings struct {
	// DefaultRegistry is applied to built-in npm commands that do not already
	// pin a registry in the catalog.
	DefaultRegistry string `json:"default_registry,omitempty"`
	// Frameworks holds per-framework overrides keyed by catalog kind.
	Frameworks map[string]FrameworkOverride `json:"frameworks,omitempty"`
}

// FrameworkOverride customises one framework. A custom command replaces the
// built-in command entirely (and is not given registry flags); Registry
// replaces any registry the catalog pins for built-in npm commands and is
// also used to read the latest version.
type FrameworkOverride struct {
	Registry       string `json:"registry,omitempty"`
	InstallCommand string `json:"install_command,omitempty"`
	UpdateCommand  string `json:"update_command,omitempty"`
}

const maxCustomCommandLength = 4096

// ErrCustomCommandUnconfirmed is returned when a custom command would run
// without the caller echoing back the exact command it showed for approval.
var ErrCustomCommandUnconfirmed = errors.New("custom command must be confirmed before it runs")

// NormalizeInstallSettings trims and validates settings submitted by an
// administrator. Empty overrides are dropped so storage only keeps real
// customisations.
func NormalizeInstallSettings(in InstallSettings) (InstallSettings, error) {
	out := InstallSettings{}
	registry, err := normalizeRegistry(in.DefaultRegistry)
	if err != nil {
		return out, fmt.Errorf("default registry: %w", err)
	}
	out.DefaultRegistry = registry
	for kind, override := range in.Frameworks {
		kind = strings.TrimSpace(kind)
		spec, ok := Lookup(kind)
		if !ok || spec.KindType != KindCLI {
			return out, fmt.Errorf("unknown CLI framework %q", kind)
		}
		normalized := FrameworkOverride{}
		if normalized.Registry, err = normalizeRegistry(override.Registry); err != nil {
			return out, fmt.Errorf("%s registry: %w", spec.Display, err)
		}
		if normalized.InstallCommand, err = normalizeCustomCommand(override.InstallCommand); err != nil {
			return out, fmt.Errorf("%s install command: %w", spec.Display, err)
		}
		if normalized.UpdateCommand, err = normalizeCustomCommand(override.UpdateCommand); err != nil {
			return out, fmt.Errorf("%s update command: %w", spec.Display, err)
		}
		if normalized == (FrameworkOverride{}) {
			continue
		}
		if out.Frameworks == nil {
			out.Frameworks = map[string]FrameworkOverride{}
		}
		out.Frameworks[kind] = normalized
	}
	return out, nil
}

func normalizeRegistry(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || strings.ContainsAny(value, " \t\r\n") {
		return "", fmt.Errorf("%q is not an http(s) registry URL", value)
	}
	if !strings.HasSuffix(value, "/") {
		value += "/"
	}
	return value, nil
}

func normalizeCustomCommand(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if len(value) > maxCustomCommandLength {
		return "", fmt.Errorf("is longer than %d characters", maxCustomCommandLength)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", errors.New("must be a single line without control characters")
		}
	}
	return value, nil
}

func (s InstallSettings) override(kind string) FrameworkOverride {
	return s.Frameworks[kind]
}

// CommandPlan is the command AgentMux will run for one framework action.
type CommandPlan struct {
	Argv []string `json:"-"`
	// Command is the text shown to the user and, for custom commands, the
	// exact text that must be confirmed.
	Command string `json:"command"`
	Custom  bool   `json:"custom,omitempty"`
	// Registry is the registry AgentMux added to a built-in npm command.
	Registry string `json:"registry,omitempty"`
}

// PlanCommand resolves the install or update command for a CLI framework
// after applying the machine's install settings.
func PlanCommand(spec Spec, action string, settings InstallSettings) (CommandPlan, error) {
	override := settings.override(spec.Kind)
	custom := ""
	var builtIn []string
	switch action {
	case "install":
		custom = override.InstallCommand
		builtIn = append([]string(nil), spec.InstallCommand...)
		if len(builtIn) == 0 && spec.NPMPackage != "" {
			builtIn = []string{"npm", "install", "-g", spec.NPMPackage + "@latest"}
		}
		if len(builtIn) == 0 && custom == "" {
			return CommandPlan{}, fmt.Errorf("framework %q has no install command", spec.Kind)
		}
	case "update":
		custom = override.UpdateCommand
		if custom == "" {
			command, err := resolvedCLIUpdateCommand(spec)
			if err != nil {
				return CommandPlan{}, err
			}
			builtIn = command
		}
	default:
		return CommandPlan{}, fmt.Errorf("action %q has no customisable command", action)
	}
	if custom != "" {
		return CommandPlan{Argv: shellCommand(custom), Command: custom, Custom: true}, nil
	}
	argv, registry := applyRegistry(builtIn, override.Registry, settings.DefaultRegistry)
	return CommandPlan{Argv: argv, Command: strings.Join(argv, " "), Registry: registry}, nil
}

// RegistryApplies reports whether a registry setting can change any built-in
// command or latest-version lookup for the framework.
func RegistryApplies(spec Spec) bool {
	if spec.NPMPackage != "" {
		return true
	}
	return isNPMInstall(spec.InstallCommand) || isNPMInstall(spec.UpdateCommand)
}

func isNPMInstall(argv []string) bool {
	if len(argv) < 2 || argv[0] != "npm" {
		return false
	}
	return argv[1] == "install" || argv[1] == "i" || argv[1] == "update"
}

// applyRegistry adds registry flags to a built-in npm command. A framework
// registry replaces whatever registry flags the catalog pins; the machine
// default only fills in when the catalog pins none.
func applyRegistry(argv []string, frameworkRegistry, defaultRegistry string) ([]string, string) {
	if !isNPMInstall(argv) {
		return argv, ""
	}
	if frameworkRegistry != "" {
		out := make([]string, 0, len(argv)+1)
		for _, arg := range argv {
			if isRegistryFlag(arg) {
				continue
			}
			out = append(out, arg)
		}
		return append(out, "--registry="+frameworkRegistry), frameworkRegistry
	}
	if defaultRegistry != "" {
		for _, arg := range argv {
			if strings.HasPrefix(arg, "--registry=") {
				return argv, ""
			}
		}
		return append(append([]string(nil), argv...), "--registry="+defaultRegistry), defaultRegistry
	}
	return argv, ""
}

func isRegistryFlag(arg string) bool {
	if strings.HasPrefix(arg, "--registry=") || strings.HasPrefix(arg, "--replace-registry-host=") {
		return true
	}
	// Scoped registry pins look like --@scope:registry=https://...
	return strings.HasPrefix(arg, "--@") && strings.Contains(arg, ":registry=")
}

// latestVersionRegistry is the registry `npm view` should read for a
// framework, or "" for npm's own configuration.
func latestVersionRegistry(spec Spec, settings InstallSettings) string {
	if registry := settings.override(spec.Kind).Registry; registry != "" {
		return registry
	}
	return settings.DefaultRegistry
}

func shellCommand(command string) []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd", "/C", command}
	}
	return []string{"/bin/sh", "-c", command}
}

// authorizeCommand enforces the custom-command rules: custom commands run
// only for requests made on this machine, and only after the caller confirmed
// the exact command text.
func authorizeCommand(spec Spec, action string, plan CommandPlan, options InstallOptions) error {
	if !plan.Custom {
		return nil
	}
	if options.Forwarded {
		return fmt.Errorf("%s uses a custom %s command configured on this machine; run it from this machine's own Console", spec.Display, action)
	}
	if options.ConfirmedCommand != plan.Command {
		return fmt.Errorf("%s: %w", spec.Display, ErrCustomCommandUnconfirmed)
	}
	return nil
}

// InstallSettingsDefaults describes one framework's built-in commands so a
// settings editor can show what an override replaces.
type InstallSettingsDefaults struct {
	Kind            string `json:"kind"`
	Display         string `json:"display"`
	InstallCommand  string `json:"install_command,omitempty"`
	UpdateCommand   string `json:"update_command,omitempty"`
	RegistryApplies bool   `json:"registry_applies"`
}

// CustomizableFrameworks lists the visible CLI frameworks whose install or
// update can be customised, with their built-in commands.
func CustomizableFrameworks() []InstallSettingsDefaults {
	out := []InstallSettingsDefaults{}
	for _, spec := range Catalog() {
		if spec.KindType != KindCLI || !spec.Supported || (!spec.InstallSupported && !spec.UpdateSupported) {
			continue
		}
		entry := InstallSettingsDefaults{Kind: spec.Kind, Display: spec.Display, RegistryApplies: RegistryApplies(spec)}
		if spec.InstallSupported {
			if plan, err := PlanCommand(spec, "install", InstallSettings{}); err == nil {
				entry.InstallCommand = plan.Command
			}
		}
		if spec.UpdateSupported && len(spec.UpdateCommand) > 0 {
			entry.UpdateCommand = strings.Join(spec.UpdateCommand, " ")
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Display < out[j].Display })
	return out
}
