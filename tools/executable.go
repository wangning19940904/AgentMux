package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// resolveCLIExecutable recognizes Homebrew's standard prefixes in addition to
// PATH. macOS GUI apps and background services do not load interactive shell
// startup files, so Apple Silicon Homebrew's /opt/homebrew/bin is commonly
// absent even when brew and its installed CLIs work in a terminal.
func resolveCLIExecutable(bin string) (string, error) {
	if path, err := exec.LookPath(bin); err == nil {
		return path, nil
	}
	for _, dir := range homebrewExecutableDirs() {
		path, err := exec.LookPath(filepath.Join(dir, bin))
		if err == nil {
			return path, nil
		}
	}
	return "", exec.ErrNotFound
}

func homebrewExecutableDirs() []string {
	dirs := make([]string, 0, 5)
	if prefix := strings.TrimSpace(os.Getenv("HOMEBREW_PREFIX")); prefix != "" {
		dirs = append(dirs, filepath.Join(prefix, "bin"))
	}
	dirs = append(dirs,
		"/opt/homebrew/bin",
		"/usr/local/bin",
		"/home/linuxbrew/.linuxbrew/bin",
	)
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		dirs = append(dirs, filepath.Join(home, ".linuxbrew", "bin"), filepath.Join(home, ".local", "bin"))
	}

	seen := make(map[string]bool, len(dirs))
	unique := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		cleaned := filepath.Clean(dir)
		if seen[cleaned] {
			continue
		}
		seen[cleaned] = true
		unique = append(unique, cleaned)
	}
	return unique
}

func missingCLIExecutableError(bin string) error {
	if bin == "brew" {
		return fmt.Errorf("Homebrew (brew) was not found; install it from https://brew.sh and retry")
	}
	return fmt.Errorf("%s not found on PATH", bin)
}
