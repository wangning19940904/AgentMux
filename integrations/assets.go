// Package integrations embeds the two native observer marketplaces so a
// released single-binary AgentMux can materialize them into its private
// directory before invoking the host plugin CLIs.
package integrations

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed marketplaces
var marketplaceAssets embed.FS

func MaterializeMarketplaces(home, version string) (string, error) {
	if strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("home directory is required")
	}
	if version == "" {
		version = "dev"
	}
	root := filepath.Join(home, ".agentmux", "assets", "agentmux-observer", version)
	target := filepath.Join(root, "marketplaces")
	if err := fs.WalkDir(marketplaceAssets, "marketplaces", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel("marketplaces", path)
		if err != nil || relative == "." {
			return err
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o700)
		}
		data, err := marketplaceAssets.ReadFile(path)
		if err != nil {
			return err
		}
		if current, readErr := os.ReadFile(destination); readErr == nil && string(current) == string(data) {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return err
		}
		temporary, err := os.CreateTemp(filepath.Dir(destination), ".asset-*")
		if err != nil {
			return err
		}
		temporaryPath := temporary.Name()
		defer os.Remove(temporaryPath)
		if err := temporary.Chmod(0o600); err == nil {
			_, err = temporary.Write(data)
		}
		if closeErr := temporary.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		return os.Rename(temporaryPath, destination)
	}); err != nil {
		return "", err
	}
	return target, nil
}
