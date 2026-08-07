package cliagent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/wangning19940904/AgentMux/core"
)

const (
	stderrTailLimit = 16 * 1024
	stdoutScanLimit = 16 * 1024 * 1024
)

type session struct {
	agent    *Agent
	workDir  string
	id       string
	settings *core.RuntimeSettingsSelection
}

func (s *session) ID() string { return s.id }

func (s *session) RuntimeSettingsCapabilities() core.RuntimeSettingsCapabilities {
	return s.settings.RuntimeSettingsCapabilities()
}
func (s *session) CurrentRuntimeSettings() core.RuntimeSettings {
	return s.settings.CurrentRuntimeSettings()
}
func (s *session) DefaultRuntimeSettings() core.RuntimeSettings {
	return s.settings.DefaultRuntimeSettings()
}
func (s *session) SetRuntimeSetting(setting core.RuntimeSetting, value string) error {
	return s.settings.SetRuntimeSetting(setting, value)
}
func (s *session) ResetRuntimeSetting(setting core.RuntimeSetting) error {
	return s.settings.ResetRuntimeSetting(setting)
}
func (s *session) ModelSwitchingSupported() bool {
	return len(s.RuntimeSettingsCapabilities().Models) > 0
}
func (s *session) CurrentModel() string { return s.CurrentRuntimeSettings().Model }
func (s *session) DefaultModel() string { return s.DefaultRuntimeSettings().Model }
func (s *session) SupportedModels() []string {
	options := s.RuntimeSettingsCapabilities().Models
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
	out := make(chan *core.Event, 16)
	settings := s.CurrentRuntimeSettings()
	model := settings.Model
	if s.agent.spec.ModelForSettings != nil {
		model = s.agent.spec.ModelForSettings(settings)
	}
	args := s.agent.spec.Args(text, s.agent.systemPrompt, model, settings.ApprovalMode)

	bin := s.agent.spec.Binary
	if p, err := exec.LookPath(bin); err == nil {
		bin = p
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = s.workDir
	env := buildEnv(s.agent.env)
	if s.agent.spec.ApprovalEnv != nil {
		env = overrideEnv(env, s.agent.spec.ApprovalEnv(settings.ApprovalMode))
	}
	cmd.Env = withTraceparent(env, core.ObservationTraceparent(ctx))
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
			for _, ev := range s.mapOutputLine(sc.Bytes()) {
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

func buildEnv(extra map[string]string) []string {
	env := os.Environ()
	for k, v := range extra {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	return env
}

func overrideEnv(env []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return env
	}
	filtered := make([]string, 0, len(env)+len(overrides))
	for _, value := range env {
		key, _, ok := strings.Cut(value, "=")
		if ok {
			if _, replaced := overrides[key]; replaced {
				continue
			}
		}
		filtered = append(filtered, value)
	}
	for key, value := range overrides {
		filtered = append(filtered, key+"="+value)
	}
	return filtered
}

func withTraceparent(env []string, traceparent string) []string {
	if traceparent == "" {
		return env
	}
	filtered := make([]string, 0, len(env)+1)
	for _, value := range env {
		if strings.HasPrefix(value, "TRACEPARENT=") {
			continue
		}
		filtered = append(filtered, value)
	}
	return append(filtered, "TRACEPARENT="+traceparent)
}

func randID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// PlainTextMapper treats every output line as plain assistant text.
func PlainTextMapper(line []byte) *core.Event {
	t := strings.TrimRight(string(line), "\r\n")
	if t == "" {
		return nil
	}
	return &core.Event{Type: core.EventOutput, Text: t}
}
