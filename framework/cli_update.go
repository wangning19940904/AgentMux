package framework

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// cliUpdateEnvironment preserves the selected CLI (including user wrappers)
// while making package managers available to its native updater. Finder and
// launchd omit Homebrew from PATH; Codex can detect a Homebrew installation but
// still fail to spawn `brew` with ENOENT. Keep these additions child-local so
// detecting or uninstalling a CLI does not discover a different installation.
func cliUpdateEnvironment(environ []string, goos string) []string {
	env := append([]string(nil), environ...)
	if goos != "darwin" {
		return env
	}
	pathIndex := -1
	var paths []string
	for i, entry := range env {
		if strings.HasPrefix(entry, "PATH=") {
			pathIndex = i
			paths = filepath.SplitList(strings.TrimPrefix(entry, "PATH="))
			break
		}
	}
	for _, dir := range []string{"/opt/homebrew/bin", "/usr/local/bin"} {
		found := false
		for _, existing := range paths {
			if filepath.Clean(existing) == dir {
				found = true
				break
			}
		}
		if !found {
			paths = append(paths, dir)
		}
	}
	entry := "PATH=" + strings.Join(paths, string(os.PathListSeparator))
	if pathIndex < 0 {
		return append(env, entry)
	}
	env[pathIndex] = entry
	return env
}

// cliInstallMethod is how a CLI binary was installed, judged from where its
// executable really lives.
type cliInstallMethod int

const (
	cliInstallUnknown cliInstallMethod = iota
	cliInstallHomebrewCask
	cliInstallHomebrewFormula
	cliInstallNPMGlobal
)

// cliInstallation describes a detected installation. Prefix is the Homebrew
// prefix or the npm global prefix; Token is the Homebrew cask or formula.
type cliInstallation struct {
	Method cliInstallMethod
	Prefix string
	Token  string
}

var homebrewTokenRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@._+-]*$`)

// detectCLIInstallation classifies the fully resolved executable path of a
// CLI. Native self-updaters (`codex update`, `claude update`, ...) make the
// same decision themselves, but they judge the path they were started from
// and give up on layouts they do not recognise ("Could not detect the Codex
// installation method"). AgentMux resolves symlinks first, so an alias in
// ~/.local/bin or a version-manager shim pointing into Homebrew or an npm
// global prefix is still upgraded by the package manager that owns it.
func detectCLIInstallation(resolved, npmPackage, goos string) cliInstallation {
	path := filepath.ToSlash(filepath.Clean(resolved))
	if strings.Contains(path, "/node_modules/") {
		// A package inside a Node installation, even one Homebrew installed
		// (Cellar/node/...), belongs to that Node's package manager, never to
		// the Homebrew formula that happens to contain it.
		return detectNPMGlobalInstallation(path, npmPackage, goos)
	}
	for _, layout := range []struct {
		marker string
		method cliInstallMethod
	}{
		{"/Caskroom/", cliInstallHomebrewCask},
		{"/Cellar/", cliInstallHomebrewFormula},
	} {
		index := strings.Index(path, layout.marker)
		if index < 0 {
			continue
		}
		token := strings.SplitN(path[index+len(layout.marker):], "/", 2)[0]
		if !homebrewTokenRE.MatchString(token) {
			continue
		}
		prefix := path[:index]
		if prefix == "" {
			prefix = "/"
		}
		return cliInstallation{Method: layout.method, Prefix: filepath.FromSlash(prefix), Token: token}
	}
	return cliInstallation{}
}

func detectNPMGlobalInstallation(path, npmPackage, goos string) cliInstallation {
	if npmPackage == "" {
		return cliInstallation{}
	}
	marker := "/node_modules/" + npmPackage + "/"
	index := strings.Index(path, marker)
	if index < 0 {
		return cliInstallation{}
	}
	parent := path[:index]
	// pnpm, Bun, Yarn and Volta keep their own global stores; `npm -g` into
	// them would create a second, unmanaged copy. Their native updaters (or the
	// CLI's own launcher) know how to handle those. A nested node_modules is a
	// project-local dependency, not a global installation.
	for _, foreign := range []string{"/.pnpm/", "/.bun/", "/.yarn/", "/yarn/global/", "/.volta/", "/node_modules/"} {
		if strings.Contains(parent+"/", foreign) {
			return cliInstallation{}
		}
	}
	prefix := parent
	if goos != "windows" {
		// npm's POSIX global layout is <prefix>/lib/node_modules.
		if !strings.HasSuffix(parent, "/lib") {
			return cliInstallation{}
		}
		prefix = strings.TrimSuffix(parent, "/lib")
	}
	if prefix == "" {
		prefix = "/"
	}
	return cliInstallation{Method: cliInstallNPMGlobal, Prefix: filepath.FromSlash(prefix)}
}

// cliUpdateCommandFor chooses the update command for a detected
// installation. ok is false when the catalog command should run unchanged.
// brewLookup resolves a Homebrew executable path.
func cliUpdateCommandFor(spec Spec, inst cliInstallation, brewLookup func(string) (string, error)) ([]string, bool, error) {
	switch inst.Method {
	case cliInstallHomebrewCask, cliInstallHomebrewFormula:
		preferred := filepath.Join(inst.Prefix, "bin", "brew")
		brew, err := brewLookup(preferred)
		if err != nil {
			if brew, err = brewLookup("brew"); err != nil {
				return nil, false, fmt.Errorf(
					"%s is installed by Homebrew under %s, but brew was not found at %s or on PATH",
					spec.Display, inst.Prefix, preferred,
				)
			}
		}
		if inst.Method == cliInstallHomebrewCask {
			return []string{brew, "upgrade", "--cask", inst.Token}, true, nil
		}
		return []string{brew, "upgrade", inst.Token}, true, nil
	case cliInstallNPMGlobal:
		// The prefix pins the install to the copy on PATH even when the npm
		// found first belongs to another Node (nvm, Homebrew node, ...).
		return []string{"npm", "install", "-g", spec.NPMPackage + "@latest", "--prefix=" + inst.Prefix}, true, nil
	}
	return nil, false, nil
}

// resolvedCLIUpdateCommand returns the catalog-owned update command, adjusted
// for the way the CLI was actually installed. Catalog commands that already
// use npm are kept. For CLIs with a native self-updater, a Homebrew or npm
// global installation is upgraded by that package manager directly: native
// updaters judge the path they were launched from, so a symlink or shim in
// front of the real binary makes them report the installation method as
// unknown and exit 1. Anything else keeps the native updater.
func resolvedCLIUpdateCommand(spec Spec) ([]string, error) {
	if len(spec.UpdateCommand) == 0 {
		return nil, fmt.Errorf("framework %q has no update command", spec.Kind)
	}
	command := append([]string(nil), spec.UpdateCommand...)
	if spec.Bin == "" || isNPMInstall(command) {
		return command, nil
	}
	executable, err := resolveCLIExecutable(spec.Bin)
	if err != nil {
		// The update reports the missing executable when it runs.
		return command, nil
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return command, nil
	}
	inst := detectCLIInstallation(resolved, spec.NPMPackage, runtime.GOOS)
	adjusted, ok, err := cliUpdateCommandFor(spec, inst, exec.LookPath)
	if err != nil {
		return nil, err
	}
	if !ok {
		return command, nil
	}
	return adjusted, nil
}

// nativeUpdaterCannotDetectInstall reports a native updater giving up because
// it could not tell how the CLI was installed.
func nativeUpdaterCannotDetectInstall(log string) bool {
	lower := strings.ToLower(log)
	return strings.Contains(lower, "installation method") &&
		(strings.Contains(lower, "could not detect") || strings.Contains(lower, "unknown") ||
			strings.Contains(lower, "could not determine") || strings.Contains(lower, "unable to"))
}

// npmFallbackUpdatePlan is the npm command used when a native updater cannot
// identify its installation. It is only offered for built-in commands of
// CLIs published to npm.
func npmFallbackUpdatePlan(spec Spec, plan CommandPlan, log string, settings InstallSettings) (CommandPlan, bool) {
	if plan.Custom || spec.NPMPackage == "" || isNPMInstall(plan.Argv) || !nativeUpdaterCannotDetectInstall(log) {
		return CommandPlan{}, false
	}
	override := settings.override(spec.Kind)
	argv, registry := applyRegistry(
		[]string{"npm", "install", "-g", spec.NPMPackage + "@latest"},
		override.Registry, settings.DefaultRegistry,
	)
	return CommandPlan{Argv: argv, Command: strings.Join(argv, " "), Registry: registry}, true
}
