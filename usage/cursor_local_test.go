package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func TestCollectCursorLocalExactEstimatedAndCheckpoint(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.vscdb")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE cursorDiskKV(key TEXT PRIMARY KEY,value BLOB)`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	insertCursorBubble(t, db, "bubbleId:session-a:bubble-a", map[string]any{
		"type": 2, "bubbleId": "bubble-a", "requestId": "request-a", "createdAt": now.UnixMilli(),
		"text": "answer", "tokenCount": map[string]any{"inputTokens": 100, "outputTokens": 20},
		"modelInfo": map[string]any{"modelName": "claude-sonnet"}, "workspaceUris": []string{"file:///tmp/project"},
	})
	insertCursorBubble(t, db, "bubbleId:session-a:bubble-b", map[string]any{
		"type": 2, "bubbleId": "bubble-b", "requestId": "request-b", "createdAt": now.Add(time.Second).UnixMilli(),
		"text": "12345678", "tokenCount": map[string]any{},
		"contextWindowStatusAtCreation": map[string]any{"tokensUsed": 80},
	})
	insertCursorBubble(t, db, "bubbleId:session-a:user", map[string]any{
		"type": 1, "bubbleId": "user", "createdAt": now.UnixMilli(), "text": "private prompt",
	})
	if _, err := db.Exec(`INSERT INTO cursorDiskKV(key,value) VALUES(?,?)`, "bubbleId:session-a:bad", []byte(`{`)); err != nil {
		t.Fatal(err)
	}

	batch, err := collectCursorLocalBatch(context.Background(), dbPath, 0, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Records) != 2 || batch.LastRowID == 0 {
		t.Fatalf("batch = %+v", batch)
	}
	byID := map[string]core.UsageRecord{}
	for _, record := range batch.Records {
		byID[record.RequestID] = record
	}
	if exact := byID["request-a"]; exact.InputTokens != 100 || exact.OutputTokens != 20 || exact.TokenQuality != core.UsageTokenQualityExact || exact.Project != "/tmp/project" {
		t.Fatalf("exact = %+v", exact)
	}
	if estimated := byID["request-b"]; estimated.InputTokens != 80 || estimated.OutputTokens != 2 || estimated.TokenQuality != core.UsageTokenQualityEstimated {
		t.Fatalf("estimated = %+v", estimated)
	}
	next, err := collectCursorLocalBatch(context.Background(), dbPath, batch.LastRowID, now.Add(-time.Hour))
	if err != nil || len(next.Records) != 0 {
		t.Fatalf("next = %+v err=%v", next, err)
	}
}

func TestCursorReadOnlySeesCommittedWAL(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.vscdb")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE cursorDiskKV(key TEXT PRIMARY KEY,value BLOB)`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	insertCursorBubble(t, db, "bubbleId:wal-session:wal-bubble", map[string]any{
		"type": 2, "requestId": "wal-request", "createdAt": now.UnixMilli(),
		"tokenCount": map[string]any{"inputTokens": 5, "outputTokens": 1},
	})
	batch, err := collectCursorLocalBatch(context.Background(), dbPath, 0, now.Add(-time.Hour))
	if err != nil || len(batch.Records) != 1 || batch.Records[0].RequestID != "wal-request" {
		t.Fatalf("batch=%+v err=%v", batch, err)
	}
}

func TestCursorHookInstallPreservesExistingEntries(t *testing.T) {
	home := t.TempDir()
	helper := filepath.Join(t.TempDir(), "agentmux-hook")
	if err := os.WriteFile(helper, []byte("helper"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTMUX_HOOK_HELPER", helper)
	hooksPath := cursorHooksPath(home)
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o700); err != nil {
		t.Fatal(err)
	}
	existing := []byte(`{"version":1,"hooks":{"stop":[{"command":"flux-island --source cursor"}],"sessionStart":[{"command":"keep-me"}]}}`)
	if err := os.WriteFile(hooksPath, existing, 0o600); err != nil {
		t.Fatal(err)
	}
	if status, err := installCursorHook(home); err != nil || status.Status != "healthy" {
		t.Fatalf("install status=%+v err=%v", status, err)
	}
	raw, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	root, err := parseCursorHooks(raw)
	if err != nil {
		t.Fatal(err)
	}
	stop := cursorHooksMap(root)["stop"].([]any)
	if len(stop) != 2 || !cursorHookInstalled(root, home) {
		t.Fatalf("hooks = %s", raw)
	}
	if _, err := removeCursorHook(home); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(hooksPath)
	root, _ = parseCursorHooks(raw)
	stop = cursorHooksMap(root)["stop"].([]any)
	if len(stop) != 1 || stop[0].(map[string]any)["command"] != "flux-island --source cursor" {
		t.Fatalf("preserved hooks = %s", raw)
	}
}

func insertCursorBubble(t *testing.T, db *sql.DB, key string, value map[string]any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO cursorDiskKV(key,value) VALUES(?,?)`, key, raw); err != nil {
		t.Fatal(err)
	}
}
