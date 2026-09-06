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

// claudeCollector parses Claude Code transcripts at
// ~/.claude/projects/<slug>/<session>.jsonl. Each assistant entry carries the
// model name and a usage block with token counts.
type claudeCollector struct {
	root string // override base dir; "" => ~/.claude
}

func (c *claudeCollector) Source() string { return "claude" }

func (c *claudeCollector) base() string {
	if c.root != "" {
		return c.root
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// claudeLine is the subset of a transcript line we care about.
type claudeLine struct {
	Type       string `json:"type"`
	Timestamp  string `json:"timestamp"`
	SessionID  string `json:"sessionId"`
	Cwd        string `json:"cwd"`
	Entrypoint string `json:"entrypoint"`
	Message    struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

func (c *claudeCollector) Collect(ctx context.Context, since time.Time) ([]core.UsageRecord, error) {
	projectsDir := filepath.Join(c.base(), "projects")
	var out []core.UsageRecord
	err := filepath.WalkDir(projectsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // tolerate missing dirs
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
		recs := c.parseFile(path, since)
		out = append(out, recs...)
		return nil
	})
	if os.IsNotExist(err) {
		return out, nil
	}
	return out, err
}

func (c *claudeCollector) parseFile(path string, since time.Time) []core.UsageRecord {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	project := filepath.Base(filepath.Dir(path))
	var out []core.UsageRecord
	runtime := ClaudeRuntime("")
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		var l claudeLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			continue
		}
		if l.Entrypoint != "" {
			runtime = ClaudeRuntime(l.Entrypoint)
		}
		if l.Type != "assistant" || l.Message.Model == "" {
			continue
		}
		ts, _ := time.Parse(time.RFC3339Nano, l.Timestamp)
		if !sinceOK(ts, since) {
			continue
		}
		u := l.Message.Usage
		if u.InputTokens == 0 && u.OutputTokens == 0 &&
			u.CacheReadInputTokens == 0 && u.CacheCreationInputTokens == 0 {
			continue
		}
		out = append(out, core.UsageRecord{
			Source:           "claude",
			RuntimeID:        runtime,
			SessionID:        l.SessionID,
			Project:          project,
			Model:            l.Message.Model,
			Timestamp:        ts,
			InputTokens:      u.InputTokens,
			OutputTokens:     u.OutputTokens,
			CacheReadTokens:  u.CacheReadInputTokens,
			CacheWriteTokens: u.CacheCreationInputTokens,
		})
	}
	return out
}
