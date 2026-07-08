package framework

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	sidecarfs "github.com/agentnexus/agentnexus/sidecar"
)

// DataDir returns AgentNexus's base data directory (~/.agentnexus), matching
// the store's default location.
func DataDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".agentnexus")
}

// SidecarDir returns the directory where the Node sidecar worker and its
// npm-installed framework packages live.
func SidecarDir() string {
	return filepath.Join(DataDir(), "sidecar")
}

// WorkerPath returns the absolute path to the sidecar worker entrypoint.
func WorkerPath() string {
	return filepath.Join(SidecarDir(), "worker.mjs")
}

// EnsureSidecar materializes the embedded sidecar sources into SidecarDir,
// writing any file that is missing or whose contents differ. Existing
// node_modules and package-lock.json are left untouched.
func EnsureSidecar() error {
	dir := SidecarDir()
	if err := os.MkdirAll(filepath.Join(dir, "adapters"), 0o755); err != nil {
		return fmt.Errorf("mkdir sidecar dir: %w", err)
	}
	return fs.WalkDir(sidecarfs.Files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := sidecarfs.Files.ReadFile(path)
		if err != nil {
			return err
		}
		dest := filepath.Join(dir, filepath.FromSlash(path))
		if existing, err := os.ReadFile(dest); err == nil && string(existing) == string(data) {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dest, data, 0o644)
	})
}
