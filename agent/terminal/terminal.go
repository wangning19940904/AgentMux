// Package terminal implements the optional persistent interactive CLI runtime.
// It keeps the real coding CLI inside tmux while presenting the same
// core.AgentSession contract used by AgentMux's structured adapters.
package terminal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/agent/internal/runner"
	"github.com/wangning19940904/AgentMux/core"
)

const registeredName = "terminal"

type runtimeSpec struct {
	Name   string
	Binary string
	Args   func(core.RuntimeSettings) []string
}

var runtimeSpecs = map[string]runtimeSpec{
	"claudecode": {Name: "claudecode", Binary: "claude", Args: claudeArgs},
	"codex":      {Name: "codex", Binary: "codex", Args: modelArgs("--model")},
	"cursor":     {Name: "cursor", Binary: "cursor-agent", Args: noArgs},
	"gemini":     {Name: "gemini", Binary: "gemini", Args: modelArgs("--model")},
	"qoder":      {Name: "qoder", Binary: "qodercli", Args: noArgs},
	"opencode":   {Name: "opencode", Binary: "opencode", Args: noArgs},
	"iflow":      {Name: "iflow", Binary: "iflow", Args: noArgs},
	"kimi":       {Name: "kimi", Binary: "kimi", Args: noArgs},
}

func init() {
	core.RegisterAgent(registeredName, func(cfg map[string]any) (core.Agent, error) {
		return New(cfg)
	})
}

// Agent owns configuration shared by all tmux conversations for one managed
// Agent instance. Each StartSession still gets an isolated tmux session.
type Agent struct {
	spec                     runtimeSpec
	binary                   string
	systemPrompt             string
	env                      map[string]string
	defaults                 core.RuntimeSettings
	capabilities             core.RuntimeSettingsCapabilities
	idleTimeout              time.Duration
	promptSettle             time.Duration
	pollInterval             time.Duration
	minimumCompletionLatency time.Duration
}

// New constructs a persistent terminal Agent for terminal_runtime.
func New(cfg map[string]any) (*Agent, error) {
	runtimeID, _ := cfg["terminal_runtime"].(string)
	runtimeID = normalizeRuntime(runtimeID)
	spec, ok := runtimeSpecs[runtimeID]
	if !ok {
		return nil, fmt.Errorf("terminal: runtime %q does not support the tmux backend", runtimeID)
	}
	a := &Agent{
		spec:                     spec,
		binary:                   spec.Binary,
		idleTimeout:              15 * time.Second,
		promptSettle:             1200 * time.Millisecond,
		pollInterval:             250 * time.Millisecond,
		minimumCompletionLatency: 750 * time.Millisecond,
	}
	if value, ok := cfg["terminal_binary"].(string); ok && strings.TrimSpace(value) != "" {
		a.binary = strings.TrimSpace(value)
	}
	if value, ok := cfg["system_prompt"].(string); ok {
		a.systemPrompt = strings.TrimSpace(value)
	}
	if value, ok := cfg["env"].(map[string]string); ok {
		for key, envValue := range value {
			if !validEnvironmentKey(key) {
				return nil, fmt.Errorf("terminal: invalid environment variable name %q", key)
			}
			if strings.ContainsRune(envValue, '\x00') {
				return nil, fmt.Errorf("terminal: environment variable %q contains NUL", key)
			}
		}
		a.env = cloneStrings(value)
	}
	selection := core.RuntimeSettingsSelectionFromConfig(cfg)
	if selection != nil {
		a.defaults = selection.DefaultRuntimeSettings()
		a.capabilities = selection.RuntimeSettingsCapabilities()
	}
	if value := durationMilliseconds(cfg["terminal_idle_timeout_ms"]); value > 0 {
		a.idleTimeout = value
	}
	if value := durationMilliseconds(cfg["terminal_poll_interval_ms"]); value > 0 {
		a.pollInterval = value
	}
	if value := durationMilliseconds(cfg["terminal_minimum_latency_ms"]); value > 0 {
		a.minimumCompletionLatency = value
	}
	return a, nil
}

func (a *Agent) Name() string { return a.spec.Name }

func (a *Agent) StartSession(_ context.Context, workDir string) (core.AgentSession, error) {
	return a.newSession(workDir, "", false), nil
}

func (a *Agent) StartSessionResume(ctx context.Context, workDir, resumeID string) (core.AgentSession, error) {
	resumeID = strings.TrimSpace(resumeID)
	if !validSessionName(resumeID) || !tmuxHasSession(ctx, resumeID) {
		return nil, core.ErrNativeSessionUnavailable
	}
	return a.newSession(workDir, resumeID, true), nil
}

func (a *Agent) newSession(workDir, sessionID string, started bool) *session {
	if sessionID == "" {
		sessionID = "amux-" + randomID(8)
	}
	return &session{
		Settings:   runner.NewSettings(a.defaults, a.capabilities),
		agent:      a,
		workDir:    workDir,
		id:         sessionID,
		started:    started,
		firstInput: started,
	}
}

func (a *Agent) ListSessions(ctx context.Context) ([]string, error) {
	output, err := tmuxOutput(ctx, nil, "list-sessions", "-F", "#{session_name}")
	if err != nil {
		if strings.Contains(strings.ToLower(output), "no server running") || strings.Contains(strings.ToLower(output), "failed to connect") {
			return nil, nil
		}
		return nil, fmt.Errorf("terminal: list tmux sessions: %s", output)
	}
	var sessions []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if validSessionName(line) {
			sessions = append(sessions, line)
		}
	}
	return sessions, nil
}

func (a *Agent) Stop(context.Context) error { return nil }

type session struct {
	runner.Settings
	agent   *Agent
	workDir string
	id      string

	mu         sync.Mutex
	turnMu     sync.Mutex
	started    bool
	closed     bool
	firstInput bool
	activeTurn string
}

func (s *session) ID() string              { return s.id }
func (s *session) NativeSessionID() string { return s.id }

func (s *session) Send(ctx context.Context, text string) (<-chan *core.Event, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("terminal: input is empty")
	}
	s.turnMu.Lock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.turnMu.Unlock()
		return nil, errors.New("terminal: session is closed")
	}
	if s.activeTurn != "" {
		s.mu.Unlock()
		s.turnMu.Unlock()
		return nil, errors.New("terminal: another turn is already active")
	}
	turnID := "turn-" + randomID(8)
	s.activeTurn = turnID
	s.mu.Unlock()

	if err := s.ensureStarted(ctx); err != nil {
		s.finishTurn()
		return nil, err
	}
	prompt := s.preparePrompt(text)
	if err := s.WriteTerminal(ctx, prompt, true); err != nil {
		s.finishTurn()
		return nil, err
	}

	out := make(chan *core.Event, 16)
	go s.observeTurn(ctx, turnID, out)
	return out, nil
}

func (s *session) ensureStarted(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("terminal: session is closed")
	}
	if s.started {
		if !tmuxHasSession(ctx, s.id) {
			return core.ErrNativeSessionUnavailable
		}
		return nil
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		return fmt.Errorf("terminal: tmux is required: %w", err)
	}
	if _, err := exec.LookPath(s.agent.binary); err != nil && !strings.ContainsRune(s.agent.binary, '/') {
		return fmt.Errorf("terminal: runtime binary %q is not installed", s.agent.binary)
	}
	settings := s.CurrentRuntimeSettings()
	args := []string{"new-session", "-d", "-s", s.id, "-x", "160", "-y", "50", "-c", s.workDir, "--", "env"}
	for _, key := range sortedKeys(s.agent.env) {
		args = append(args, key+"="+s.agent.env[key])
	}
	args = append(args, s.agent.binary)
	args = append(args, s.agent.spec.Args(settings)...)
	if output, err := tmuxOutput(ctx, nil, args...); err != nil {
		return fmt.Errorf("terminal: start tmux session: %s", output)
	}
	s.started = true
	return nil
}

func (s *session) preparePrompt(text string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.firstInput || s.agent.systemPrompt == "" {
		s.firstInput = true
		return text
	}
	s.firstInput = true
	return "System instructions for this conversation:\n" + s.agent.systemPrompt + "\n\nUser request:\n" + text
}

func (s *session) observeTurn(ctx context.Context, turnID string, out chan<- *core.Event) {
	defer close(out)
	defer s.finishTurn()

	ticker := time.NewTicker(s.agent.pollInterval)
	defer ticker.Stop()
	startedAt := time.Now()
	lastChanged := startedAt
	lastSnapshot := ""
	emitted := ""
	for {
		select {
		case <-ctx.Done():
			_ = s.Interrupt(context.Background())
			out <- &core.Event{Type: core.EventError, TurnID: turnID, Err: ctx.Err(), Status: "cancelled"}
			return
		case <-ticker.C:
			snapshot, err := s.TerminalSnapshot(ctx)
			if err != nil {
				out <- &core.Event{Type: core.EventError, TurnID: turnID, Err: err, Status: "failed"}
				return
			}
			if snapshot != lastSnapshot {
				lastSnapshot = snapshot
				lastChanged = time.Now()
			}
			if snapshot != "" && snapshot != emitted {
				emitted = snapshot
				out <- &core.Event{Type: core.EventOutput, TurnID: turnID, Text: snapshot, Status: "in_progress"}
			}
			stableFor := time.Since(lastChanged)
			ready := terminalLooksReady(s.agent.spec.Name, snapshot) && stableFor >= s.agent.promptSettle
			if time.Since(startedAt) >= s.agent.minimumCompletionLatency && (ready || stableFor >= s.agent.idleTimeout) {
				out <- &core.Event{Type: core.EventFinal, TurnID: turnID, Text: snapshot, Final: true, Status: "completed", DurationMs: time.Since(startedAt).Milliseconds(), Metadata: map[string]string{
					"runtime": s.agent.spec.Name, "transport": "tmux", "coverage": "terminal_snapshot",
				}}
				return
			}
		}
	}
}

func (s *session) finishTurn() {
	s.mu.Lock()
	s.activeTurn = ""
	s.mu.Unlock()
	s.turnMu.Unlock()
}

func (s *session) RespondPermission(ctx context.Context, allow bool) error {
	answer := "n"
	if allow {
		answer = "y"
	}
	return s.WriteTerminal(ctx, answer, true)
}

func (s *session) Steer(ctx context.Context, text string) error {
	return s.WriteTerminal(ctx, text, true)
}

func (s *session) Interrupt(ctx context.Context) error {
	if output, err := tmuxOutput(ctx, nil, "send-keys", "-t", "="+s.id, "C-c"); err != nil {
		return fmt.Errorf("terminal: interrupt: %s", output)
	}
	return nil
}

func (s *session) ResolveInteraction(ctx context.Context, _ string, response core.AgentInteractionResponse) error {
	answer := response.Decision
	if answer == "" {
		for _, values := range response.Answers {
			answer = strings.Join(values, ",")
			break
		}
	}
	return s.WriteTerminal(ctx, answer, true)
}

func (s *session) ActiveTurnID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeTurn
}

func (s *session) TerminalInfo() core.TerminalSessionInfo {
	return core.TerminalSessionInfo{
		Backend:       "tmux",
		SessionID:     s.id,
		AttachCommand: "tmux attach-session -t " + s.id,
		Available:     tmuxHasSession(context.Background(), s.id),
	}
}

func (s *session) TerminalSnapshot(ctx context.Context) (string, error) {
	output, err := tmuxOutput(ctx, nil, "capture-pane", "-p", "-t", "="+s.id, "-S", "-1000")
	if err != nil {
		return "", fmt.Errorf("terminal: capture pane: %s", output)
	}
	return trimTerminalSnapshot(output), nil
}

func (s *session) WriteTerminal(ctx context.Context, text string, submit bool) error {
	if text == "\x03" && !submit {
		return s.Interrupt(ctx)
	}
	if strings.ContainsRune(text, '\x00') {
		return errors.New("terminal: input contains NUL")
	}
	if !tmuxHasSession(ctx, s.id) {
		return core.ErrNativeSessionUnavailable
	}
	if text != "" {
		bufferName := s.id + "-input"
		if output, err := tmuxOutput(ctx, strings.NewReader(text), "load-buffer", "-b", bufferName, "-"); err != nil {
			return fmt.Errorf("terminal: load input buffer: %s", output)
		}
		if output, err := tmuxOutput(ctx, nil, "paste-buffer", "-b", bufferName, "-d", "-t", "="+s.id); err != nil {
			return fmt.Errorf("terminal: paste input: %s", output)
		}
	}
	if submit {
		if output, err := tmuxOutput(ctx, nil, "send-keys", "-t", "="+s.id, "Enter"); err != nil {
			return fmt.Errorf("terminal: submit input: %s", output)
		}
	}
	return nil
}

func (s *session) ResizeTerminal(ctx context.Context, columns, rows int) error {
	if columns < 20 || columns > 500 || rows < 5 || rows > 300 {
		return fmt.Errorf("terminal: invalid size %dx%d", columns, rows)
	}
	if output, err := tmuxOutput(ctx, nil, "resize-window", "-t", "="+s.id, "-x", strconv.Itoa(columns), "-y", strconv.Itoa(rows)); err != nil {
		return fmt.Errorf("terminal: resize: %s", output)
	}
	return nil
}

func (s *session) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	started := s.started
	s.mu.Unlock()
	if !started || !tmuxHasSession(ctx, s.id) {
		return nil
	}
	if output, err := tmuxOutput(ctx, nil, "kill-session", "-t", "="+s.id); err != nil {
		return fmt.Errorf("terminal: close: %s", output)
	}
	return nil
}

// Detach retires this in-process handle without killing the tmux session. A
// replacement AgentMux process can resume it from the persisted session id.
func (s *session) Detach(context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func tmuxHasSession(ctx context.Context, id string) bool {
	if !validSessionName(id) {
		return false
	}
	cmd := exec.CommandContext(ctx, "tmux", "has-session", "-t", "="+id)
	return cmd.Run() == nil
}

func tmuxOutput(ctx context.Context, stdin *strings.Reader, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "tmux", args...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	output, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func validSessionName(value string) bool {
	if !strings.HasPrefix(value, "amux-") || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if r != '-' && !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func randomID(bytes int) string {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(buf)
}

func normalizeRuntime(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "claude", "claude-code", "claudecode-cli":
		return "claudecode"
	case "codex-cli":
		return "codex"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func noArgs(core.RuntimeSettings) []string { return nil }

func modelArgs(flag string) func(core.RuntimeSettings) []string {
	return func(settings core.RuntimeSettings) []string {
		if strings.TrimSpace(settings.Model) == "" {
			return nil
		}
		return []string{flag, settings.Model}
	}
}

func claudeArgs(settings core.RuntimeSettings) []string {
	var args []string
	if settings.Model != "" {
		args = append(args, "--model", settings.Model)
	}
	if settings.ReasoningEffort != "" {
		args = append(args, "--effort", settings.ReasoningEffort)
	}
	switch settings.ApprovalMode {
	case core.ApprovalModePlan:
		args = append(args, "--permission-mode", "plan")
	case core.ApprovalModeAutoEdit:
		args = append(args, "--permission-mode", "acceptEdits")
	case core.ApprovalModeYolo:
		args = append(args, "--dangerously-skip-permissions")
	}
	return args
}

func trimTerminalSnapshot(value string) string {
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

func terminalLooksReady(runtimeID, snapshot string) bool {
	lines := strings.Split(snapshot, "\n")
	for index := len(lines) - 1; index >= 0 && index >= len(lines)-6; index-- {
		line := strings.TrimSpace(lines[index])
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		switch runtimeID {
		case "claudecode":
			return strings.HasPrefix(line, "❯") || strings.HasSuffix(line, "❯")
		case "codex":
			return strings.HasPrefix(line, "›") || strings.Contains(lower, "ask anything")
		case "gemini":
			return strings.Contains(lower, "type your message") || strings.HasPrefix(line, ">")
		default:
			return strings.Contains(lower, "type your message") || strings.Contains(lower, "ask anything")
		}
	}
	return false
}

func cloneStrings(input map[string]string) map[string]string {
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func sortedKeys(input map[string]string) []string {
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	// Small insertion sort avoids another dependency and keeps env ordering
	// deterministic for tests and reproducible process inspection.
	for index := 1; index < len(keys); index++ {
		for cursor := index; cursor > 0 && keys[cursor] < keys[cursor-1]; cursor-- {
			keys[cursor], keys[cursor-1] = keys[cursor-1], keys[cursor]
		}
	}
	return keys
}

func durationMilliseconds(value any) time.Duration {
	switch typed := value.(type) {
	case int:
		return time.Duration(typed) * time.Millisecond
	case int64:
		return time.Duration(typed) * time.Millisecond
	case float64:
		return time.Duration(typed) * time.Millisecond
	default:
		return 0
	}
}

func validEnvironmentKey(value string) bool {
	if value == "" {
		return false
	}
	for index, char := range value {
		if (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || char == '_' || (index > 0 && char >= '0' && char <= '9') {
			continue
		}
		return false
	}
	return true
}
