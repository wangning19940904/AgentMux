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

	mu       sync.Mutex
	nativeID string // claude-native session id, discovered from stream output
	resumeID string // native id to resume on the next Send (persisted context)
	model    *core.ModelSelection
}

func newSession(a *Agent, workDir string) (*session, error) {
	return newSessionResume(a, workDir, "")
}

func newSessionResume(a *Agent, workDir, resumeID string) (*session, error) {
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	return &session{
		agent:    a,
		workDir:  workDir,
		id:       "claude-" + randID(),
		nativeID: resumeID,
		resumeID: resumeID,
		model:    core.NewModelSelection(a.defaultModel, a.supportedModels),
	}, nil
}

func (s *session) ID() string { return s.id }

func (s *session) ModelSwitchingSupported() bool { return true }
func (s *session) CurrentModel() string          { return s.model.CurrentModel() }
func (s *session) DefaultModel() string          { return s.model.DefaultModel() }
func (s *session) SupportedModels() []string     { return s.model.SupportedModels() }
func (s *session) SetModel(model string) error   { return s.model.SetModel(model) }
func (s *session) ResetModel() error             { return s.model.ResetModel() }

// NativeSessionID returns the claude-native session id discovered so far.
func (s *session) NativeSessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nativeID
}

// Send runs one turn and streams events.
func (s *session) Send(ctx context.Context, text string) (<-chan *core.Event, error) {
	out := make(chan *core.Event, 16)
	args := s.args(text)

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
		m := &streamMapper{}
		for sc.Scan() {
			line := sc.Bytes()
			if sid := parseSessionID(line); sid != "" {
				s.mu.Lock()
				s.nativeID = sid
				s.resumeID = sid
				s.mu.Unlock()
			}
			ev := m.map_(line)
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

func (s *session) args(text string) []string {
	args := []string{"--print", "--output-format", "stream-json", "--verbose", "--include-partial-messages"}
	if s.agent.systemPrompt != "" {
		args = append(args, "--append-system-prompt", s.agent.systemPrompt)
	}
	if model := s.CurrentModel(); model != "" {
		args = append(args, "--model", model)
	}
	// Resume prior context when we already know the native session id, so the
	// conversation carries across turns and process restarts.
	s.mu.Lock()
	resume := s.resumeID
	s.mu.Unlock()
	if resume != "" {
		args = append(args, "--resume", resume)
	}
	args = append(args, text)
	return args
}

func (s *session) RespondPermission(ctx context.Context, allow bool) error {
	return nil // print mode auto-approves per its own flags
}

func (s *session) Close(ctx context.Context) error { return nil }

// streamLine is the subset of Claude Code stream-json we map. With
// --include-partial-messages the CLI additionally emits "stream_event" lines
// carrying token-level deltas (event.delta.text) that we surface as they
// arrive so downstream renderers can show the answer being typed out.
type streamLine struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	Result    string `json:"result"`
	SessionID string `json:"session_id"`
	Event     struct {
		Type  string `json:"type"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	} `json:"event"`
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

// streamMapper turns Claude Code stream-json lines into core.Events while
// accumulating token deltas into a running buffer. Each text_delta yields an
// EventOutput carrying the full accumulated text so far, which lets in-place
// renderers (Feishu streaming card) grow the reply as the model types.
type streamMapper struct {
	buf string
}

func (m *streamMapper) map_(b []byte) *core.Event {
	var l streamLine
	if err := json.Unmarshal(b, &l); err != nil {
		return nil
	}
	switch l.Type {
	case "stream_event":
		if l.Event.Type == "content_block_delta" && l.Event.Delta.Type == "text_delta" && l.Event.Delta.Text != "" {
			m.buf += l.Event.Delta.Text
			return &core.Event{Type: core.EventOutput, Text: m.buf}
		}
		return nil
	case "assistant":
		var text string
		for _, c := range l.Message.Content {
			if c.Type == "text" {
				text += c.Text
			}
		}
		// The complete assistant message is authoritative; resync the buffer
		// so any bytes missed by delta parsing are reflected.
		if text != "" {
			m.buf = text
		}
		ev := &core.Event{Type: core.EventOutput, Text: m.buf}
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
		text := l.Result
		if text == "" {
			text = m.buf
		}
		return &core.Event{Type: core.EventFinal, Text: text, Final: true}
	default:
		return nil
	}
}

// parseSessionID extracts the claude-native session id from any stream line
// that carries one (the init "system" line and the final "result" line both
// include session_id). Returns "" when absent.
func parseSessionID(b []byte) string {
	var l streamLine
	if err := json.Unmarshal(b, &l); err != nil {
		return ""
	}
	return l.SessionID
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
