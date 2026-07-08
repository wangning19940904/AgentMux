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

// EventSink receives every lifecycle event the Engine emits, in addition to
// the config.toml HookRunner. The connect runtime uses it to fire
// store-managed event triggers.
type EventSink func(event HookEvent, data map[string]string)

// Engine is the central orchestrator. It wires platforms to agents and routes
// inbound messages to agent sessions, streaming responses back. Besides
// config.toml projects it hosts dynamically attached console-managed channels.
type Engine struct {
	log      *slog.Logger
	hooks    *HookRunner
	sink     EventSink
	mu       sync.RWMutex
	projects map[string]*projectRuntime
	channels map[string]*channelRuntime
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
		channels: map[string]*channelRuntime{},
		inbound:  make(chan *Message, 256),
	}
}

// SetEventSink attaches the unified event callback. Must be called before
// Start.
func (e *Engine) SetEventSink(sink EventSink) { e.sink = sink }

// emit dispatches a lifecycle event to config.toml hooks and the event sink.
// It copies data per consumer so the caller's map is never mutated and async
// sinks cannot observe a later event's fields (both share nothing).
func (e *Engine) emit(ctx context.Context, event HookEvent, data map[string]string) {
	payload := make(map[string]string, len(data)+1)
	for k, v := range data {
		payload[k] = v
	}
	payload["event"] = string(event)
	e.hooks.Fire(ctx, event, payload)
	if e.sink != nil {
		sinkCopy := make(map[string]string, len(payload))
		for k, v := range payload {
			sinkCopy[k] = v
		}
		e.sink(event, sinkCopy)
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

// eventData builds the hook/event payload for a message.
func eventData(msg *Message) map[string]string {
	return map[string]string{
		"text":       msg.Text,
		"platform":   msg.Platform,
		"project":    msg.Project,
		"channel_id": msg.ChannelID,
		"chat_id":    msg.ChatID,
		"user_id":    msg.UserID,
		"user_name":  msg.UserName,
		"origin":     msg.Origin,
	}
}

func withError(data map[string]string, err error) map[string]string {
	out := map[string]string{}
	for k, v := range data {
		out[k] = v
	}
	out["error"] = errString(err)
	return out
}

// handle routes a single inbound message to its runtime's agent session.
func (e *Engine) handle(ctx context.Context, msg *Message) {
	if msg.ChannelID != "" && e.duplicateChannelMessage(msg) {
		e.log.Info("duplicate channel message ignored", "channel_id", msg.ChannelID, "platform", msg.Platform, "message_id", msg.ID)
		return
	}

	data := eventData(msg)
	e.emit(ctx, HookMessageReceived, data)

	if msg.ChannelID != "" {
		e.handleChannelMessage(ctx, msg, data)
		return
	}

	e.mu.RLock()
	pr := e.projects[msg.Project]
	e.mu.RUnlock()
	if pr == nil {
		e.log.Warn("no project for message", "project", msg.Project)
		return
	}

	sess, created, err := pr.session(ctx, msg.ChatID)
	if err != nil {
		e.log.Error("start session", "err", err)
		e.emit(ctx, HookError, withError(data, err))
		e.replyAll(ctx, pr, msg, "failed to start agent session: "+err.Error())
		return
	}
	if created {
		e.emit(ctx, HookSessionStarted, data)
	}

	_, _ = e.streamTurn(ctx, sess, msg.Text, func(text string) {
		e.replyAll(ctx, pr, msg, text)
	}, data)
	e.emit(ctx, HookMessageSent, data)
}

// streamTurn submits text to a session and forwards output through reply
// (when non-nil). It returns the last answer text and the first error event.
func (e *Engine) streamTurn(ctx context.Context, sess AgentSession, text string, reply func(string), data map[string]string) (string, error) {
	events, err := sess.Send(ctx, text)
	if err != nil {
		e.log.Error("send to session", "err", err)
		e.emit(ctx, HookError, withError(data, err))
		if reply != nil {
			reply("failed: " + err.Error())
		}
		return "", err
	}

	return e.consumeTurn(ctx, events, reply, data)
}

// consumeTurn drains a session event channel, forwarding textual output through
// reply (deduplicated) and surfacing errors. It returns the last answer text
// and the first error event.
func (e *Engine) consumeTurn(ctx context.Context, events <-chan *Event, reply func(string), data map[string]string) (string, error) {
	var lastText string
	var lastReply string
	var firstErr error
	for ev := range events {
		switch ev.Type {
		case EventFinal, EventOutput:
			if ev.Text != "" && ev.Text != "NO_REPLY" {
				lastText = ev.Text
				if reply != nil && ev.Text != lastReply {
					reply(ev.Text)
					lastReply = ev.Text
				}
			}
		case EventError:
			e.emit(ctx, HookError, withError(data, ev.Err))
			if firstErr == nil {
				firstErr = fmt.Errorf("%s", errString(ev.Err))
			}
			if reply != nil {
				reply("error: " + errString(ev.Err))
			}
		}
	}
	return lastText, firstErr
}

// streamTurnCard drives a single turn onto a StreamReplier, rendering the whole
// answer as one in-place updating message (a Feishu card). It updates the same
// message as the agent streams output and marks it done/failed at the end. On
// any streaming setup failure it degrades to a single final update.
func (e *Engine) streamTurnCard(ctx context.Context, sr StreamReplier, sess AgentSession, msg *Message, data map[string]string) {
	events, err := sess.Send(ctx, msg.Text)
	if err != nil {
		e.log.Error("send to session", "err", err)
		e.emit(ctx, HookError, withError(data, err))
		e.emitCardOnce(ctx, sr, msg, "failed: "+err.Error(), true)
		return
	}

	stream, err := sr.BeginReply(ctx, msg)
	if err != nil {
		e.log.Error("begin streaming reply", "err", err)
		// Degrade to per-event replies using the platform's Reply (the
		// StreamReplier is always also a Platform).
		var reply func(string)
		if p, ok := sr.(Platform); ok {
			reply = func(text string) {
				if rerr := p.Reply(ctx, msg, text); rerr != nil {
					e.log.Error("channel reply", "err", rerr)
				}
			}
		}
		e.consumeTurn(ctx, events, reply, data)
		return
	}
	defer func() { _ = stream.Close(ctx) }()

	var lastText, rendered string
	var failed bool
	for ev := range events {
		switch ev.Type {
		case EventFinal, EventOutput:
			if ev.Text == "" || ev.Text == "NO_REPLY" {
				continue
			}
			lastText = ev.Text
			if ev.Text != rendered {
				if err := stream.Update(ctx, ev.Text, false, false); err != nil {
					e.log.Error("stream update", "err", err)
				}
				rendered = ev.Text
			}
		case EventError:
			e.emit(ctx, HookError, withError(data, ev.Err))
			failed = true
			lastText = "error: " + errString(ev.Err)
		}
	}

	if lastText == "" {
		lastText = "(no reply)"
	}
	if err := stream.Update(ctx, lastText, true, failed); err != nil {
		e.log.Error("stream finalize", "err", err)
	}
}

// emitCardOnce sends a single terminal streaming message, used when the turn
// fails before any events could be produced.
func (e *Engine) emitCardOnce(ctx context.Context, sr StreamReplier, msg *Message, text string, failed bool) {
	stream, err := sr.BeginReply(ctx, msg)
	if err != nil {
		e.log.Error("begin streaming reply", "err", err)
		return
	}
	defer func() { _ = stream.Close(ctx) }()
	if err := stream.Update(ctx, text, true, failed); err != nil {
		e.log.Error("stream finalize", "err", err)
	}
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

func (pr *projectRuntime) session(ctx context.Context, chatID string) (AgentSession, bool, error) {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	if s, ok := pr.sessions[chatID]; ok {
		return s, false, nil
	}
	if pr.agent == nil {
		return nil, false, fmt.Errorf("project %q has no agent", pr.name)
	}
	s, err := pr.agent.StartSession(ctx, pr.workDir)
	if err != nil {
		return nil, false, err
	}
	pr.sessions[chatID] = s
	return s, true, nil
}

func (e *Engine) shutdown() error {
	e.mu.Lock()
	channels := e.channels
	e.channels = map[string]*channelRuntime{}
	e.mu.Unlock()
	ctx := context.Background()
	for _, rt := range channels {
		rt.close(ctx)
	}

	e.mu.RLock()
	defer e.mu.RUnlock()
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
