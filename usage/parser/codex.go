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
// ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl. Current Codex versions nest
// cumulative token_count events under event_msg.payload; older versions used
// a top-level token_count object.
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
	Type       string             `json:"type"`
	Timestamp  string             `json:"timestamp"`
	Model      string             `json:"model"`
	TokenCount *codexUsageNumbers `json:"token_count"`
	Payload    struct {
		Type  string `json:"type"`
		ID    string `json:"id"`
		Model string `json:"model"`
		Cwd   string `json:"cwd"`
		Info  *struct {
			TotalTokenUsage codexUsageNumbers `json:"total_token_usage"`
			LastTokenUsage  codexUsageNumbers `json:"last_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

type codexUsageNumbers struct {
	InputTokens           int64 `json:"input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
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
	var project string
	var previous codexUsageNumbers
	var havePrevious bool
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
		if l.Payload.ID != "" && l.Type == "session_meta" {
			sessionID = l.Payload.ID
		}
		if l.Payload.Model != "" {
			model = l.Payload.Model
		}
		if l.Payload.Cwd != "" {
			project = l.Payload.Cwd
		}

		ts, _ := time.Parse(time.RFC3339Nano, l.Timestamp)
		if l.TokenCount != nil {
			if !sinceOK(ts, since) || l.TokenCount.isZero() {
				continue
			}
			out = append(out, codexUsageRecord(sessionID, project, model, ts, *l.TokenCount))
			continue
		}
		if l.Type != "event_msg" || l.Payload.Type != "token_count" || l.Payload.Info == nil {
			continue
		}
		current := l.Payload.Info.TotalTokenUsage
		if current.isZero() {
			continue
		}
		delta, reset := current.deltaFrom(previous, havePrevious)
		previous, havePrevious = current, true
		if reset && !l.Payload.Info.LastTokenUsage.isZero() {
			delta = l.Payload.Info.LastTokenUsage
		}
		// Identical cumulative totals are repeated telemetry, not another model
		// request. Suppressing them prevents double counting on transcript scans.
		if delta.isZero() {
			continue
		}
		if !sinceOK(ts, since) {
			continue
		}
		out = append(out, codexUsageRecord(sessionID, project, model, ts, delta))
	}
	return out
}

func codexUsageRecord(sessionID, project, model string, ts time.Time, usage codexUsageNumbers) core.UsageRecord {
	return core.UsageRecord{
		Source:          "codex",
		SessionID:       sessionID,
		Project:         project,
		Model:           orDefault(model, "gpt-5"),
		Timestamp:       ts,
		InputTokens:     codexUncachedInput(usage.InputTokens, usage.CachedInputTokens),
		OutputTokens:    usage.OutputTokens,
		CacheReadTokens: usage.CachedInputTokens,
	}
}

func codexUncachedInput(input, cached int64) int64 {
	if input <= cached {
		return 0
	}
	return input - cached
}

func (u codexUsageNumbers) isZero() bool {
	return u.InputTokens == 0 && u.OutputTokens == 0 && u.CachedInputTokens == 0 &&
		u.ReasoningOutputTokens == 0 && u.TotalTokens == 0
}

func (u codexUsageNumbers) deltaFrom(previous codexUsageNumbers, havePrevious bool) (codexUsageNumbers, bool) {
	if !havePrevious {
		return u, false
	}
	if u.InputTokens < previous.InputTokens || u.OutputTokens < previous.OutputTokens ||
		u.CachedInputTokens < previous.CachedInputTokens || u.ReasoningOutputTokens < previous.ReasoningOutputTokens ||
		u.TotalTokens < previous.TotalTokens {
		return u, true
	}
	return codexUsageNumbers{
		InputTokens: u.InputTokens - previous.InputTokens, OutputTokens: u.OutputTokens - previous.OutputTokens,
		CachedInputTokens:     u.CachedInputTokens - previous.CachedInputTokens,
		ReasoningOutputTokens: u.ReasoningOutputTokens - previous.ReasoningOutputTokens,
		TotalTokens:           u.TotalTokens - previous.TotalTokens,
	}, false
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
