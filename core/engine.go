package core

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

// projectRuntime holds the live platform/agent instances for one project.
type projectRuntime struct {
	owner     *Engine
	name      string
	agent     Agent
	platforms []Platform
	workDir   string
	workspace WorkspaceInitOptions

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
	log                     *slog.Logger
	hooks                   *HookRunner
	sink                    EventSink
	mu                      sync.RWMutex
	projects                map[string]*projectRuntime
	channels                map[string]*channelRuntime
	inbound                 chan *Message
	workspace               WorkspaceInitializer
	conversations           ConversationStore
	runtimeSettingsDefaults RuntimeSettingsDefaultStore
	msgLog                  *MessageLogger
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

// SetMessageLogger attaches the channel message logger used to persist inbound
// channel messages to disk.
func (e *Engine) SetMessageLogger(l *MessageLogger) { e.msgLog = l }

// MessageLogger returns the attached channel message logger, or nil.
func (e *Engine) MessageLogger() *MessageLogger { return e.msgLog }

// SetWorkspaceInitializer attaches the pre-run workspace initializer.
func (e *Engine) SetWorkspaceInitializer(initializer WorkspaceInitializer) {
	e.workspace = initializer
}

// SetConversationStore attaches the durable conversation backend. When set,
// runtimes locate and persist conversations by (scope, chatID); when nil they
// fall back to purely in-memory sessions keyed by chatID.
func (e *Engine) SetConversationStore(cs ConversationStore) {
	e.conversations = cs
}

// RuntimeSettingsDefaultStore persists Agent-level defaults selected from a
// channel picker. It is optional so config.toml projects keep working.
type RuntimeSettingsDefaultStore interface {
	UpdateAgentRuntimeSettings(ctx context.Context, id string, settings RuntimeSettings) error
}

func (e *Engine) SetRuntimeSettingsDefaultStore(store RuntimeSettingsDefaultStore) {
	e.runtimeSettingsDefaults = store
}

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
	return map[string]string{
		"text":          msg.Text,
		"platform":      msg.Platform,
		"project":       msg.Project,
		"channel_id":    msg.ChannelID,
		"chat_id":       msg.ChatID,
		"chat_type":     msg.ChatType,
		"user_id":       msg.UserID,
		"user_name":     msg.UserName,
		"mentioned_bot": fmt.Sprintf("%t", msg.MentionedBot),
		"mention_all":   fmt.Sprintf("%t", msg.MentionAll),
		"origin":        msg.Origin,
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
		if !rt.acceptsMessage(msg) {
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

	if e.handleProjectConversationCommand(ctx, pr, msg) {
		e.emit(ctx, HookMessageSent, data)
		return
	}

	sess, conv, created, err := pr.session(ctx, msg.ChatID, msg.ChatType)
	if err != nil {
		e.log.Error("start session", "err", err)
		e.emit(ctx, HookError, withError(data, err))
		e.replyAll(ctx, pr, msg, "failed to start agent session: "+err.Error())
		return
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

func (e *Engine) handleRuntimeSettingsCommand(sess AgentSession, text string, reply func(string), picker func(RuntimeSettingsPickerState) bool, legacyPicker func(ModelPickerState) bool) bool {
	cmd, ok := parseRuntimeSettingsCommand(text)
	if !ok {
		return false
	}
	if reply == nil {
		reply = func(string) {}
	}
	settings, ok := RuntimeSettingsForSession(sess)
	if !ok {
		reply("This runtime does not support runtime settings.")
		return true
	}
	if cmd.List {
		if picker != nil && picker(runtimeSettingsPickerState(settings, RuntimeSettingsScopeConversation, RuntimeSettings{}, false)) {
			return true
		}
		if cmd.Setting == RuntimeSettingModel && legacyPicker != nil {
			if models, ok := sess.(ModelSwitchingSession); ok && models.ModelSwitchingSupported() && legacyPicker(modelPickerState(models)) {
				return true
			}
		}
		reply(formatRuntimeSettingsStatus(settings))
		return true
	}
	var err error
	if cmd.Reset {
		err = settings.ResetRuntimeSetting(cmd.Setting)
	} else {
		err = settings.SetRuntimeSetting(cmd.Setting, cmd.Value)
	}
	if err != nil {
		reply(err.Error())
		return true
	}
	// The command response itself is the refreshed menu/status; interactive
	// pickers use handleRuntimeSettingsAction and edit their existing message.
	reply(formatRuntimeSettingsStatus(settings))
	return true
}

// handleModelCommand is retained for package tests and third-party callers
// that still use the model-only callback. New engine paths call the richer
// handleRuntimeSettingsCommand above.
func (e *Engine) handleModelCommand(sess AgentSession, text string, reply func(string), picker func(ModelPickerState) bool) bool {
	return e.handleRuntimeSettingsCommand(sess, text, reply, nil, picker)
}

func (e *Engine) handleRuntimeSettingsAction(ctx context.Context, sess AgentSession, msg *Message, agentDefaults *RuntimeSettings, agentID string, update func(RuntimeSettingsPickerState) bool, reply func(string)) bool {
	if msg == nil || msg.RuntimeSettingsAction == nil {
		return false
	}
	settings, ok := RuntimeSettingsForSession(sess)
	if !ok {
		if reply != nil {
			reply("This runtime does not support runtime settings.")
		}
		return true
	}
	action := *msg.RuntimeSettingsAction
	if action.Scope == "" {
		action.Scope = RuntimeSettingsScopeConversation
	}
	agentEditable := agentDefaults != nil && agentID != "" && !strings.HasPrefix(agentID, "config:") && e.runtimeSettingsDefaults != nil
	var err error
	if action.Scope == RuntimeSettingsScopeAgent {
		if !agentEditable {
			err = fmt.Errorf("Agent defaults are not editable for this channel")
		} else {
			candidate := *agentDefaults
			err = applyRuntimeSettingsAction(settings, action, &candidate)
			if err == nil {
				err = e.runtimeSettingsDefaults.UpdateAgentRuntimeSettings(ctx, agentID, candidate)
			}
			if err == nil {
				*agentDefaults = candidate
			}
		}
	} else {
		err = applyRuntimeSettingsAction(settings, action, nil)
	}
	defaults := RuntimeSettings{}
	if agentDefaults != nil {
		defaults = *agentDefaults
	}
	state := runtimeSettingsPickerState(settings, action.Scope, defaults, agentEditable)
	if err != nil {
		state.Notice = err.Error()
	}
	if update != nil && update(state) {
		return true
	}
	if reply != nil {
		if err != nil {
			reply(err.Error())
		} else {
			reply(formatRuntimeSettingsStatus(settings))
		}
	}
	return true
}

func parseModelCommand(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}
	fields := strings.Fields(text)
	if len(fields) == 0 || fields[0] != "/model" {
		return "", false
	}
	if len(fields) == 1 {
		return "", true
	}
	return fields[1], true
}

func formatModelStatus(models ModelSwitchingSession) string {
	current := models.CurrentModel()
	if current == "" {
		current = "(runtime default)"
	}
	def := models.DefaultModel()
	if def == "" {
		def = "(runtime default)"
	}
	return "Current model: " + current + "\nDefault model: " + def + "\n\n" + formatAvailableModels(models.SupportedModels())
}

func formatAvailableModels(models []string) string {
	if len(models) == 0 {
		return "Available models: none configured."
	}
	return "Available models:\n- " + strings.Join(models, "\n- ")
}

func modelPickerState(models ModelSwitchingSession) ModelPickerState {
	current := models.CurrentModel()
	def := models.DefaultModel()
	supported := models.SupportedModels()
	options := make([]ModelPickerOption, 0, len(supported))
	for _, model := range supported {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		options = append(options, ModelPickerOption{
			Model:   model,
			Current: model == current,
			Default: model == def,
		})
	}
	return ModelPickerState{
		CurrentModel: current,
		DefaultModel: def,
		Options:      options,
	}
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
		case EventToolUse:
			// This path posts a new message per event, so only surface the
			// tool invocation itself (not results) as a compact progress note.
			if reply != nil && ev.ToolName != "" {
				note := "🔧 " + ev.ToolName
				if ev.ToolInput != "" {
					note += " " + ev.ToolInput
				}
				reply(note)
			}
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

// streamTurnMessage drives a single turn onto a StreamMessageReplier,
// rendering the whole answer as one in-place updating plain-text message.
func (e *Engine) streamTurnMessage(ctx context.Context, mr StreamMessageReplier, sess AgentSession, msg *Message, data map[string]string) {
	events, err := sess.Send(ctx, msg.Text)
	if err != nil {
		e.log.Error("send to session", "err", err)
		e.emit(ctx, HookError, withError(data, err))
		e.emitMessageStreamOnce(ctx, mr, msg, "failed: "+err.Error(), true)
		return
	}

	stream, err := mr.BeginMessageReply(ctx, msg)
	if err != nil {
		e.log.Error("begin streaming message reply", "err", err)
		var reply func(string)
		if p, ok := mr.(Platform); ok {
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

	e.driveReplyStream(ctx, stream, events, data)
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

	e.driveReplyStream(ctx, stream, events, data)
}

func (e *Engine) driveReplyStream(ctx context.Context, stream ReplyStream, events <-chan *Event, data map[string]string) {
	var answer, thinking, rendered string
	var failed bool
	var tools toolProgress
	for ev := range events {
		switch ev.Type {
		case EventThinking:
			if ev.Text == "" {
				continue
			}
			thinking = ev.Text
			body := tools.render(thinking, answer, false)
			if body != rendered {
				if err := stream.Update(ctx, body, false, false); err != nil {
					e.log.Error("stream update", "err", err)
				}
				rendered = body
			}
		case EventToolUse:
			// A tool_use frame opens a step; a tool_result frame (ToolResult
			// set, ToolName empty) closes the most recent open one.
			if ev.ToolName != "" {
				tools.add(ev.ToolName, ev.ToolInput)
			} else if ev.ToolResult != "" || ev.Err != nil {
				tools.attachResult(ev.ToolResult, ev.Err != nil)
			}
			body := tools.render(thinking, answer, false)
			if body != rendered {
				if err := stream.Update(ctx, body, false, false); err != nil {
					e.log.Error("stream update", "err", err)
				}
				rendered = body
			}
		case EventFinal, EventOutput:
			if ev.Text == "" || ev.Text == "NO_REPLY" {
				continue
			}
			answer = ev.Text
			body := tools.render(thinking, answer, false)
			if body != rendered {
				if err := stream.Update(ctx, body, false, false); err != nil {
					e.log.Error("stream update", "err", err)
				}
				rendered = body
			}
		case EventError:
			e.emit(ctx, HookError, withError(data, ev.Err))
			failed = true
			answer = "error: " + errString(ev.Err)
		}
	}

	if answer == "" && tools.empty() && thinking == "" {
		answer = "(no reply)"
	}
	if err := stream.Update(ctx, tools.render(thinking, answer, true), true, failed); err != nil {
		e.log.Error("stream finalize", "err", err)
	}
}

func (e *Engine) emitMessageStreamOnce(ctx context.Context, mr StreamMessageReplier, msg *Message, text string, failed bool) {
	stream, err := mr.BeginMessageReply(ctx, msg)
	if err != nil {
		e.log.Error("begin streaming message reply", "err", err)
		return
	}
	defer func() { _ = stream.Close(ctx) }()
	if err := stream.Update(ctx, text, true, failed); err != nil {
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

func (e *Engine) replyModelPicker(ctx context.Context, pr *projectRuntime, msg *Message, state ModelPickerState) bool {
	for _, p := range pr.platforms {
		if p.Name() != msg.Platform {
			continue
		}
		mp, ok := p.(ModelPickerReplier)
		if !ok {
			return false
		}
		if err := mp.ReplyModelPicker(ctx, msg, state); err != nil {
			e.log.Error("reply model picker", "platform", p.Name(), "err", err)
			return false
		}
		return true
	}
	return false
}

func (e *Engine) replyRuntimeSettingsPicker(ctx context.Context, pr *projectRuntime, msg *Message, state RuntimeSettingsPickerState) bool {
	for _, p := range pr.platforms {
		if p.Name() != msg.Platform {
			continue
		}
		picker, ok := p.(RuntimeSettingsPickerReplier)
		if !ok {
			return false
		}
		if err := picker.ReplyRuntimeSettingsPicker(ctx, msg, state); err != nil {
			e.log.Error("reply runtime settings picker", "platform", p.Name(), "err", err)
			return false
		}
		return true
	}
	return false
}

func (e *Engine) updateRuntimeSettingsPicker(ctx context.Context, pr *projectRuntime, msg *Message, state RuntimeSettingsPickerState) bool {
	for _, p := range pr.platforms {
		if p.Name() != msg.Platform {
			continue
		}
		picker, ok := p.(RuntimeSettingsPickerReplier)
		if !ok {
			return false
		}
		if err := picker.UpdateRuntimeSettingsPicker(ctx, msg, state); err != nil {
			e.log.Error("update runtime settings picker", "platform", p.Name(), "err", err)
			return false
		}
		return true
	}
	return false
}

func (e *Engine) handleProjectConversationCommand(ctx context.Context, pr *projectRuntime, msg *Message) bool {
	if !isConversationCommand(msg.Text) {
		return false
	}
	e.resetConversation(ctx, pr.scope(), msg.ChatID, msg.ChatType, pr.workspace.AgentID, pr.dropSession)
	e.replyAll(ctx, pr, msg, conversationResetReply)
	return true
}

func (pr *projectRuntime) scope() string { return "project:" + pr.name }

// dropSession closes and removes the cached in-memory session for cacheKey.
// With durable conversations cacheKey is the conversation id; without them it
// is the platform chat id.
func (pr *projectRuntime) dropSession(ctx context.Context, cacheKey string) {
	pr.mu.Lock()
	s, ok := pr.sessions[cacheKey]
	if ok {
		delete(pr.sessions, cacheKey)
	}
	pr.mu.Unlock()
	if ok && s != nil {
		_ = s.Close(ctx)
	}
}

const conversationResetReply = "Started a new conversation. Previous context has been cleared."

func isConversationCommand(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "/new", "/clear", "/reset":
		return true
	default:
		return false
	}
}

func (e *Engine) resetConversation(ctx context.Context, scope, chatID, chatType, agentID string, dropSession func(context.Context, string)) {
	cacheKey := chatID
	if e.conversations != nil {
		conv, _, err := e.conversations.GetOrCreateConversation(ctx, Conversation{
			Scope:    scope,
			ChatID:   chatID,
			ChatType: chatType,
			AgentID:  agentID,
		})
		if err != nil {
			e.log.Warn("resolve conversation for command", "scope", scope, "chat_id", chatID, "err", err)
		} else if conv != nil {
			cacheKey = conv.ID
			if endErr := e.conversations.EndConversation(ctx, conv.ID); endErr != nil {
				e.log.Warn("end conversation", "conversation", conv.ID, "err", endErr)
			}
		}
	}
	if dropSession != nil {
		dropSession(ctx, cacheKey)
	}
}

func (pr *projectRuntime) session(ctx context.Context, chatID, chatType string) (AgentSession, *Conversation, bool, error) {
	if pr.agent == nil {
		return nil, nil, false, fmt.Errorf("project %q has no agent", pr.name)
	}
	conv, workDir, err := pr.owner.prepareConversation(ctx, pr.scope(), chatID, chatType, pr.workspace, pr.workDir)
	if err != nil {
		return nil, nil, false, err
	}
	cacheKey := chatID
	if conv != nil {
		cacheKey = conv.ID
	}

	pr.mu.Lock()
	defer pr.mu.Unlock()
	if s, ok := pr.sessions[cacheKey]; ok {
		return s, conv, false, nil
	}
	s, err := pr.owner.startAgentSession(ctx, pr.agent, workDir, conv)
	if err != nil {
		return nil, nil, false, err
	}
	pr.sessions[cacheKey] = s
	return s, conv, true, nil
}

// prepareConversation resolves (or creates) the durable conversation for
// (scope, chatID) and prepares its isolated working directory. When no
// conversation store is attached it returns a nil conversation and the
// initialized fallback work dir, preserving the legacy chatID-keyed behavior.
func (e *Engine) prepareConversation(ctx context.Context, scope, chatID, chatType string, ws WorkspaceInitOptions, fallbackWorkDir string) (*Conversation, string, error) {
	if e.conversations == nil {
		workDir, err := e.initializeWorkspace(ctx, ws, fallbackWorkDir)
		return nil, workDir, err
	}

	baseWorkDir := ws.WorkDir
	if baseWorkDir == "" {
		baseWorkDir = fallbackWorkDir
	}
	cwd, err := conversationCwd(conversationBaseDir(baseWorkDir), ws.AgentID, scope, chatID)
	if err != nil {
		return nil, "", err
	}
	conv, _, err := e.conversations.GetOrCreateConversation(ctx, Conversation{
		Scope:    scope,
		ChatID:   chatID,
		ChatType: chatType,
		AgentID:  ws.AgentID,
		WorkDir:  cwd,
	})
	if err != nil {
		return nil, "", fmt.Errorf("resolve conversation: %w", err)
	}
	target := conv.WorkDir
	if target == "" {
		target = cwd
	}
	convOpts := ws
	convOpts.WorkDir = target
	workDir, err := e.initializeWorkspace(ctx, convOpts, target)
	if err != nil {
		return nil, "", err
	}
	if conv.WorkDir != workDir {
		if uerr := e.conversations.UpdateConversationSession(ctx, conv.ID, conv.NativeSessionID, workDir); uerr != nil {
			e.log.Warn("persist conversation workdir", "conversation", conv.ID, "err", uerr)
		}
		conv.WorkDir = workDir
	}
	return conv, workDir, nil
}

// startAgentSession starts a new agent session, resuming the conversation's
// native session id when both the agent and a stored id are available.
func (e *Engine) startAgentSession(ctx context.Context, agent Agent, workDir string, conv *Conversation) (AgentSession, error) {
	if conv != nil && conv.NativeSessionID != "" {
		if ra, ok := agent.(ResumableAgent); ok {
			return ra.StartSessionResume(ctx, workDir, conv.NativeSessionID)
		}
	}
	return agent.StartSession(ctx, workDir)
}

// persistConversationTurn records a completed turn: it bumps the conversation
// activity counter and persists any newly discovered native session id so
// later turns and restarts can resume.
func (e *Engine) persistConversationTurn(ctx context.Context, conv *Conversation, sess AgentSession) {
	if e.conversations == nil || conv == nil {
		return
	}
	if err := e.conversations.TouchConversation(ctx, conv.ID); err != nil {
		e.log.Warn("touch conversation", "conversation", conv.ID, "err", err)
	}
	ns, ok := sess.(NativeSessioned)
	if !ok {
		return
	}
	if id := ns.NativeSessionID(); id != "" && id != conv.NativeSessionID {
		if err := e.conversations.UpdateConversationSession(ctx, conv.ID, id, conv.WorkDir); err != nil {
			e.log.Warn("persist native session id", "conversation", conv.ID, "err", err)
			return
		}
		conv.NativeSessionID = id
	}
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
