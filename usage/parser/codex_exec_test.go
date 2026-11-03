package parser

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestCodexCollectsArchivedAndEphemeralUsage(t *testing.T) {
	root := t.TempDir()
	writeClientFixture(t, filepath.Join(root, "archived_sessions", "rollout-old.jsonl"),
		`{"type":"session_meta","payload":{"id":"archived","originator":"codex_exec","source":"exec"}}`+"\n"+
			`{"timestamp":"2026-10-14T01:00:00Z","type":"token_count","token_count":{"input_tokens":10,"output_tokens":2}}`+"\n")
	r := codexExecReceipt{Schema: "agentmux.codex-exec-usage.v1", Ephemeral: true, InvocationID: "run", SessionID: "worker", Model: "model", Project: "/project", Timestamp: time.Date(2026, 10, 14, 2, 0, 0, 0, time.UTC)}
	r.Usage.Input, r.Usage.Cached, r.Usage.CacheWrite, r.Usage.Output = 100, 60, 10, 20
	data, _ := json.Marshal(r)
	writeClientFixture(t, filepath.Join(root, "usage", "agentmux-exec", "receipt.json"), string(data))
	c := &codexCollector{root: root}
	records, err := c.Collect(context.Background(), time.Time{})
	if err != nil || len(records) != 2 {
		t.Fatalf("records=%+v err=%v", records, err)
	}
	got := records[1]
	if got.RuntimeID != "codex" || got.Provenance != "codex.exec.receipt" || got.RequestID != "codex-exec:run:0" || got.InputTokens != 30 || got.CacheReadTokens != 60 || got.CacheWriteTokens != 10 || got.OutputTokens != 20 {
		t.Fatalf("receipt=%+v", got)
	}
	records, err = c.Collect(context.Background(), r.Timestamp.Add(time.Second))
	if err != nil || len(records) != 0 {
		t.Fatalf("time filter=%+v %v", records, err)
	}
	// Regular exec is already covered by rollouts, and must not count twice.
	r.Ephemeral = false
	data, _ = json.Marshal(r)
	writeClientFixture(t, filepath.Join(root, "usage", "agentmux-exec", "receipt.json"), string(data))
	records, err = c.Collect(context.Background(), time.Time{})
	if err != nil || len(records) != 1 {
		t.Fatalf("regular exec counted twice: %+v %v", records, err)
	}
}

func TestCodexRejectsInvalidReceiptAndCancellation(t *testing.T) {
	root := t.TempDir()
	writeClientFixture(t, filepath.Join(root, "usage", "agentmux-exec", "receipt.json"), `{"schema":"agentmux.codex-exec-usage.v1","ephemeral":true}`)
	c := &codexCollector{root: root}
	if _, err := c.Collect(context.Background(), time.Time{}); err == nil {
		t.Fatal("invalid receipt silently accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Collect(ctx, time.Time{}); err != context.Canceled {
		t.Fatalf("cancel=%v", err)
	}
}
