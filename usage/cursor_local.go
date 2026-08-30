package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wangning19940904/AgentMux/core"
)

const cursorLocalBatchSize = 5000

const (
	cursorProvenanceLocalEstimated = "cursor.local.estimated"
	cursorProvenanceLocalExact     = "cursor.local.exact"
	cursorProvenanceHook           = "cursor.hook"
	cursorProvenanceDashboard      = "cursor.dashboard"

	cursorRankLocalEstimated = 10
	cursorRankLocalExact     = 20
	cursorRankHook           = 30
	cursorRankDashboard      = 40
)

type cursorLocalBatch struct {
	Records   []core.UsageRecord
	LastRowID int64
	More      bool
}

type cursorBubble struct {
	Type       int      `json:"type"`
	Role       string   `json:"role"`
	BubbleID   string   `json:"bubbleId"`
	RequestID  string   `json:"requestId"`
	UsageUUID  string   `json:"usageUuid"`
	CreatedAt  any      `json:"createdAt"`
	Text       string   `json:"text"`
	Workspace  []string `json:"workspaceUris"`
	TokenCount struct {
		InputTokens      int64 `json:"inputTokens"`
		OutputTokens     int64 `json:"outputTokens"`
		CacheReadTokens  int64 `json:"cacheReadTokens"`
		CacheWriteTokens int64 `json:"cacheWriteTokens"`
	} `json:"tokenCount"`
	ContextWindow *struct {
		TokensUsed float64 `json:"tokensUsed"`
	} `json:"contextWindowStatusAtCreation"`
	ModelInfo *struct {
		ModelName string `json:"modelName"`
	} `json:"modelInfo"`
	AttachedCodeChunks json.RawMessage `json:"attachedCodeChunks"`
	CodebaseChunks     json.RawMessage `json:"codebaseContextChunks"`
	ContextPieces      json.RawMessage `json:"contextPieces"`
}

func cursorUserDir(home string) string {
	if configured := strings.TrimSpace(os.Getenv("CURSOR_HOME")); configured != "" {
		return filepath.Clean(configured)
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Cursor", "User")
	case "windows":
		if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
			return filepath.Join(appData, "Cursor", "User")
		}
	}
	return filepath.Join(home, ".config", "Cursor", "User")
}

func cursorStateDBPath(home string) string {
	return filepath.Join(cursorUserDir(home), "globalStorage", "state.vscdb")
}

func openCursorReadOnly(path string) (*sql.DB, error) {
	if info, err := os.Stat(path); err != nil {
		return nil, err
	} else if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("cursor state database is not a regular file: %s", path)
	}
	u := &url.URL{Scheme: "file", Path: path}
	dsn := u.String() + "?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(1500)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func collectCursorLocalBatch(ctx context.Context, dbPath string, afterRowID int64, since time.Time) (cursorLocalBatch, error) {
	db, err := openCursorReadOnly(dbPath)
	if err != nil {
		return cursorLocalBatch{}, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT rowid,key,value FROM cursorDiskKV
		WHERE key LIKE 'bubbleId:%' AND rowid>? ORDER BY rowid ASC LIMIT ?`, afterRowID, cursorLocalBatchSize)
	if err != nil {
		return cursorLocalBatch{}, err
	}
	defer rows.Close()
	result := cursorLocalBatch{LastRowID: afterRowID}
	rowCount := 0
	for rows.Next() {
		var rowID int64
		var key string
		var value []byte
		if err := rows.Scan(&rowID, &key, &value); err != nil {
			return cursorLocalBatch{}, err
		}
		rowCount++
		result.LastRowID = rowID
		record, ok := cursorUsageRecordFromBubble(key, value, since)
		if ok {
			result.Records = append(result.Records, record)
		}
	}
	if err := rows.Err(); err != nil {
		return cursorLocalBatch{}, err
	}
	result.More = rowCount == cursorLocalBatchSize
	return result, nil
}

func cursorUsageRecordFromBubble(key string, value []byte, since time.Time) (core.UsageRecord, bool) {
	composerID, keyBubbleID, ok := parseCursorBubbleKey(key)
	if !ok {
		return core.UsageRecord{}, false
	}
	var bubble cursorBubble
	if err := json.Unmarshal(value, &bubble); err != nil || !cursorAssistantBubble(bubble) {
		return core.UsageRecord{}, false
	}
	ts := cursorTimestamp(bubble.CreatedAt)
	if ts.IsZero() || (!since.IsZero() && ts.Before(since)) {
		return core.UsageRecord{}, false
	}
	input := max64(0, bubble.TokenCount.InputTokens)
	output := max64(0, bubble.TokenCount.OutputTokens)
	cacheRead := max64(0, bubble.TokenCount.CacheReadTokens)
	cacheWrite := max64(0, bubble.TokenCount.CacheWriteTokens)
	quality := core.UsageTokenQualityExact
	provenance := cursorProvenanceLocalExact
	rank := cursorRankLocalExact
	if input+output+cacheRead+cacheWrite == 0 {
		quality = core.UsageTokenQualityEstimated
		provenance = cursorProvenanceLocalEstimated
		rank = cursorRankLocalEstimated
		if bubble.ContextWindow != nil && bubble.ContextWindow.TokensUsed > 0 {
			input = int64(bubble.ContextWindow.TokensUsed + 0.5)
		} else {
			contextBytes := len(bubble.AttachedCodeChunks) + len(bubble.CodebaseChunks) + len(bubble.ContextPieces)
			input = int64((contextBytes + 3) / 4)
		}
		output = int64((utf8.RuneCountInString(bubble.Text) + 3) / 4)
	}
	if input+output+cacheRead+cacheWrite == 0 {
		return core.UsageRecord{}, false
	}
	requestID := firstCursorValue(bubble.RequestID, bubble.UsageUUID, bubble.BubbleID, keyBubbleID)
	model := "cursor"
	if bubble.ModelInfo != nil && strings.TrimSpace(bubble.ModelInfo.ModelName) != "" {
		model = strings.TrimSpace(bubble.ModelInfo.ModelName)
	}
	project := ""
	if len(bubble.Workspace) > 0 {
		project = cursorWorkspacePath(bubble.Workspace[0])
	}
	return core.UsageRecord{
		Source: "cursor", RuntimeID: "cursor", SessionID: composerID, ConversationID: composerID,
		RequestID: requestID, Project: project, Model: model, Timestamp: ts,
		InputTokens: input, OutputTokens: output, CacheReadTokens: cacheRead, CacheWriteTokens: cacheWrite,
		Provenance: provenance, ProvenanceRank: rank, TokenQuality: quality, CostKind: core.UsageCostKindCalculated,
	}, true
}

func parseCursorBubbleKey(key string) (composerID, bubbleID string, ok bool) {
	if !strings.HasPrefix(key, "bubbleId:") {
		return "", "", false
	}
	rest := strings.TrimPrefix(key, "bubbleId:")
	index := strings.LastIndex(rest, ":")
	if index <= 0 || index >= len(rest)-1 {
		return "", "", false
	}
	return rest[:index], rest[index+1:], true
}

func cursorAssistantBubble(b cursorBubble) bool {
	if b.Type == 2 {
		return true
	}
	role := strings.ToLower(strings.TrimSpace(b.Role))
	return role == "assistant" || role == "agent" || role == "ai"
}

func cursorTimestamp(value any) time.Time {
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case json.Number:
		number, _ = typed.Float64()
	case string:
		if parsed, err := time.Parse(time.RFC3339Nano, typed); err == nil {
			return parsed.UTC()
		}
		_, _ = fmt.Sscan(typed, &number)
	}
	if number <= 0 {
		return time.Time{}
	}
	if number < 1e11 {
		number *= 1000
	}
	return time.UnixMilli(int64(number)).UTC()
}

func cursorWorkspacePath(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "file://") {
		if parsed, err := url.Parse(value); err == nil && parsed.Path != "" {
			return filepath.Clean(parsed.Path)
		}
	}
	return value
}

func firstCursorValue(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func max64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

var errCursorNotConnected = errors.New("cursor usage source is not connected")
