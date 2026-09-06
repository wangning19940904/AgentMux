package parser

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// The transcript format identifies a product, not necessarily its client.
// Keep Source stable for deduplication and use RuntimeID for the client.
func ClaudeRuntime(entrypoint string) string {
	switch strings.ToLower(strings.TrimSpace(entrypoint)) {
	case "claude-desktop":
		return "claude-desktop"
	case "cli":
		return "claudecode"
	default:
		return "claude-unknown"
	}
}

func CodexRuntime(originator string, source json.RawMessage) string {
	switch strings.ToLower(strings.TrimSpace(originator)) {
	case "codex desktop", "codex-desktop":
		// Desktop also emits source=vscode, exec, and subagent objects.
		return "codex-app"
	case "codex-tui", "codex_cli_rs", "codex_exec":
		return "codex"
	case "codex_vscode":
		return "codex-vscode"
	case "":
		var value string
		_ = json.Unmarshal(source, &value)
		switch value {
		case "cli":
			return "codex"
		}
	}
	return "codex-unknown"
}

// SessionRuntimes repairs legacy rows without replaying token events. Codex
// needs only its session header, even for archived, multi-gigabyte rollouts.
// A Claude session with multiple clients cannot safely be classified as a
// whole; its historical rows remain unknown until individually re-collected.
func SessionRuntimes(ctx context.Context, source, root string) (map[string]string, error) {
	runtimes := map[string]string{}
	var dirs []string
	switch source {
	case "claude":
		dirs = []string{filepath.Join((&claudeCollector{root: root}).base(), "projects")}
	case "codex":
		base := (&codexCollector{root: root}).base()
		dirs = []string{filepath.Join(base, "sessions"), filepath.Join(base, "archived_sessions")}
	default:
		return runtimes, nil
	}
	add := func(session, runtime string) {
		if session == "" {
			return
		}
		if previous, ok := runtimes[session]; ok && previous != runtime {
			runtime = source + "-unknown"
		}
		runtimes[session] = runtime
	}
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".jsonl") ||
				(source == "codex" && !strings.HasPrefix(entry.Name(), "rollout-")) {
				return nil
			}
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()
			sc := bufio.NewScanner(file)
			sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
			for sc.Scan() {
				if err := ctx.Err(); err != nil {
					return err
				}
				if source == "codex" {
					var line codexLine
					if json.Unmarshal(sc.Bytes(), &line) == nil && line.Type == "session_meta" {
						runtime := CodexRuntime(line.Payload.Originator, line.Payload.Source)
						add(line.Payload.ID, runtime)
						add(strings.TrimSuffix(entry.Name(), ".jsonl"), runtime)
					}
					// A missing header is unknown, never inferred from the path.
					break
				}
				var line struct {
					SessionID  string `json:"sessionId"`
					Entrypoint string `json:"entrypoint"`
				}
				if json.Unmarshal(sc.Bytes(), &line) == nil && line.Entrypoint != "" {
					add(line.SessionID, ClaudeRuntime(line.Entrypoint))
				}
			}
			return sc.Err()
		})
		if err != nil {
			return nil, err
		}
	}
	return runtimes, nil
}
