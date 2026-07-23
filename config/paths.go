package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// EnvPath is the environment variable used to point AgentNexus at a config
// file when --config is not supplied.
const EnvPath = "ANX_CONFIG"

// NotFoundError reports all config paths that were checked.
type NotFoundError struct {
	Candidates []string
}

func (e *NotFoundError) Error() string {
	if len(e.Candidates) == 0 {
		return "config file not found"
	}
	return fmt.Sprintf("config file not found (searched: %s)", strings.Join(e.Candidates, ", "))
}

// IsNotFound reports whether err is a config lookup miss.
func IsNotFound(err error) bool {
	var target *NotFoundError
	return errors.As(err, &target)
}

// DefaultPath returns the per-user config path. On Linux this follows XDG:
// $XDG_CONFIG_HOME/agentnexus/config.toml, falling back to
// ~/.config/agentnexus/config.toml.
func DefaultPath() string {
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return filepath.Join(dir, "agentnexus", "config.toml")
	}
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return filepath.Join(dir, "agentnexus", "config.toml")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config", "agentnexus", "config.toml")
	}
	return filepath.Join(".config", "agentnexus", "config.toml")
}

// SystemPath returns the machine-wide config path used by Linux services.
func SystemPath() string {
	return "/etc/agentnexus/config.toml"
}

// CandidatePaths returns config paths in lookup order. An explicit path or
// ANX_CONFIG disables fallback search so mistakes fail loudly.
func CandidatePaths(explicit string) ([]string, error) {
	if p := strings.TrimSpace(explicit); p != "" {
		path, err := ExpandPath(p)
		if err != nil {
			return nil, err
		}
		return []string{path}, nil
	}
	if p := strings.TrimSpace(os.Getenv(EnvPath)); p != "" {
		path, err := ExpandPath(p)
		if err != nil {
			return nil, err
		}
		return []string{path}, nil
	}

	raw := []string{
		"config.toml",
		DefaultPath(),
	}
	if runtime.GOOS != "windows" {
		raw = append(raw, SystemPath())
	}

	seen := map[string]bool{}
	paths := make([]string, 0, len(raw))
	for _, item := range raw {
		path, err := ExpandPath(item)
		if err != nil {
			return nil, err
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	return paths, nil
}

// ResolvePath returns the first existing regular config file in lookup order.
func ResolvePath(explicit string) (string, []string, error) {
	candidates, err := CandidatePaths(explicit)
	if err != nil {
		return "", nil, err
	}
	for _, path := range candidates {
		info, err := os.Stat(path)
		if err == nil {
			if info.IsDir() {
				return "", candidates, fmt.Errorf("config path %s is a directory", path)
			}
			return path, candidates, nil
		}
		if !os.IsNotExist(err) {
			return "", candidates, err
		}
	}
	return "", candidates, &NotFoundError{Candidates: candidates}
}

// LoadResolved resolves a config path and loads it.
func LoadResolved(explicit string) (*Config, string, error) {
	path, _, err := ResolvePath(explicit)
	if err != nil {
		return nil, "", err
	}
	cfg, err := Load(path)
	if err != nil {
		return nil, path, err
	}
	return cfg, path, nil
}

// ExpandPath expands environment variables, a leading ~, and returns an
// absolute cleaned path.
func ExpandPath(raw string) (string, error) {
	path := strings.TrimSpace(os.ExpandEnv(raw))
	if path == "" {
		return "", fmt.Errorf("config path is empty")
	}
	if strings.HasPrefix(path, "~") {
		if path != "~" && !strings.HasPrefix(path, "~/") {
			return "", fmt.Errorf("only current-user home paths are supported")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}
