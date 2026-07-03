package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"

	"github.com/agentnexus/agentnexus/core"
)

// session drives one `claude` subprocess invocation per turn using
// --print --output-format stream-json, which yields newline-delimited JSON
// events that we map to core.Event.
type session struct {
	agent   *Agent
	workDir string
	id      string
	mu      sync.Mutex
}

func newSession(a *Agent, workDir string) (*session, error) {
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	return &session{agent: a, workDir: workDir, id: "claude-" + randID()}, nil
}

func (s *session) ID() string { return s.id }

// Send runs one turn and streams events.
func (s *session) Send(ctx context.Context, text string) (<-chan *core.Event, error) {
	out := make(chan *core.Event, 16)

	args := []string{"--print", "--output-format", "stream-json", "--verbose"}
	if s.agent.systemPrompt != "" {
		args = append(args, "--append-system-prompt", s.agent.systemPrompt)
	}
	args = append(args, text)

	cmd := exec.CommandContext(ctx, claudeBinary(), args...)
	cmd.Dir = s.workDir
	cmd.Env = buildEnv(s.agent.env)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	go func() {
		defer close(out)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
		for sc.Scan() {
			ev := mapStreamLine(sc.Bytes())
			if ev != nil {
				out <- ev
			}
		}
		if err := cmd.Wait(); err != nil {
			out <- &core.Event{Type: core.EventError, Err: err}
		}
	}()
	return out, nil
}

func (s *session) RespondPermission(ctx context.Context, allow bool) error {
	return nil // print mode auto-approves per its own flags
}

func (s *session) Close(ctx context.Context) error { return nil }

// streamLine is the subset of Claude Code stream-json we map.
type streamLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Result  string `json:"result"`
	Message struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Model string `json:"model"`
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

func mapStreamLine(b []byte) *core.Event {
	var l streamLine
	if err := json.Unmarshal(b, &l); err != nil {
		return nil
	}
	switch l.Type {
	case "assistant":
		var text string
		for _, c := range l.Message.Content {
			if c.Type == "text" {
				text += c.Text
			}
		}
		ev := &core.Event{Type: core.EventOutput, Text: text}
		if l.Message.Model != "" {
			ev.Usage = &core.TurnUsage{
				Model:            l.Message.Model,
				InputTokens:      l.Message.Usage.InputTokens,
				OutputTokens:     l.Message.Usage.OutputTokens,
				CacheReadTokens:  l.Message.Usage.CacheReadInputTokens,
				CacheWriteTokens: l.Message.Usage.CacheCreationInputTokens,
			}
		}
		return ev
	case "result":
		return &core.Event{Type: core.EventFinal, Text: l.Result, Final: true}
	default:
		return nil
	}
}

func buildEnv(extra map[string]string) []string {
	env := os.Environ()
	// Unset CLAUDECODE so a nested claude can launch (INSTALL.md gotcha).
	filtered := env[:0]
	for _, e := range env {
		if len(e) >= 11 && e[:11] == "CLAUDECODE=" {
			continue
		}
		filtered = append(filtered, e)
	}
	for k, v := range extra {
		filtered = append(filtered, fmt.Sprintf("%s=%s", k, v))
	}
	return filtered
}
