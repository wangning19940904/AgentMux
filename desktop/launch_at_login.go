//go:build desktop

package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"log/slog"
)

const (
	launchAtLoginLabel          = "com.agentmux.desktop"
	launchAtLoginPreferenceFile = "launch-at-login"
)

// LaunchAtLoginStatus is returned to the Wails frontend.
type LaunchAtLoginStatus struct {
	Supported bool `json:"supported"`
	Enabled   bool `json:"enabled"`
}

type launchAtLoginManager struct {
	supported      bool
	executablePath string
	agentPath      string
	preferencePath string
}

func newLaunchAtLoginManager() (*launchAtLoginManager, error) {
	manager := &launchAtLoginManager{}
	if runtime.GOOS != "darwin" {
		return manager, nil
	}

	executablePath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve AgentMux executable: %w", err)
	}
	if appBundlePath(executablePath) == "" {
		// Launch-at-login is only offered by the packaged desktop app. This
		// keeps local go test/dev binaries out of the user's login items.
		return manager, nil
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user config directory: %w", err)
	}

	manager.supported = true
	manager.executablePath = executablePath
	manager.agentPath = filepath.Join(homeDir, "Library", "LaunchAgents", launchAtLoginLabel+".plist")
	manager.preferencePath = filepath.Join(configDir, "agentmux", launchAtLoginPreferenceFile)
	return manager, nil
}

// ensureLaunchAtLoginDefault applies the persisted choice. On first launch,
// the choice defaults to enabled and is saved before the UI is shown.
func (a *App) ensureLaunchAtLoginDefault(log *slog.Logger) {
	manager, err := newLaunchAtLoginManager()
	if err != nil {
		log.Warn("prepare launch at login", "err", err)
		return
	}
	if !manager.supported {
		return
	}
	enabled, exists, err := manager.readPreference()
	if err != nil {
		log.Warn("read launch at login preference", "err", err)
		return
	}
	if !exists {
		enabled = true
	}
	if err := manager.apply(enabled, !exists); err != nil {
		log.Warn("apply launch at login preference", "enabled", enabled, "err", err)
	}
}

// GetLaunchAtLogin returns whether the packaged macOS app will open at login.
func (a *App) GetLaunchAtLogin() (LaunchAtLoginStatus, error) {
	manager, err := newLaunchAtLoginManager()
	if err != nil {
		return LaunchAtLoginStatus{}, err
	}
	return manager.status()
}

// SetLaunchAtLogin updates the macOS LaunchAgent and persists the choice.
func (a *App) SetLaunchAtLogin(enabled bool) (LaunchAtLoginStatus, error) {
	manager, err := newLaunchAtLoginManager()
	if err != nil {
		return LaunchAtLoginStatus{}, err
	}
	if !manager.supported {
		return manager.status()
	}
	if err := manager.apply(enabled, true); err != nil {
		return manager.status()
	}
	return manager.status()
}

func (m *launchAtLoginManager) status() (LaunchAtLoginStatus, error) {
	if !m.supported {
		return LaunchAtLoginStatus{Supported: false, Enabled: false}, nil
	}
	_, err := os.Stat(m.agentPath)
	switch {
	case err == nil:
		return LaunchAtLoginStatus{Supported: true, Enabled: true}, nil
	case errors.Is(err, os.ErrNotExist):
		return LaunchAtLoginStatus{Supported: true, Enabled: false}, nil
	default:
		return LaunchAtLoginStatus{Supported: true, Enabled: false}, err
	}
}

func (m *launchAtLoginManager) apply(enabled, persist bool) error {
	if !m.supported {
		return nil
	}
	if enabled {
		if err := writeAtomicFile(m.agentPath, launchAgentPlist(m.executablePath), 0o644); err != nil {
			return fmt.Errorf("enable launch at login: %w", err)
		}
	} else if err := os.Remove(m.agentPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("disable launch at login: %w", err)
	}
	if persist {
		if err := writeAtomicFile(m.preferencePath, []byte(fmt.Sprintf("%t\n", enabled)), 0o600); err != nil {
			return fmt.Errorf("save launch at login preference: %w", err)
		}
	}
	return nil
}

func (m *launchAtLoginManager) readPreference() (enabled, exists bool, err error) {
	raw, err := os.ReadFile(m.preferencePath)
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	switch strings.TrimSpace(string(raw)) {
	case "true":
		return true, true, nil
	case "false":
		return false, true, nil
	default:
		return false, false, fmt.Errorf("invalid value in %s", m.preferencePath)
	}
}

func launchAgentPlist(executablePath string) []byte {
	var escapedPath bytes.Buffer
	_ = xml.EscapeText(&escapedPath, []byte(executablePath))
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
      <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>ProcessType</key>
    <string>Interactive</string>
  </dict>
</plist>
`, launchAtLoginLabel, escapedPath.String()))
}

func writeAtomicFile(path string, data []byte, mode os.FileMode) error {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(parent, ".agentmux-*")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}
