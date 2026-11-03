package framework

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// resolvedCLIUpdateCommand returns the catalog-owned update command, adjusted
// for installation methods whose native self-updater cannot be reached through
// an alias. In particular, Codex identifies its installation from the invoked
// executable path, so a user-level symlink in front of a Homebrew Cask makes
// `codex update` report the installation method as unknown.
func resolvedCLIUpdateCommand(spec Spec) ([]string, error) {
	if len(spec.UpdateCommand) == 0 {
		return nil, fmt.Errorf("framework %q has no update command", spec.Kind)
	}
	command := append([]string(nil), spec.UpdateCommand...)
	if spec.Kind != "codex" || spec.Bin == "" {
		return command, nil
	}

	executable, err := exec.LookPath(spec.Bin)
	if err != nil {
		return nil, fmt.Errorf("find %s on PATH: %w", spec.Bin, err)
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		// A non-Homebrew installation may still support Codex's native updater.
		// Let that updater produce the installation-specific diagnostic rather
		// than rejecting an otherwise valid executable alias here.
		return command, nil
	}
	prefix, ok := homebrewCaskPrefix(resolved, "codex")
	if !ok {
		return command, nil
	}

	brew := filepath.Join(prefix, "bin", "brew")
	if path, lookErr := exec.LookPath(brew); lookErr == nil {
		brew = path
	} else if path, fallbackErr := exec.LookPath("brew"); fallbackErr == nil {
		brew = path
	} else {
		return nil, fmt.Errorf(
			"Codex is installed by Homebrew at %s, but brew was not found at %s or on PATH",
			resolved, brew,
		)
	}
	return []string{brew, "upgrade", "--cask", "codex"}, nil
}

func homebrewCaskPrefix(executable, token string) (string, bool) {
	cleaned := filepath.Clean(executable)
	separator := string(filepath.Separator)
	marker := separator + "Caskroom" + separator + token + separator
	index := strings.Index(cleaned, marker)
	if index < 0 {
		return "", false
	}
	prefix := cleaned[:index]
	if prefix == "" {
		prefix = separator
	}
	return prefix, true
}
