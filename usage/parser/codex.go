package parser

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agentnexus/agentnexus/core"
)

// codexCollector parses Codex sessions at
// ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl. token_count events carry
// per-call token usage.
type codexCollector struct {
	root string
}

func (c *codexCollector) Source() string { return "codex" }

func (c *codexCollector) base() string {
	if c.root != "" {
		return c.root
	}
	if v := os.Getenv("CODEX_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

type codexLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Model     string `json:"model"`
	TokenCount *struct {
		InputTokens       int64 `json:"input_tokens"`
		OutputTokens      int64 `json:"output_tokens"`
		CachedInputTokens int64 `json:"cached_input_tokens"`
	} `json:"token_count"`
}

func (c *codexCollector) Collect(ctx context.Context, since time.Time) ([]core.UsageRecord, error) {
	sessionsDir := filepath.Join(c.base(), "sessions")
	var out []core.UsageRecord
	err := filepath.WalkDir(sessionsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() || !strings.HasPrefix(d.Name(), "rollout-") || !strings.HasSuffix(path, ".jsonl") {
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

func (c *codexCollector) parseFile(path string, since time.Time) []core.UsageRecord {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	sessionID := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	var out []core.UsageRecord
	var model string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		var l codexLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			continue
		}
		if l.Model != "" {
			model = l.Model
		}
		if l.TokenCount == nil {
			continue
		}
		ts, _ := time.Parse(time.RFC3339Nano, l.Timestamp)
		if !sinceOK(ts, since) {
			continue
		}
		out = append(out, core.UsageRecord{
			Source:          "codex",
			SessionID:       sessionID,
			Project:         "",
			Model:           orDefault(model, "gpt-5"),
			Timestamp:       ts,
			InputTokens:     l.TokenCount.InputTokens,
			OutputTokens:    l.TokenCount.OutputTokens,
			CacheReadTokens: l.TokenCount.CachedInputTokens,
		})
	}
	return out
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
