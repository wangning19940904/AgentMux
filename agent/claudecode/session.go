package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/wangning19940904/AgentMux/agent/internal/runner"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/internal/procutil"
)

// session drives one `claude` subprocess invocation per turn using
// --print --output-format stream-json, which yields newline-delimited JSON
// events that we map to core.Event.
type session struct {
	runner.Settings
	agent   *Agent
	workDir string
	id      string

	mu       sync.Mutex
	nativeID string // claude-native session id, discovered from stream output
	resumeID string // native id to resume on the next Send (persisted context)
}

func newSession(a *Agent, workDir string) (*session, error) {
	return newSessionResume(a, workDir, "")
}

func newSessionResume(a *Agent, workDir, resumeID string) (*session, error) {
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	return &session{
		Settings: runner.NewSettings(core.RuntimeSettings{
			Model:           a.defaultModel,
			ReasoningEffort: a.defaultReasoningEffort,
			ApprovalMode:    a.defaultApprovalMode,
		}, core.RuntimeSettingsCapabilities{
			Models:           core.RuntimeOptions(a.supportedModels),
			ReasoningEfforts: core.RuntimeOptions(a.supportedReasoningEfforts),
			ApprovalModes:    core.RuntimeOptions(a.supportedApprovalModes),
		}),
		agent:    a,
		workDir:  workDir,
		id:       "claude-" + runner.RandID(),
		nativeID: resumeID,
		resumeID: resumeID,
	}, nil
}

func (s *session) ID() string { return s.id }

// NativeSessionID returns the claude-native session id discovered so far.
func (s *session) NativeSessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nativeID
}

// Send runs one turn and streams events.
func (s *session) Send(ctx context.Context, text string) (<-chan *core.Event, error) {
	out := make(chan *core.Event, 16)
	requestedModel := s.CurrentModel()
	args := s.args(text)

	cmd := exec.CommandContext(ctx, claudeBinary(), args...)
	procutil.Prepare(cmd)
	cmd.Dir = s.workDir
	// Drop CLAUDECODE so a nested claude can launch (INSTALL.md gotcha).
	cmd.Env = withObservationTelemetry(
		runner.WithTraceparent(runner.BuildEnv(s.agent.env, "CLAUDECODE"), core.ObservationTraceparent(ctx)),
		core.ObservationChildTelemetryFromContext(ctx),
	)

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
		m := &streamMapper{requestedModel: requestedModel}
		for sc.Scan() {
			line := sc.Bytes()
			if sid := parseSessionID(line); sid != "" {
				s.mu.Lock()
				s.nativeID = sid
				s.resumeID = sid
				s.mu.Unlock()
			}
			for _, ev := range m.map_(line) {
				if ev != nil {
					out <- ev
				}
			}
		}
		if err := cmd.Wait(); err != nil {
			out <- &core.Event{Type: core.EventError, Err: err}
		}
	}()
	return out, nil
}

// claudeApprovalArgs maps each approval mode to its claude CLI flags.
var claudeApprovalArgs = map[string][]string{
	core.ApprovalModeManual:   {"--permission-mode", "manual"},
	core.ApprovalModeAutoEdit: {"--permission-mode", "acceptEdits"},
	core.ApprovalModeAuto:     {"--permission-mode", "auto"},
	core.ApprovalModePlan:     {"--permission-mode", "plan"},
	core.ApprovalModeYolo:     {"--dangerously-skip-permissions"},
}

func (s *session) args(text string) []string {
	args := []string{"--print", "--output-format", "stream-json", "--verbose", "--include-partial-messages"}
	if s.agent.systemPrompt != "" {
		args = append(args, "--append-system-prompt", s.agent.systemPrompt)
	}
	if model := s.CurrentModel(); model != "" {
		args = append(args, "--model", model)
	}
	if effort := s.CurrentRuntimeSettings().ReasoningEffort; effort != "" {
		args = append(args, "--effort", effort)
	}
	args = append(args, claudeApprovalArgs[s.CurrentRuntimeSettings().ApprovalMode]...)
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
	UUID      string `json:"uuid"`
	Event     struct {
		Type  string `json:"type"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	} `json:"event"`
	Message struct {
		Role    string          `json:"role"`
		Content []streamContent `json:"content"`
		Model   string          `json:"model"`
		Usage   struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// streamContent is one content block in an assistant/user message. Assistant
// messages carry text and tool_use blocks; user messages carry tool_result
// blocks (whose Content is the tool's output, either a string or an array of
// {type,text} parts).
type streamContent struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	ToolUseID string          `json:"tool_use_id"`
	Text      string          `json:"text"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

// streamMapper turns Claude Code stream-json lines into core.Events while
// accumulating token deltas into a running buffer. Each text_delta yields an
// EventOutput carrying the full accumulated text so far, which lets in-place
// renderers (Feishu streaming card) grow the reply as the model types.
type streamMapper struct {
	buf            string
	requestedModel string
}

func (m *streamMapper) map_(b []byte) []*core.Event {
	var l streamLine
	if err := json.Unmarshal(b, &l); err != nil {
		return nil
	}
	switch l.Type {
	case "stream_event":
		if l.Event.Type == "content_block_delta" && l.Event.Delta.Type == "text_delta" && l.Event.Delta.Text != "" {
			m.buf += l.Event.Delta.Text
			return []*core.Event{{Type: core.EventOutput, Text: m.buf}}
		}
		return nil
	case "assistant":
		return m.mapAssistant(l)
	case "user":
		// User messages during a turn carry tool_result blocks (the output of
		// the tools the assistant just invoked).
		var evs []*core.Event
		for _, c := range l.Message.Content {
			if c.Type == "tool_result" {
				status := "completed"
				if c.IsError {
					status = "failed"
				}
				evs = append(evs, &core.Event{
					Type:          core.EventToolUse,
					EventID:       claudeLifecycleEventID(c.ToolUseID, "completed", l.UUID),
					ItemID:        c.ToolUseID,
					ToolCallID:    c.ToolUseID,
					ToolResult:    summarizeToolResult(c.Content),
					ToolResultRaw: string(c.Content),
					Status:        status,
					Final:         false,
					Err:           toolResultErr(c.IsError),
					Metadata:      claudeEventMetadata("completed"),
				})
			}
		}
		return evs
	case "result":
		text := l.Result
		if text == "" {
			text = m.buf
		}
		return []*core.Event{{Type: core.EventFinal, Text: text, Final: true}}
	default:
		return nil
	}
}

// mapAssistant turns one assistant message into text output plus one
// EventToolUse per tool_use block, preserving order (tool calls before the
// output that references them).
func (m *streamMapper) mapAssistant(l streamLine) []*core.Event {
	var evs []*core.Event
	var text string
	for _, c := range l.Message.Content {
		switch c.Type {
		case "text":
			text += c.Text
		case "tool_use":
			evs = append(evs, &core.Event{
				Type:         core.EventToolUse,
				EventID:      claudeLifecycleEventID(c.ID, "started", l.UUID),
				ItemID:       c.ID,
				ToolCallID:   c.ID,
				ToolName:     c.Name,
				ToolInput:    summarizeToolInput(c.Input),
				ToolInputRaw: string(c.Input),
				Status:       "in_progress",
				Metadata:     claudeEventMetadata("started"),
			})
		}
	}
	// The complete assistant message is authoritative; resync the buffer so any
	// bytes missed by delta parsing are reflected.
	if text != "" {
		m.buf = text
	}
	// Only emit an output event when this message actually carried text; a
	// tool-only assistant turn should not blank the accumulated answer.
	if text != "" || len(evs) == 0 {
		evs = append(evs, &core.Event{Type: core.EventOutput, Text: m.buf})
	}
	if hasClaudeUsage(l) {
		u := l.Message.Usage
		total := u.InputTokens + u.OutputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
		evs = append(evs, &core.Event{
			Type:    core.EventModelResponse,
			EventID: claudeLifecycleEventID(l.UUID, "response", ""),
			Status:  "completed",
			Usage: &core.TurnUsage{
				Model:            l.Message.Model,
				RequestID:        l.UUID,
				RequestedModel:   m.requestedModel,
				ResolvedModel:    l.Message.Model,
				InputTokens:      u.InputTokens,
				OutputTokens:     u.OutputTokens,
				CacheReadTokens:  u.CacheReadInputTokens,
				CacheWriteTokens: u.CacheCreationInputTokens,
				TotalTokens:      total,
			},
			Metadata: claudeEventMetadata("response"),
		})
	}
	return evs
}

func hasClaudeUsage(l streamLine) bool {
	u := l.Message.Usage
	return l.Message.Model != "" || u.InputTokens != 0 || u.OutputTokens != 0 ||
		u.CacheReadInputTokens != 0 || u.CacheCreationInputTokens != 0
}

func claudeLifecycleEventID(toolID, lifecycle, fallback string) string {
	if toolID == "" {
		toolID = fallback
	}
	if toolID == "" {
		return ""
	}
	return toolID + ":" + lifecycle
}

func claudeEventMetadata(lifecycle string) map[string]string {
	return map[string]string{
		"runtime":   "claude-code",
		"transport": "stream-json",
		"coverage":  "native_stream",
		"lifecycle": lifecycle,
	}
}

func toolResultErr(isErr bool) error {
	if isErr {
		return fmt.Errorf("tool reported error")
	}
	return nil
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

func withObservationTelemetry(env []string, telemetry core.ObservationChildTelemetry) []string {
	if telemetry.Endpoint == "" || telemetry.Token == "" {
		return env
	}
	content := "0"
	if telemetry.CaptureContent {
		content = "1"
	}
	resource := []string{
		"service.namespace=agentmux", "agentmux.runtime=claude",
		"agentmux.parent_trace_id=" + telemetry.TraceID,
		"agentmux.parent_span_id=" + telemetry.ParentSpanID,
		"agentmux.turn_id=" + telemetry.TurnID,
		"agentmux.session_id=" + telemetry.SessionID,
		"agentmux.agent_id=" + telemetry.AgentID,
	}
	overrides := map[string]string{
		"CLAUDE_CODE_ENABLE_TELEMETRY":        "1",
		"CLAUDE_CODE_ENHANCED_TELEMETRY_BETA": "1",
		"OTEL_TRACES_EXPORTER":                "otlp",
		"OTEL_LOGS_EXPORTER":                  "none",
		"OTEL_METRICS_EXPORTER":               "none",
		"OTEL_EXPORTER_OTLP_PROTOCOL":         "http/json",
		"OTEL_EXPORTER_OTLP_ENDPOINT":         strings.TrimRight(telemetry.Endpoint, "/"),
		"OTEL_EXPORTER_OTLP_HEADERS":          "Authorization=Bearer " + telemetry.Token,
		"OTEL_TRACES_EXPORT_INTERVAL":         "1000",
		"OTEL_RESOURCE_ATTRIBUTES":            strings.Join(resource, ","),
		"OTEL_LOG_USER_PROMPTS":               content,
		"OTEL_LOG_ASSISTANT_RESPONSES":        content,
		"OTEL_LOG_TOOL_DETAILS":               content,
		"OTEL_LOG_TOOL_CONTENT":               content,
		// Raw API bodies contain the full conversation. Public prompt/response
		// and tool content are already available through the narrower gates.
		"OTEL_LOG_RAW_API_BODIES": "0",
	}
	return runner.OverrideEnv(env, overrides)
}

// toolSummaryMax bounds the length of tool input/result summaries so a single
// noisy tool call cannot blow up the card.
const toolSummaryMax = 120

// summarizeToolInput renders a tool's JSON input into a short one-line summary,
// preferring the common "command"/"query"/"path"/"file_path" keys and falling
// back to the compact JSON.
func summarizeToolInput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return clip(strings.TrimSpace(string(raw)), toolSummaryMax)
	}
	for _, k := range []string{"command", "query", "prompt", "file_path", "path", "pattern", "url"} {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return clip(strings.TrimSpace(v), toolSummaryMax)
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return clip(string(b), toolSummaryMax)
}

// summarizeToolResult renders a tool_result content payload (either a JSON
// string or an array of {type,text} parts) into a short one-line summary.
func summarizeToolResult(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return clip(collapseWhitespace(s), toolSummaryMax)
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var texts []string
		for _, p := range parts {
			if p.Text != "" {
				texts = append(texts, p.Text)
			}
		}
		if len(texts) > 0 {
			return clip(collapseWhitespace(strings.Join(texts, " ")), toolSummaryMax)
		}
	}
	return clip(collapseWhitespace(string(raw)), toolSummaryMax)
}

func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
