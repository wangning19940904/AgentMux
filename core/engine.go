package core

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// EventSink receives every lifecycle event the Engine emits. The connect runtime uses it to fire
// store-managed event triggers.
type EventSink func(event HookEvent, data map[string]string)

// Engine is the central orchestrator. It wires platforms to agents and routes
// inbound messages to agent sessions, streaming responses back. Besides
// it hosts dynamically attached PostgreSQL-managed channels.
type Engine struct {
	log                     *slog.Logger
	hooks                   *HookRunner
	sinkMu                  sync.RWMutex
	sink                    EventSink // compatibility slot used by SetEventSink
	sinks                   map[uint64]EventSink
	nextSinkID              uint64
	observationMu           sync.RWMutex
	observations            *ObservationBus
	usageSink               UsageRecordSink
	childTelemetry          ObservationChildTelemetry
	mu                      sync.RWMutex
	projects                map[string]*projectRuntime
	channels                map[string]*channelRuntime
	inbound                 chan *Message
	workspace               WorkspaceInitializer
	memory                  MemoryStore
	guard                   Guard
	conversations           ConversationStore
	channelControl          ChannelControlStore
	feedbackStore           ChannelFeedbackStore
	runtimeSettingsDefaults RuntimeSettingsDefaultStore
	meetingResponseModes    MeetingResponseModeStore
	meetingEventMu          sync.Mutex
	meetingEventSubscribers map[uint64]chan MeetingEvent
	nextMeetingEventID      uint64
	remoteWorkMu            sync.Mutex
	remoteWorkLocks         map[string]*sync.Mutex
	invocationMu            sync.Mutex
	activeInvocations       map[string]struct{}
	msgLog                  *MessageLogger
}

// NewEngine constructs an Engine.
func NewEngine(log *slog.Logger, hooks *HookRunner) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{
		log:                     log,
		hooks:                   hooks,
		sinks:                   map[uint64]EventSink{},
		projects:                map[string]*projectRuntime{},
		channels:                map[string]*channelRuntime{},
		inbound:                 make(chan *Message, 256),
		meetingEventSubscribers: map[uint64]chan MeetingEvent{},
		remoteWorkLocks:         map[string]*sync.Mutex{},
		activeInvocations:       map[string]struct{}{},
	}
}

// SetEventSink attaches the legacy unified event callback. New consumers
// should use SubscribeEventSink so Connect, recording and exporters can
// coexist without replacing each other.
func (e *Engine) SetEventSink(sink EventSink) {
	e.sinkMu.Lock()
	e.sink = sink
	e.sinkMu.Unlock()
}

// SubscribeEventSink adds an independent lifecycle subscriber and returns an
// idempotent unsubscribe function.
func (e *Engine) SubscribeEventSink(sink EventSink) func() {
	if sink == nil {
		return func() {}
	}
	e.sinkMu.Lock()
	e.nextSinkID++
	id := e.nextSinkID
	e.sinks[id] = sink
	e.sinkMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			e.sinkMu.Lock()
			delete(e.sinks, id)
			e.sinkMu.Unlock()
		})
	}
}

// SetMessageLogger attaches the channel message logger used to persist inbound
// channel messages to disk.
func (e *Engine) SetMessageLogger(l *MessageLogger) { e.msgLog = l }

// MessageLogger returns the attached channel message logger, or nil.
func (e *Engine) MessageLogger() *MessageLogger { return e.msgLog }

// SetWorkspaceInitializer attaches the pre-run workspace initializer.
func (e *Engine) SetWorkspaceInitializer(initializer WorkspaceInitializer) {
	e.workspace = initializer
}

func (e *Engine) SetMemoryStore(memory MemoryStore) { e.memory = memory }

func (e *Engine) SetGuard(guard Guard) { e.guard = guard }

// SetConversationStore attaches the durable conversation backend. When set,
// runtimes locate and persist conversations by (scope, chatID); when nil they
// fall back to purely in-memory sessions keyed by chatID.
func (e *Engine) SetConversationStore(cs ConversationStore) {
	e.conversations = cs
	if control, ok := cs.(ChannelControlStore); ok {
		e.channelControl = control
	}
	if feedback, ok := cs.(ChannelFeedbackStore); ok {
		e.feedbackStore = feedback
	}
}

// RuntimeSettingsDefaultStore persists Agent-level defaults selected from a
// channel picker. It remains optional for embedded/test runtimes.
type RuntimeSettingsDefaultStore interface {
	UpdateAgentRuntimeSettings(ctx context.Context, id string, settings RuntimeSettings) error
}

func (e *Engine) SetRuntimeSettingsDefaultStore(store RuntimeSettingsDefaultStore) {
	e.runtimeSettingsDefaults = store
}

func (e *Engine) SetMeetingResponseModeStore(store MeetingResponseModeStore) {
	e.meetingResponseModes = store
}

// emit dispatches a lifecycle event to config.toml hooks and the event sink.
// It copies data per consumer so the caller's map is never mutated and async
// sinks cannot observe a later event's fields (both share nothing).
func (e *Engine) emit(ctx context.Context, event HookEvent, data map[string]string) {
	if event == HookError {
		e.markRemoteTaskError(data)
	}
	if e.ObservationBus() != nil {
		ensureObservationData(data)
	}
	payload := make(map[string]string, len(data)+1)
	for k, v := range data {
		payload[k] = v
	}
	payload["event"] = string(event)
	e.hooks.Fire(ctx, event, payload)
	e.observeLifecycle(ctx, event, payload)
	e.sinkMu.RLock()
	legacy := e.sink
	sinks := make([]EventSink, 0, len(e.sinks))
	for _, sink := range e.sinks {
		sinks = append(sinks, sink)
	}
	e.sinkMu.RUnlock()
	if legacy != nil {
		sinkCopy := make(map[string]string, len(payload))
		for k, v := range payload {
			sinkCopy[k] = v
		}
		legacy(event, sinkCopy)
	}
	for _, sink := range sinks {
		sinkCopy := make(map[string]string, len(payload))
		for k, v := range payload {
			sinkCopy[k] = v
		}
		sink(event, sinkCopy)
	}
}

// AddProject registers a project's agent and platforms with the engine.
func (e *Engine) AddProject(name, workDir string, agent Agent, platforms []Platform, workspace ...WorkspaceInitOptions) {
	e.mu.Lock()
	defer e.mu.Unlock()
	opts := WorkspaceInitOptions{WorkDir: workDir}
	if len(workspace) > 0 {
		opts = workspace[0]
		if opts.WorkDir == "" {
			opts.WorkDir = workDir
		}
	}
	if opts.AgentName == "" {
		opts.AgentName = name
	}
	e.projects[name] = &projectRuntime{
		owner:     e,
		name:      name,
		agent:     agent,
		platforms: platforms,
		workDir:   workDir,
		workspace: opts,
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
	data := map[string]string{
		"text":             msg.Text,
		"platform":         msg.Platform,
		"project":          msg.Project,
		"channel_id":       msg.ChannelID,
		"chat_id":          msg.ChatID,
		"chat_type":        msg.ChatType,
		"root_id":          msg.RootID,
		"parent_id":        msg.ParentID,
		"thread_id":        msg.ThreadID,
		"conversation_key": msg.ConversationKey,
		"user_id":          msg.UserID,
		"user_name":        msg.UserName,
		"mentioned_bot":    fmt.Sprintf("%t", msg.MentionedBot),
		"mention_all":      fmt.Sprintf("%t", msg.MentionAll),
		"origin":           msg.Origin,
	}
	if callback := msg.Callback; callback != nil {
		data["event_type"] = callback.Type
		data["event_id"] = msg.ID
		data["message_id"] = callback.MessageID
		data["operator_id"] = msg.UserID
		data["host"] = callback.Host
		data["action_tag"] = callback.ActionTag
		data["action_name"] = callback.ActionName
		data["action_value"] = callback.ActionValue
		data["form_value"] = callback.FormValue
		data["input_value"] = callback.InputValue
		data["option"] = callback.Option
		data["options"] = callback.Options
		data["checked"] = fmt.Sprintf("%t", callback.Checked)
		data["timezone"] = callback.Timezone
	}
	return data
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
	if msg == nil {
		return
	}
	msg.ConversationKey = ResolveConversationKey(msg)
	if msg.ChannelID != "" {
		rt := e.channelRuntime(msg.ChannelID)
		if rt == nil {
			e.log.Warn("no runtime for channel message", "channel_id", msg.ChannelID)
			return
		}
		if rt.duplicateMessage(msg) {
			e.log.Info("duplicate channel message ignored", "channel_id", msg.ChannelID, "platform", msg.Platform, "message_id", msg.ID)
			return
		}
		if !msg.LogOnly && !rt.acceptsMessage(msg) {
			e.log.Info("channel message ignored by reply scope",
				"channel_id", msg.ChannelID,
				"platform", msg.Platform,
				"chat_type", msg.ChatType,
				"mentioned_bot", msg.MentionedBot)
			return
		}
	}

	data := eventData(msg)

	if msg.ChannelID != "" && e.msgLog != nil {
		if err := e.msgLog.Log(msg.ChannelID, data); err != nil {
			e.log.Warn("write channel message log", "channel_id", msg.ChannelID, "err", err)
		}
	}
	if msg.LogOnly {
		return
	}

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

	if e.handleProjectHelpCommand(ctx, pr, msg) {
		e.emit(ctx, HookMessageSent, data)
		return
	}

	if e.handleProjectConversationCommand(ctx, pr, msg) {
		e.emit(ctx, HookMessageSent, data)
		return
	}

	sess, conv, created, err := pr.session(ctx, msg.ChatID, msg.ChatType, msg.ConversationKey)
	if err != nil {
		e.log.Error("start session", "err", err)
		e.emit(ctx, HookError, withError(data, err))
		e.replyAll(ctx, pr, msg, "failed to start agent session: "+err.Error())
		return
	}
	data["agent_id"] = pr.workspace.AgentID
	data["runtime_id"] = pr.workspace.RuntimeID
	if pr.agent != nil {
		data["agent_name"] = pr.agent.Name()
	}
	data["session_id"] = sessionObservationID(sess)
	if conv != nil {
		data["conversation_id"] = conv.ID
	}
	if created {
		e.emit(ctx, HookSessionStarted, data)
	}
	if e.handleRuntimeSettingsAction(ctx, sess, msg, nil, "", func(state RuntimeSettingsPickerState) bool {
		return e.updateRuntimeSettingsPicker(ctx, pr, msg, state)
	}, func(text string) { e.replyAll(ctx, pr, msg, text) }) {
		e.persistConversationTurn(ctx, conv, sess)
		e.emit(ctx, HookMessageSent, data)
		return
	}
	if e.handleRuntimeSettingsCommand(sess, msg.Text, func(text string) {
		e.replyAll(ctx, pr, msg, text)
	}, func(state RuntimeSettingsPickerState) bool {
		return e.replyRuntimeSettingsPicker(ctx, pr, msg, state)
	}, func(state ModelPickerState) bool {
		return e.replyModelPicker(ctx, pr, msg, state)
	}) {
		e.persistConversationTurn(ctx, conv, sess)
		e.emit(ctx, HookMessageSent, data)
		return
	}

	_, _ = e.streamTurn(ctx, sess, msg.Text, func(text string) {
		e.replyAll(ctx, pr, msg, text)
	}, data)
	e.persistConversationTurn(ctx, conv, sess)
	e.emit(ctx, HookMessageSent, data)
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
		for cacheKey, s := range pr.sessions {
			data := map[string]string{
				"project": pr.name, "agent_id": pr.workspace.AgentID, "runtime_id": pr.workspace.RuntimeID,
				"session_id": sessionObservationID(s), "conversation_id": cacheKey,
			}
			if pr.agent != nil {
				data["agent_name"] = pr.agent.Name()
			}
			e.emit(ctx, HookSessionEnded, data)
			if detachable, ok := s.(DetachableAgentSession); ok {
				_ = detachable.Detach(ctx)
			} else {
				_ = s.Close(ctx)
			}
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
