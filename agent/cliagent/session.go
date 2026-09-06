package cliagent

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"github.com/wangning19940904/AgentMux/agent/internal/runner"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/internal/procutil"
)

const (
	stderrTailLimit = 16 * 1024
	stdoutScanLimit = 16 * 1024 * 1024
)

type session struct {
	runner.Settings
	agent             *Agent
	workDir           string
	id                string
	modelCapabilities map[string]ModelRuntimeCapabilities
	nativeSessionMu   sync.Mutex
	nativeSessionID   string
}

func (s *session) ID() string { return s.id }

// RuntimeSettingsView returns the effective settings and controls for an
// arbitrary settings snapshot. It lets the shared picker correctly render
// both the current-conversation scope and persisted Agent defaults.
func (s *session) RuntimeSettingsView(settings core.RuntimeSettings) (core.RuntimeSettings, core.RuntimeSettingsCapabilities) {
	if s == nil {
		return settings, core.RuntimeSettingsCapabilities{}
	}
	capabilities := s.Settings.RuntimeSettingsCapabilities()
	if modelCapabilities, ok := s.modelCapabilities[settings.Model]; ok {
		capabilities.ReasoningEfforts = append([]core.RuntimeOption(nil), modelCapabilities.ReasoningEfforts...)
		capabilities.ServiceTiers = append([]core.RuntimeOption(nil), modelCapabilities.ServiceTiers...)
		if !runtimeOptionAvailable(capabilities.ReasoningEfforts, settings.ReasoningEffort) {
			settings.ReasoningEffort = ""
		}
		if !runtimeOptionAvailable(capabilities.ServiceTiers, settings.ServiceTier) {
			settings.ServiceTier = ""
		}
	}
	if s.agent == nil || s.agent.spec.EmbeddedModelSettings == nil {
		return settings, capabilities
	}
	embedded := s.agent.spec.EmbeddedModelSettings(settings.Model)
	if embedded.ReasoningEffort != "" {
		settings.ReasoningEffort = embedded.ReasoningEffort
		capabilities.ReasoningEfforts = nil
	}
	if embedded.ServiceTier != "" {
		settings.ServiceTier = embedded.ServiceTier
		capabilities.ServiceTiers = nil
	}
	return settings, capabilities
}

func runtimeOptionAvailable(options []core.RuntimeOption, value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	for _, option := range options {
		if option.Value == value {
			return true
		}
	}
	return false
}

func (s *session) RuntimeSettingsCapabilities() core.RuntimeSettingsCapabilities {
	_, capabilities := s.RuntimeSettingsView(s.Settings.CurrentRuntimeSettings())
	return capabilities
}

func (s *session) CurrentRuntimeSettings() core.RuntimeSettings {
	settings, _ := s.RuntimeSettingsView(s.Settings.CurrentRuntimeSettings())
	return settings
}

func (s *session) DefaultRuntimeSettings() core.RuntimeSettings {
	settings, _ := s.RuntimeSettingsView(s.Settings.DefaultRuntimeSettings())
	return settings
}

func (s *session) SetRuntimeSetting(setting core.RuntimeSetting, value string) error {
	if setting != core.RuntimeSettingModel {
		if err := core.ValidateRuntimeSetting(s.RuntimeSettingsCapabilities(), setting, value); err != nil {
			return err
		}
	}
	return s.Settings.SetRuntimeSetting(setting, value)
}

func (s *session) ResetRuntimeSetting(setting core.RuntimeSetting) error {
	if setting != core.RuntimeSettingModel && !s.RuntimeSettingsCapabilities().Supports(setting) {
		return fmt.Errorf("%s is fixed by the selected model", setting)
	}
	return s.Settings.ResetRuntimeSetting(setting)
}

func (s *session) ModelSwitchingSupported() bool {
	return len(s.RuntimeSettingsCapabilities().Models) > 0
}

func (s *session) CurrentModel() string { return s.CurrentRuntimeSettings().Model }

func (s *session) DefaultModel() string { return s.DefaultRuntimeSettings().Model }

func (s *session) SupportedModels() []string {
	options := s.Settings.RuntimeSettingsCapabilities().Models
	models := make([]string, 0, len(options))
	for _, option := range options {
		models = append(models, option.Value)
	}
	return models
}

func (s *session) SetModel(model string) error {
	return s.SetRuntimeSetting(core.RuntimeSettingModel, model)
}

func (s *session) ResetModel() error { return s.ResetRuntimeSetting(core.RuntimeSettingModel) }

func (s *session) Send(ctx context.Context, text string) (<-chan *core.Event, error) {
	if s.agent.spec.EnsureAuth != nil {
		if err := s.agent.spec.EnsureAuth(ctx, s.agent.env); err != nil {
			return nil, err
		}
	}
	out := make(chan *core.Event, 16)
	settings := s.CurrentRuntimeSettings()
	model := settings.Model
	if catalogModel, managed, err := s.catalogModelForSettings(settings); managed {
		if err != nil {
			return nil, err
		}
		model = catalogModel
	} else if s.agent.spec.ModelForSettings != nil {
		model = s.agent.spec.ModelForSettings(settings)
	}
	args := s.agent.spec.Args(text, s.agent.systemPrompt, model, settings.ApprovalMode)
	if nativeSessionID := s.currentNativeSessionID(); nativeSessionID != "" && s.agent.spec.ResumeArgs != nil {
		args = s.agent.spec.ResumeArgs(nativeSessionID, text, s.agent.systemPrompt, model, settings.ApprovalMode)
	}

	bin := s.agent.spec.Binary
	if p, err := exec.LookPath(bin); err == nil {
		bin = p
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	procutil.Prepare(cmd)
	cmd.Dir = s.workDir
	env := runner.BuildEnv(s.agent.env)
	if s.agent.spec.ApprovalEnv != nil {
		env = runner.OverrideEnv(env, s.agent.spec.ApprovalEnv(settings.ApprovalMode))
	}
	cmd.Env = runner.WithTraceparent(env, core.ObservationTraceparent(ctx))
	stderr := &tailBuffer{limit: stderrTailLimit}

	var stateMu sync.Mutex
	var last string
	var sawFinal bool
	var sawError bool
	emit := func(ev *core.Event) {
		if ev == nil {
			return
		}
		stateMu.Lock()
		if ev.Text != "" {
			last = ev.Text
		}
		if ev.Type == core.EventFinal {
			sawFinal = true
		}
		if ev.Type == core.EventError {
			sawError = true
		}
		stateMu.Unlock()
		out <- ev
	}
	var stderrStream *mappedLineWriter
	if s.agent.spec.NewStderrMapper != nil {
		stderrStream = &mappedLineWriter{tail: stderr, mapper: s.agent.spec.NewStderrMapper(), emit: emit}
		cmd.Stderr = stderrStream
	} else {
		cmd.Stderr = stderr
	}

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
		sc.Buffer(make([]byte, 0, 1024*1024), stdoutScanLimit)
		for sc.Scan() {
			line := sc.Bytes()
			if s.agent.spec.SessionIDFromLine != nil {
				s.rememberNativeSessionID(s.agent.spec.SessionIDFromLine(line))
			}
			for _, ev := range s.mapOutputLine(line) {
				emit(ev)
			}
		}
		scanErr := sc.Err()
		if scanErr != nil {
			// Stop stdout backpressure before Wait. Without this close, a child
			// that keeps writing after Scanner rejects an oversized frame can
			// deadlock with the parent waiting for it to exit.
			_ = stdout.Close()
		}
		waitErr := cmd.Wait()
		if stderrStream != nil {
			stderrStream.Flush()
		}
		stateMu.Lock()
		finalText, finalSeen, errorSeen := last, sawFinal, sawError
		stateMu.Unlock()
		if scanErr != nil {
			err := fmt.Errorf("read %s output: %w", s.agent.spec.Name, scanErr)
			if waitErr != nil {
				err = fmt.Errorf("%v (%w)", err, waitErr)
			}
			out <- &core.Event{
				Type: core.EventError, Err: processError(err, stderr.String()),
				Metadata: s.processMetadata("failed"),
			}
			return
		}
		if waitErr != nil {
			// A structured CLI error already contains the useful explanation.
			// Do not follow it with a second, generic exit-status event.
			if !errorSeen {
				out <- &core.Event{
					Type: core.EventError, Err: processError(waitErr, stderr.String()),
					Metadata: s.processMetadata("failed"),
				}
			}
			return
		}
		if s.agent.spec.FinalFromLast && !finalSeen {
			out <- &core.Event{
				Type: core.EventFinal, Text: finalText, Final: true,
				Metadata: s.processMetadata("completed"),
			}
		}
	}()
	return out, nil
}

func (s *session) catalogModelForSettings(settings core.RuntimeSettings) (string, bool, error) {
	modelCapabilities, managed := s.modelCapabilities[settings.Model]
	if !managed || len(modelCapabilities.Variants) == 0 {
		return "", false, nil
	}
	effort := strings.TrimSpace(settings.ReasoningEffort)
	tier := normalizeCatalogServiceTier(settings.ServiceTier)

	bestID := ""
	bestScore := -1
	for _, variant := range modelCapabilities.Variants {
		if effort != "" && variant.ReasoningEffort != effort {
			continue
		}
		if tier != "" && variant.ServiceTier != tier {
			continue
		}
		score := 0
		if variant.ReasoningEffort == effort {
			score += 4
		}
		if variant.ServiceTier == tier {
			score += 2
		}
		if tier == "" && variant.ServiceTier == "default" {
			score++
		}
		if score > bestScore {
			bestID, bestScore = variant.ID, score
		}
	}
	if bestID != "" {
		return bestID, true, nil
	}
	return "", true, fmt.Errorf(
		"model %q has no Cursor variant for effort=%q and speed=%q",
		settings.Model, effort, tier,
	)
}

func normalizeCatalogServiceTier(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "priority", "fast":
		return "priority"
	case "default", "normal", "standard", "flex":
		return "default"
	default:
		return strings.TrimSpace(value)
	}
}

func (s *session) currentNativeSessionID() string {
	s.nativeSessionMu.Lock()
	defer s.nativeSessionMu.Unlock()
	return s.nativeSessionID
}

func (s *session) rememberNativeSessionID(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	s.nativeSessionMu.Lock()
	s.nativeSessionID = sessionID
	s.nativeSessionMu.Unlock()
}

// processMetadata keeps events synthesized by the subprocess runner
// attributable to the concrete runtime. Native stream mappers already attach
// this information, but scanner failures, exit statuses, and fallback finals
// are created here after mapping has finished.
func (s *session) processMetadata(lifecycle string) map[string]string {
	metadata := map[string]string{"transport": "process", "lifecycle": lifecycle}
	if runtime := strings.TrimSpace(s.agent.spec.Name); runtime != "" {
		metadata["runtime"] = runtime
	}
	return metadata
}

func (s *session) mapOutputLine(line []byte) []*core.Event {
	if s.agent.spec.EventMapper != nil {
		return s.agent.spec.EventMapper(line)
	}
	if s.agent.spec.Mapper == nil {
		return nil
	}
	if event := s.agent.spec.Mapper(line); event != nil {
		return []*core.Event{event}
	}
	return nil
}

func processError(err error, stderr string) error {
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		return err
	}
	return fmt.Errorf("%s (%w)", detail, err)
}

// tailBuffer continuously drains stderr while retaining only the most recent
// bytes, where command failures normally put their actionable explanation.
// Write always reports the full input length so os/exec keeps draining.
type tailBuffer struct {
	limit int
	buf   []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.limit <= 0 || n == 0 {
		return n, nil
	}
	if n >= b.limit {
		b.buf = append(b.buf[:0], p[n-b.limit:]...)
		return n, nil
	}
	if overflow := len(b.buf) + n - b.limit; overflow > 0 {
		copy(b.buf, b.buf[overflow:])
		b.buf = b.buf[:len(b.buf)-overflow]
	}
	b.buf = append(b.buf, p...)
	return n, nil
}

func (b *tailBuffer) String() string { return string(b.buf) }

// mappedLineWriter keeps the bounded stderr tail used in process errors while
// also forwarding complete lines to a per-turn mapper as soon as they arrive.
// os/exec serializes writes to this writer, so mapper state needs no additional
// synchronization.
type mappedLineWriter struct {
	tail    *tailBuffer
	mapper  LineMapper
	emit    func(*core.Event)
	pending []byte
}

func (w *mappedLineWriter) Write(p []byte) (int, error) {
	n := len(p)
	if w.tail != nil {
		_, _ = w.tail.Write(p)
	}
	w.pending = append(w.pending, p...)
	for {
		newline := bytes.IndexByte(w.pending, '\n')
		if newline < 0 {
			break
		}
		w.mapLine(w.pending[:newline])
		w.pending = w.pending[newline+1:]
	}
	// A misbehaving CLI must not turn a newline-free diagnostic into an
	// unbounded in-memory buffer. Keep the same useful tail as processError.
	if len(w.pending) > stderrTailLimit {
		w.pending = append(w.pending[:0], w.pending[len(w.pending)-stderrTailLimit:]...)
	}
	return n, nil
}

func (w *mappedLineWriter) Flush() {
	if len(w.pending) == 0 {
		return
	}
	w.mapLine(w.pending)
	w.pending = nil
}

func (w *mappedLineWriter) mapLine(line []byte) {
	line = bytes.TrimSuffix(line, []byte{'\r'})
	if len(line) > stderrTailLimit {
		line = line[len(line)-stderrTailLimit:]
	}
	if w.mapper == nil || w.emit == nil {
		return
	}
	w.emit(w.mapper(line))
}

func (s *session) RespondPermission(ctx context.Context, allow bool) error { return nil }
func (s *session) Close(ctx context.Context) error                         { return nil }

// PlainTextMapper treats every output line as plain assistant text.
func PlainTextMapper(line []byte) *core.Event {
	t := strings.TrimRight(string(line), "\r\n")
	if t == "" {
		return nil
	}
	return &core.Event{Type: core.EventOutput, Text: t}
}

func (s *session) NativeSessionID() string { return s.currentNativeSessionID() }
