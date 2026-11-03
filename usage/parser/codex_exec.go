package parser

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// Ephemeral exec runs have no rollout. Callers can persist token-only receipts
// under CODEX_HOME/usage/agentmux-exec; each receipt is one completed CLI turn,
// potentially containing several model requests. Non-ephemeral receipts are
// deliberately rejected: their rollouts are the authoritative usage source.
type codexExecReceipt struct {
	Schema       string    `json:"schema"`
	Ephemeral    bool      `json:"ephemeral"`
	InvocationID string    `json:"invocation_id"`
	SessionID    string    `json:"session_id"`
	TurnIndex    int       `json:"turn_index"`
	Timestamp    time.Time `json:"timestamp"`
	Model        string    `json:"model"`
	Project      string    `json:"project"`
	Usage        struct {
		Input      int64 `json:"input_tokens"`
		Cached     int64 `json:"cached_input_tokens"`
		CacheWrite int64 `json:"cache_write_input_tokens"`
		Output     int64 `json:"output_tokens"`
	} `json:"usage"`
}

func (c *codexCollector) collectExecReceipts(ctx context.Context, since time.Time) ([]core.UsageRecord, error) {
	var records []core.UsageRecord
	err := filepath.WalkDir(filepath.Join(c.base(), "usage", "agentmux-exec"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || skipUnmodified(entry, since) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > 64*1024 {
			return fmt.Errorf("Codex usage receipt exceeds 64 KiB: %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var receipt codexExecReceipt
		if err := json.Unmarshal(data, &receipt); err != nil {
			return fmt.Errorf("invalid Codex usage receipt %s: %w", path, err)
		}
		if receipt.Schema != "agentmux.codex-exec-usage.v1" || !receipt.Ephemeral {
			return nil
		}
		u := receipt.Usage
		if receipt.InvocationID == "" || receipt.SessionID == "" || receipt.TurnIndex < 0 || receipt.Model == "" || receipt.Timestamp.IsZero() ||
			u.Input < 0 || u.Output < 0 || u.Cached < 0 || u.CacheWrite < 0 || u.Cached > u.Input || u.CacheWrite > u.Input-u.Cached {
			return fmt.Errorf("invalid Codex usage receipt fields: %s", path)
		}
		if !sinceOK(receipt.Timestamp, since) || (u.Input == 0 && u.Output == 0) {
			return nil
		}
		records = append(records, core.UsageRecord{
			Source: "codex", RuntimeID: "codex", SessionID: receipt.SessionID,
			RequestID: fmt.Sprintf("codex-exec:%s:%d", receipt.InvocationID, receipt.TurnIndex),
			Model:     receipt.Model, Project: receipt.Project, Timestamp: receipt.Timestamp,
			InputTokens: u.Input - u.Cached - u.CacheWrite, OutputTokens: u.Output,
			CacheReadTokens: u.Cached, CacheWriteTokens: u.CacheWrite,
			Provenance: "codex.exec.receipt", ProvenanceRank: 100, TokenQuality: core.UsageTokenQualityExact,
		})
		return nil
	})
	return records, err
}
