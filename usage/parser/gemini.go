package parser

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// geminiCollector parses Gemini CLI logs. Gemini stores session telemetry
// under ~/.gemini/tmp/<id>/logs or similar; we walk JSONL logs and pick up
// usageMetadata blocks.
type geminiCollector struct {
	root string
}

func (c *geminiCollector) Source() string { return "gemini" }

func (c *geminiCollector) base() string {
	if c.root != "" {
		return c.root
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gemini")
}

type geminiLine struct {
	Timestamp     string `json:"timestamp"`
	Model         string `json:"model"`
	SessionID     string `json:"sessionId"`
	UsageMetadata *struct {
		PromptTokenCount        int64 `json:"promptTokenCount"`
		CandidatesTokenCount    int64 `json:"candidatesTokenCount"`
		CachedContentTokenCount int64 `json:"cachedContentTokenCount"`
	} `json:"usageMetadata"`
}

func (c *geminiCollector) Collect(ctx context.Context, since time.Time) ([]core.UsageRecord, error) {
	var out []core.UsageRecord
	err := filepath.WalkDir(c.base(), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		if skipUnmodified(d, since) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		out = append(out, c.parseFile(path, since)...)
		return nil
	})
	if os.IsNotExist(err) {
		return out, nil
	}
	return out, err
}

func (c *geminiCollector) parseFile(path string, since time.Time) []core.UsageRecord {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []core.UsageRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		var l geminiLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			continue
		}
		if l.UsageMetadata == nil {
			continue
		}
		ts, _ := time.Parse(time.RFC3339Nano, l.Timestamp)
		if !sinceOK(ts, since) {
			continue
		}
		out = append(out, core.UsageRecord{
			Source:          "gemini",
			SessionID:       l.SessionID,
			Model:           orDefault(l.Model, "gemini"),
			Timestamp:       ts,
			InputTokens:     l.UsageMetadata.PromptTokenCount,
			OutputTokens:    l.UsageMetadata.CandidatesTokenCount,
			CacheReadTokens: l.UsageMetadata.CachedContentTokenCount,
		})
	}
	return out
}
