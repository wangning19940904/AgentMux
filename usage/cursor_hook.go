package usage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const cursorHookTimeoutSeconds = 2

type cursorHookStatus struct {
	Status     string   `json:"status"`
	HooksPath  string   `json:"hooks_path"`
	HelperPath string   `json:"helper_path"`
	Actions    []string `json:"actions,omitempty"`
}

func cursorHooksPath(home string) string {
	return filepath.Join(home, ".cursor", "hooks.json")
}

func cursorHookHelperPath(home string) string {
	return filepath.Join(home, ".agentmux", "bin", "agentmux-hook")
}

func cursorHookCommand(home string) string {
	return shellQuote(cursorHookHelperPath(home)) + " --source cursor"
}

func inspectCursorHook(home string) cursorHookStatus {
	status := cursorHookStatus{Status: "not_installed", HooksPath: cursorHooksPath(home), HelperPath: cursorHookHelperPath(home)}
	raw, err := os.ReadFile(status.HooksPath)
	if err != nil {
		if !os.IsNotExist(err) {
			status.Status = "unavailable"
		}
		status.Actions = []string{"install AgentMux Cursor stop hook"}
		return status
	}
	root, err := parseCursorHooks(raw)
	if err != nil {
		status.Status = "conflict"
		status.Actions = []string{"repair invalid Cursor hooks.json manually"}
		return status
	}
	if cursorHookInstalled(root, home) {
		if info, helperErr := os.Stat(status.HelperPath); helperErr == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			status.Status = "healthy"
			return status
		}
		status.Status = "drift"
		status.Actions = []string{"repair AgentMux hook helper"}
		return status
	}
	status.Actions = []string{"append AgentMux Cursor stop hook while preserving existing hooks"}
	return status
}

func installCursorHook(home string) (cursorHookStatus, error) {
	helper, err := ensureCursorHookHelper(home)
	if err != nil {
		return inspectCursorHook(home), err
	}
	path := cursorHooksPath(home)
	raw, mode, expectedHash, err := readCursorHooksFile(path)
	if err != nil {
		return inspectCursorHook(home), err
	}
	root, err := parseCursorHooks(raw)
	if err != nil {
		return inspectCursorHook(home), fmt.Errorf("parse Cursor hooks.json: %w", err)
	}
	if !cursorHookInstalled(root, home) {
		hooks := cursorHooksMap(root)
		stop, _ := hooks["stop"].([]any)
		stop = append(stop, map[string]any{
			"command": cursorHookCommand(home), "timeout": cursorHookTimeoutSeconds, "failClosed": false,
		})
		hooks["stop"] = stop
		root["hooks"] = hooks
	}
	root["version"] = 1
	encoded, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return inspectCursorHook(home), err
	}
	encoded = append(encoded, '\n')
	if err := writeCursorCAS(path, encoded, mode, expectedHash); err != nil {
		return inspectCursorHook(home), err
	}
	status := inspectCursorHook(home)
	status.HelperPath = helper
	return status, nil
}

func removeCursorHook(home string) (cursorHookStatus, error) {
	path := cursorHooksPath(home)
	raw, mode, expectedHash, err := readCursorHooksFile(path)
	if os.IsNotExist(err) {
		return inspectCursorHook(home), nil
	}
	if err != nil {
		return inspectCursorHook(home), err
	}
	root, err := parseCursorHooks(raw)
	if err != nil {
		return inspectCursorHook(home), fmt.Errorf("parse Cursor hooks.json: %w", err)
	}
	hooks := cursorHooksMap(root)
	stop, _ := hooks["stop"].([]any)
	filtered := stop[:0]
	for _, item := range stop {
		entry, _ := item.(map[string]any)
		command, _ := entry["command"].(string)
		if isAgentMuxCursorHook(command, home) {
			continue
		}
		filtered = append(filtered, item)
	}
	if len(filtered) == 0 {
		delete(hooks, "stop")
	} else {
		hooks["stop"] = filtered
	}
	root["hooks"] = hooks
	encoded, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return inspectCursorHook(home), err
	}
	encoded = append(encoded, '\n')
	if err := writeCursorCAS(path, encoded, mode, expectedHash); err != nil {
		return inspectCursorHook(home), err
	}
	return inspectCursorHook(home), nil
}

func parseCursorHooks(raw []byte) (map[string]any, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return map[string]any{"version": 1, "hooks": map[string]any{}}, nil
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	if root == nil {
		root = map[string]any{}
	}
	return root, nil
}

func cursorHooksMap(root map[string]any) map[string]any {
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	return hooks
}

func cursorHookInstalled(root map[string]any, home string) bool {
	hooks := cursorHooksMap(root)
	stop, _ := hooks["stop"].([]any)
	for _, item := range stop {
		entry, _ := item.(map[string]any)
		command, _ := entry["command"].(string)
		if isAgentMuxCursorHook(command, home) {
			return true
		}
	}
	return false
}

func isAgentMuxCursorHook(command, home string) bool {
	command = strings.TrimSpace(command)
	return strings.Contains(command, cursorHookHelperPath(home)) && strings.Contains(command, "--source cursor")
}

func readCursorHooksFile(path string) ([]byte, os.FileMode, string, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []byte(`{"version":1,"hooks":{}}`), 0o600, "", nil
	}
	if err != nil {
		return nil, 0, "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, 0, "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, 0, "", fmt.Errorf("refusing non-regular Cursor hooks file %s", path)
	}
	return raw, info.Mode().Perm(), hashCursorBytes(raw), nil
}

func ensureCursorHookHelper(home string) (string, error) {
	target := cursorHookHelperPath(home)
	if info, err := os.Stat(target); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
		return target, nil
	}
	var candidates []string
	if configured := strings.TrimSpace(os.Getenv("AGENTMUX_HOOK_HELPER")); configured != "" {
		candidates = append(candidates, configured)
	}
	if executable, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(executable), "agentmux-hook"))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "agentmux-hook"))
	}
	if found, err := exec.LookPath("agentmux-hook"); err == nil {
		candidates = append(candidates, found)
	}
	var source string
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			source = candidate
			break
		}
	}
	if source == "" {
		return "", errors.New("agentmux-hook helper was not found next to the AgentMux executable")
	}
	raw, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "", err
	}
	if err := writeCursorCAS(target, raw, 0o700, hashCursorFileOrEmpty(target)); err != nil {
		return "", err
	}
	return target, nil
}

func writeCursorCAS(path string, data []byte, mode os.FileMode, expectedHash string) error {
	if current := hashCursorFileOrEmpty(path); current != expectedHash {
		return fmt.Errorf("Cursor hook file changed concurrently: %s", path)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".agentmux-cursor-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if current := hashCursorFileOrEmpty(path); current != expectedHash {
		return fmt.Errorf("Cursor hook file changed concurrently during commit: %s", path)
	}
	return os.Rename(tmpPath, path)
}

func hashCursorFileOrEmpty(path string) string {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		return "error"
	}
	return hashCursorBytes(raw)
}

func hashCursorBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
