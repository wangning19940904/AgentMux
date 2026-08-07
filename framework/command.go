package framework

import (
	"context"
	"os"
	"os/exec"
)

// frameworkCommandContext gives child processes a durable working directory.
// A running daemon can outlive the release directory it was started from when
// a deployment replaces that directory. Inheriting the now-unlinked cwd makes
// Node-based CLIs fail in process.cwd() and can pollute otherwise successful
// version output with warnings that look like version numbers.
func frameworkCommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = durableFrameworkCommandDir()
	return cmd
}

func durableFrameworkCommandDir() string {
	candidates := make([]string, 0, 2)
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, home)
	}
	candidates = append(candidates, os.TempDir())
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	return ""
}
