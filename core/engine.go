package core

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// projectRuntime holds the live platform/agent instances for one project.
type projectRuntime struct {
	name      string
	agent     Agent
	platforms []Platform
	workDir   string

	mu       sync.Mutex
	sessions map[string]AgentSession // chatID -> session
}

// Engine is the central orchestrator. It wires platforms to agents and routes
// inbound messages to agent sessions, streaming responses back.
type Engine struct {
	log      *slog.Logger
	hooks    *HookRunner
	mu       sync.RWMutex
	projects map[string]*projectRuntime
	inbound  chan *Message
}

// NewEngine constructs an Engine.
func NewEngine(log *slog.Logger, hooks *HookRunner) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{
		log:      log,
		hooks:    hooks,
		projects: map[string]*projectRuntime{},
		inbound:  make(chan *Message, 256),
	}
}

// AddProject registers a project's agent and platforms with the engine.
func (e *Engine) AddProject(name, workDir string, agent Agent, platforms []Platform) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.projects[name] = &projectRuntime{
		name:      name,
		agent:     agent,
		platforms: platforms,
		workDir:   workDir,
		sessions:  map[string]AgentSession{},
	}
}

// Start begins all platforms and the routing loop. It blocks until ctx is
// cancelled.
func (e *Engine) Start(ctx context.Context) error {
	e.mu.RLock()
	for _, pr := range e.projects {
		for _, p := range pr.platforms {
			p := p
			go func() {
				if err := p.Start(ctx, e.inbound); err != nil {
					e.log.Error("platform stopped", "platform", p.Name(), "err", err)
				}
			}()
		}
	}
	e.mu.RUnlock()

	e.log.Info("engine started", "projects", len(e.projects))
	for {
		select {
		case <-ctx.Done():
			return e.shutdown()
		case msg := <-e.inbound:
			go e.handle(ctx, msg)
		}
	}
}

// handle routes a single inbound message to its project's agent session.
func (e *Engine) handle(ctx context.Context, msg *Message) {
	e.hooks.Fire(ctx, HookMessageReceived, map[string]string{"text": msg.Text})

	e.mu.RLock()
	pr := e.projects[msg.Project]
	e.mu.RUnlock()
	if pr == nil {
		e.log.Warn("no project for message", "project", msg.Project)
		return
	}

	sess, err := pr.session(ctx, msg.ChatID)
	if err != nil {
		e.log.Error("start session", "err", err)
		e.replyAll(ctx, pr, msg, "failed to start agent session: "+err.Error())
		return
	}

	events, err := sess.Send(ctx, msg.Text)
	if err != nil {
		e.log.Error("send to session", "err", err)
		e.replyAll(ctx, pr, msg, "failed: "+err.Error())
		return
	}

	for ev := range events {
		switch ev.Type {
		case EventFinal, EventOutput:
			if ev.Text != "" && ev.Text != "NO_REPLY" {
				e.replyAll(ctx, pr, msg, ev.Text)
			}
		case EventError:
			e.hooks.Fire(ctx, HookError, map[string]string{"error": errString(ev.Err)})
			e.replyAll(ctx, pr, msg, "error: "+errString(ev.Err))
		}
	}
	e.hooks.Fire(ctx, HookMessageSent, nil)
}

func (e *Engine) replyAll(ctx context.Context, pr *projectRuntime, msg *Message, text string) {
	for _, p := range pr.platforms {
		if p.Name() == msg.Platform {
			if err := p.Reply(ctx, msg, text); err != nil {
				e.log.Error("reply", "platform", p.Name(), "err", err)
			}
			return
		}
	}
}

func (pr *projectRuntime) session(ctx context.Context, chatID string) (AgentSession, error) {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	if s, ok := pr.sessions[chatID]; ok {
		return s, nil
	}
	if pr.agent == nil {
		return nil, fmt.Errorf("project %q has no agent", pr.name)
	}
	s, err := pr.agent.StartSession(ctx, pr.workDir)
	if err != nil {
		return nil, err
	}
	pr.sessions[chatID] = s
	return s, nil
}

func (e *Engine) shutdown() error {
	e.mu.RLock()
	defer e.mu.RUnlock()
	ctx := context.Background()
	for _, pr := range e.projects {
		pr.mu.Lock()
		for _, s := range pr.sessions {
			_ = s.Close(ctx)
		}
		pr.mu.Unlock()
		if pr.agent != nil {
			_ = pr.agent.Stop(ctx)
		}
		for _, p := range pr.platforms {
			_ = p.Stop(ctx)
		}
	}
	e.log.Info("engine stopped")
	return nil
}

func errString(err error) string {
	if err == nil {
		return "unknown"
	}
	return err.Error()
}
