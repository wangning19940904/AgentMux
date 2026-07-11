package cliagent

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/agentnexus/agentnexus/core"
)

const (
	stderrTailLimit = 16 * 1024
	stdoutScanLimit = 16 * 1024 * 1024
)

type session struct {
	agent   *Agent
	workDir string
	id      string
	model   *core.ModelSelection
}

func (s *session) ID() string { return s.id }

func (s *session) ModelSwitchingSupported() bool { return s.model != nil }
func (s *session) CurrentModel() string          { return s.model.CurrentModel() }
func (s *session) DefaultModel() string          { return s.model.DefaultModel() }
func (s *session) SupportedModels() []string     { return s.model.SupportedModels() }
func (s *session) SetModel(model string) error   { return s.model.SetModel(model) }
func (s *session) ResetModel() error             { return s.model.ResetModel() }

func (s *session) Send(ctx context.Context, text string) (<-chan *core.Event, error) {
	out := make(chan *core.Event, 16)
	args := s.agent.spec.Args(text, s.agent.systemPrompt, s.CurrentModel())

	bin := s.agent.spec.Binary
	if p, err := exec.LookPath(bin); err == nil {
		bin = p
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = s.workDir
	cmd.Env = withTraceparent(buildEnv(s.agent.env), core.ObservationTraceparent(ctx))
	stderr := &tailBuffer{limit: stderrTailLimit}
	cmd.Stderr = stderr

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
		var last string
		var sawFinal bool
		var sawError bool
		for sc.Scan() {
			line := sc.Bytes()
			if ev := s.agent.spec.Mapper(line); ev != nil {
				if ev.Text != "" {
					last = ev.Text
				}
				if ev.Type == core.EventFinal {
					sawFinal = true
				}
				if ev.Type == core.EventError {
					sawError = true
				}
				out <- ev
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
		if scanErr != nil {
			err := fmt.Errorf("read %s output: %w", s.agent.spec.Name, scanErr)
			if waitErr != nil {
				err = fmt.Errorf("%v (%w)", err, waitErr)
			}
			out <- &core.Event{Type: core.EventError, Err: processError(err, stderr.String())}
			return
		}
		if waitErr != nil {
			// A structured CLI error already contains the useful explanation.
			// Do not follow it with a second, generic exit-status event.
			if !sawError {
				out <- &core.Event{Type: core.EventError, Err: processError(waitErr, stderr.String())}
			}
			return
		}
		if s.agent.spec.FinalFromLast && !sawFinal {
			out <- &core.Event{Type: core.EventFinal, Text: last, Final: true}
		}
	}()
	return out, nil
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

func (s *session) RespondPermission(ctx context.Context, allow bool) error { return nil }
func (s *session) Close(ctx context.Context) error                         { return nil }

func buildEnv(extra map[string]string) []string {
	env := os.Environ()
	for k, v := range extra {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	return env
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
