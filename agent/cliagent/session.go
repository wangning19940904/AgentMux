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
		var last string
		var sawFinal bool
		for sc.Scan() {
			line := sc.Bytes()
			if ev := s.agent.spec.Mapper(line); ev != nil {
				if ev.Text != "" {
					last = ev.Text
				}
				if ev.Type == core.EventFinal {
					sawFinal = true
				}
				out <- ev
			}
		}
		if err := cmd.Wait(); err != nil {
			out <- &core.Event{Type: core.EventError, Err: err}
			return
		}
		if s.agent.spec.FinalFromLast && !sawFinal {
			out <- &core.Event{Type: core.EventFinal, Text: last, Final: true}
		}
	}()
	return out, nil
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
