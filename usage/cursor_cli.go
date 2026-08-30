package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

const cursorCLIFileLimit = 1 << 20

type cursorCLIMeta struct {
	CreatedAtMS     int64  `json:"createdAtMs"`
	UpdatedAtMS     int64  `json:"updatedAtMs"`
	HasConversation bool   `json:"hasConversation"`
	CWD             string `json:"cwd"`
}

func cursorCLIAuthPath(home string) string {
	return filepath.Join(home, ".config", "cursor", "auth.json")
}

func cursorCLIChatsRoot(home string) string {
	return filepath.Join(home, ".cursor", "chats")
}

func cursorCLIAvailable(home string) bool {
	return fileExists(cursorCLIAuthPath(home)) || directoryExists(cursorCLIChatsRoot(home))
}

// readCursorAuthForHome supports both Cursor Desktop and the standalone
// cursor-agent CLI. The two clients intentionally keep their login state in
// different locations even when they run as the same OS user.
func readCursorAuthForHome(ctx context.Context, home, desktopDBPath string) (cursorAuth, error) {
	var attempts []error
	if fileExists(desktopDBPath) {
		auth, err := readCursorAuth(ctx, desktopDBPath)
		if err == nil {
			return auth, nil
		}
		attempts = append(attempts, fmt.Errorf("Cursor Desktop login: %w", err))
	}
	cliPath := cursorCLIAuthPath(home)
	if fileExists(cliPath) {
		auth, err := readCursorCLIAuth(ctx, cliPath)
		if err == nil {
			return auth, nil
		}
		attempts = append(attempts, fmt.Errorf("Cursor CLI login: %w", err))
	}
	if len(attempts) > 0 {
		return cursorAuth{}, errors.Join(attempts...)
	}
	return cursorAuth{}, fmt.Errorf("Cursor login was not found in %s or %s", desktopDBPath, cliPath)
}

func readCursorCLIAuth(ctx context.Context, path string) (cursorAuth, error) {
	if err := ctx.Err(); err != nil {
		return cursorAuth{}, err
	}
	raw, err := readCursorCLIFile(path, cursorCLIFileLimit)
	if err != nil {
		return cursorAuth{}, err
	}
	var stored struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		return cursorAuth{}, errors.New("Cursor CLI login file contains invalid JSON")
	}
	auth := cursorAuth{
		AccessToken: strings.TrimSpace(stored.AccessToken), RefreshToken: strings.TrimSpace(stored.RefreshToken),
	}
	if auth.AccessToken == "" {
		return cursorAuth{}, errors.New("Cursor CLI is not signed in on this machine")
	}
	auth.SessionToken, err = cursorSessionToken(auth.AccessToken)
	if err != nil {
		return cursorAuth{}, err
	}
	return auth, nil
}

// discoverCursorCLISessions builds a conversation index without reading
// transcript content. Cursor's dashboard events carry the same conversation
// UUID, so this is enough to attribute exact cloud usage to the right machine
// and project without importing prompts or responses.
func discoverCursorCLISessions(ctx context.Context, home string, since time.Time) (map[string]core.UsageRecord, error) {
	root := cursorCLIChatsRoot(home)
	workspaces, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return map[string]core.UsageRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := map[string]core.UsageRecord{}
	for _, workspace := range workspaces {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !workspace.IsDir() || workspace.Type()&os.ModeSymlink != 0 {
			continue
		}
		workspacePath := filepath.Join(root, workspace.Name())
		sessions, readErr := os.ReadDir(workspacePath)
		if readErr != nil {
			return nil, readErr
		}
		for _, session := range sessions {
			if !session.IsDir() || session.Type()&os.ModeSymlink != 0 {
				continue
			}
			sessionID := strings.TrimSpace(session.Name())
			if sessionID == "" {
				continue
			}
			metaPath := filepath.Join(workspacePath, session.Name(), "meta.json")
			raw, readErr := readCursorCLIFile(metaPath, cursorCLIFileLimit)
			if os.IsNotExist(readErr) {
				continue
			}
			if readErr != nil {
				return nil, readErr
			}
			var meta cursorCLIMeta
			if json.Unmarshal(raw, &meta) != nil || !meta.HasConversation {
				continue
			}
			timestamp := cursorCLITimestamp(meta.UpdatedAtMS)
			if timestamp.IsZero() {
				timestamp = cursorCLITimestamp(meta.CreatedAtMS)
			}
			if !since.IsZero() && !timestamp.IsZero() && timestamp.Before(since) {
				continue
			}
			result[cursorConversationIndexKey(sessionID)] = core.UsageRecord{
				Source: "cursor", RuntimeID: "cursor", SessionID: sessionID, ConversationID: sessionID,
				Project: strings.TrimSpace(meta.CWD), Model: "cursor", Timestamp: timestamp,
				TokenQuality: core.UsageTokenQualityUnknown, CostKind: core.UsageCostKindCalculated,
			}
		}
	}
	return result, nil
}

func cursorConversationIndexKey(sessionID string) string {
	return "conversation:" + strings.TrimSpace(sessionID)
}

func cursorCLITimestamp(milliseconds int64) time.Time {
	if milliseconds <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(milliseconds).UTC()
}

func readCursorCLIFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing non-regular Cursor CLI file %s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("Cursor CLI file exceeded the size limit: %s", path)
	}
	return raw, nil
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
