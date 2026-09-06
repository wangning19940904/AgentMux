package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

const channelMessageDedupTTL = 10 * time.Minute

var channelHealthCheckInterval = 15 * time.Second

// channelAgentGeneration is one immutable Agent configuration applied to a
// channel. Saving the Agent installs a new current generation while sessions
// that already own the previous generation finish normally.
type channelAgentGeneration struct {
	agent           Agent
	workDir         string
	workspace       WorkspaceInitOptions
	defaultSettings RuntimeSettings
	sessions        int
	retired         atomic.Bool
	stopped         bool
}

type channelSessionBinding struct {
	cacheKey   string
	session    AgentSession
	generation *channelAgentGeneration
	active     int
	retired    bool
	closed     bool
	done       chan struct{}
	doneOnce   sync.Once
}

func (binding *channelSessionBinding) signalDone() {
	if binding != nil && binding.done != nil {
		binding.doneOnce.Do(func() { close(binding.done) })
	}
}

// channelRuntime holds one live console-managed channel: the platform
// connection, the bound agent and its per-chat sessions.
type channelRuntime struct {
	owner           *Engine
	channel         Channel
	platform        Platform
	agent           Agent
	workDir         string
	workspace       WorkspaceInitOptions
	defaultSettings RuntimeSettings
	cancel          context.CancelFunc
	runCtx          context.Context

	mu                  sync.Mutex
	generation          *channelAgentGeneration
	sessions            map[string]*channelSessionBinding // conversation id -> current generation session
	retiredSessions     map[*channelSessionBinding]struct{}
	seen                map[string]time.Time
	state               string
	errMsg              string
	started             time.Time
	connected           bool
	connectedAt         time.Time
	lastCheckedAt       time.Time
	lastHeartbeatAt     time.Time
	lastEventAt         time.Time
	lastInboundAt       time.Time
	terminal            bool
	meetingResponseMode atomic.Value // string
	definitionUpdatedAt atomic.Value // time.Time

	queueCardMu  sync.Mutex
	routingLocks sync.Map
	routeMu      sync.Mutex
	routeState   map[string]string
	controlMu    sync.Mutex
	controlTasks map[string]*channelControlState
	directTurns  map[string]*directChannelTurn
	clearConfirm map[string]time.Time
	threadLists  map[string][]NativeThread
	pendingTurns map[string]pendingInitialTurn
}

func (rt *channelRuntime) currentMeetingResponseMode() string {
	if rt != nil {
		if value := rt.meetingResponseMode.Load(); value != nil {
			if mode, ok := value.(string); ok && mode != "" {
				return mode
			}
		}
		return ChannelMeetingResponseMode(rt.channel)
	}
	return DefaultMeetingResponseMode
}

func (rt *channelRuntime) setMeetingResponseMode(mode string) {
	if rt == nil {
		return
	}
	if normalized := NormalizeMeetingResponseMode(mode); normalized != "" {
		rt.meetingResponseMode.Store(normalized)
	}
}

func (rt *channelRuntime) currentDefinitionUpdatedAt() time.Time {
	if rt != nil {
		if value := rt.definitionUpdatedAt.Load(); value != nil {
			if updatedAt, ok := value.(time.Time); ok {
				return updatedAt
			}
		}
		return rt.channel.UpdatedAt
	}
	return time.Time{}
}

func (rt *channelRuntime) setDefinitionUpdatedAt(updatedAt time.Time) {
	if rt != nil {
		rt.definitionUpdatedAt.Store(updatedAt)
	}
}

type pendingInitialTurn struct {
	message         *Message
	requiredSetting RuntimeSetting
}

func (rt *channelRuntime) runtimeDefaults() RuntimeSettings {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.defaultSettings
}

func (rt *channelRuntime) setRuntimeDefaults(settings RuntimeSettings) {
	rt.mu.Lock()
	rt.defaultSettings = settings
	if generation := rt.ensureGenerationLocked(); generation != nil {
		generation.defaultSettings = settings
	}
	rt.mu.Unlock()
}

func (rt *channelRuntime) ensureGenerationLocked() *channelAgentGeneration {
	if rt == nil {
		return nil
	}
	if rt.generation == nil {
		rt.generation = &channelAgentGeneration{
			agent: rt.agent, workDir: rt.workDir, workspace: rt.workspace, defaultSettings: rt.defaultSettings,
		}
	}
	return rt.generation
}

func (rt *channelRuntime) agentSnapshot() (Agent, string, WorkspaceInitOptions) {
	if rt == nil {
		return nil, "", WorkspaceInitOptions{}
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	generation := rt.ensureGenerationLocked()
	return generation.agent, generation.workDir, generation.workspace
}

// replaceAgentGeneration hot-swaps the Agent definition without restarting
// the platform connection. Cached idle sessions are retired immediately;
// active sessions retain their old generation until their lease is released.
func (rt *channelRuntime) replaceAgentGeneration(ctx context.Context, agent Agent, workDir string, workspace WorkspaceInitOptions) error {
	if rt == nil {
		return nil
	}
	newGeneration := &channelAgentGeneration{
		agent: agent, workDir: workDir, workspace: workspace, defaultSettings: workspace.RuntimeDefaults,
	}
	var closeBindings []*channelSessionBinding
	var stopGeneration *channelAgentGeneration
	rt.mu.Lock()
	oldGeneration := rt.ensureGenerationLocked()
	oldGeneration.retired.Store(true)
	if rt.retiredSessions == nil {
		rt.retiredSessions = map[*channelSessionBinding]struct{}{}
	}
	for cacheKey, binding := range rt.sessions {
		delete(rt.sessions, cacheKey)
		binding.retired = true
		if binding.active > 0 {
			rt.retiredSessions[binding] = struct{}{}
			continue
		}
		binding.closed = true
		binding.generation.sessions--
		closeBindings = append(closeBindings, binding)
	}
	if oldGeneration.sessions == 0 && !oldGeneration.stopped {
		oldGeneration.stopped = true
		stopGeneration = oldGeneration
	}
	rt.generation = newGeneration
	rt.agent = agent
	rt.workDir = workDir
	rt.workspace = workspace
	rt.defaultSettings = workspace.RuntimeDefaults
	rt.mu.Unlock()

	var errs []error
	for _, binding := range closeBindings {
		if err := rt.closeSessionBinding(ctx, binding); err != nil {
			errs = append(errs, err)
		}
		binding.signalDone()
	}
	if stopGeneration != nil && stopGeneration.agent != nil {
		if err := stopGeneration.agent.Stop(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// scope returns the conversation scope namespace for this channel.
func (rt *channelRuntime) scope() string { return "channel:" + rt.channel.ID }

// session returns the agent session for chatID, creating one when needed. It
// also returns the durable conversation (nil when no conversation store is
// attached) so callers can persist turn activity and native session ids.
func (rt *channelRuntime) session(ctx context.Context, msg *Message) (AgentSession, *Conversation, bool, *channelAgentGeneration, func(), error) {
	if msg == nil {
		return nil, nil, false, nil, nil, fmt.Errorf("channel message is required")
	}
	rt.mu.Lock()
	generation := rt.ensureGenerationLocked()
	for {
		var wait <-chan struct{}
		for binding := range rt.retiredSessions {
			if binding.active > 0 && binding.generation.workDir == generation.workDir {
				wait = binding.done
				break
			}
		}
		if wait == nil {
			break
		}
		rt.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, nil, false, nil, nil, ctx.Err()
		case <-wait:
		}
		rt.mu.Lock()
		generation = rt.ensureGenerationLocked()
	}
	if generation.agent == nil {
		rt.mu.Unlock()
		return nil, nil, false, nil, nil, fmt.Errorf("channel %q has no agent bound", rt.channel.Name)
	}
	opts := generation.workspace
	if opts.WorkDir == "" {
		opts.WorkDir = generation.workDir
	}
	conversationKey := ResolveConversationKey(msg)
	conv, workDir, err := rt.owner.prepareConversation(ctx, rt.scope(), msg.ChatID, msg.ChatType, conversationKey, opts, generation.workDir)
	if err != nil {
		rt.mu.Unlock()
		return nil, nil, false, nil, nil, err
	}
	cacheKey := conversationKey
	if conv != nil {
		cacheKey = conv.ID
	}
	if binding, ok := rt.sessions[cacheKey]; ok {
		binding.active++
		release := rt.bindingRelease(binding)
		rt.mu.Unlock()
		return binding.session, conv, false, binding.generation, release, nil
	}
	s, err := rt.owner.startAgentSession(ctx, generation.agent, workDir, conv)
	if err != nil {
		rt.mu.Unlock()
		return nil, nil, false, nil, nil, err
	}
	rt.applyRuntimeDefaultsFrom(s, generation.defaultSettings)
	rt.owner.persistConversationSessionHandle(ctx, conv, s)
	binding := &channelSessionBinding{
		cacheKey: cacheKey, session: s, generation: generation, active: 1, done: make(chan struct{}),
	}
	generation.sessions++
	rt.sessions[cacheKey] = binding
	release := rt.bindingRelease(binding)
	rt.mu.Unlock()
	return s, conv, true, generation, release, nil
}

func (rt *channelRuntime) applyRuntimeDefaultsFrom(sess AgentSession, defaults RuntimeSettings) {
	settings, ok := RuntimeSettingsForSession(sess)
	if !ok {
		return
	}
	for _, setting := range []RuntimeSetting{RuntimeSettingModel, RuntimeSettingReasoningEffort, RuntimeSettingServiceTier, RuntimeSettingApprovalMode} {
		value := defaults.Value(setting)
		if value == "" || !settings.RuntimeSettingsCapabilities().Supports(setting) {
			continue
		}
		if err := settings.SetRuntimeSetting(setting, value); err != nil {
			rt.owner.log.Warn("apply Agent runtime default", "channel", rt.channel.Name, "setting", setting, "err", err)
		}
	}
}

func (rt *channelRuntime) bindingRelease(binding *channelSessionBinding) func() {
	var once sync.Once
	return func() {
		once.Do(func() { rt.releaseSessionBinding(context.Background(), binding) })
	}
}

func (rt *channelRuntime) releaseSessionBinding(ctx context.Context, binding *channelSessionBinding) {
	if rt == nil || binding == nil {
		return
	}
	var closeBinding bool
	var stopGeneration *channelAgentGeneration
	rt.mu.Lock()
	if binding.active > 0 {
		binding.active--
	}
	if binding.retired && binding.active == 0 && !binding.closed {
		binding.closed = true
		delete(rt.retiredSessions, binding)
		binding.generation.sessions--
		closeBinding = true
		if binding.generation.retired.Load() && binding.generation.sessions == 0 && !binding.generation.stopped {
			binding.generation.stopped = true
			stopGeneration = binding.generation
		}
	}
	rt.mu.Unlock()
	if closeBinding {
		_ = rt.closeSessionBinding(ctx, binding)
	}
	if stopGeneration != nil && stopGeneration.agent != nil {
		_ = stopGeneration.agent.Stop(ctx)
	}
	if closeBinding {
		binding.signalDone()
	}
}

func (rt *channelRuntime) closeSessionBinding(ctx context.Context, binding *channelSessionBinding) error {
	if binding == nil || binding.session == nil {
		return nil
	}
	generation := binding.generation
	data := map[string]string{
		"channel_id": rt.channel.ID, "session_id": sessionObservationID(binding.session), "conversation_id": binding.cacheKey,
	}
	if generation != nil {
		data["agent_id"] = generation.workspace.AgentID
		data["runtime_id"] = generation.workspace.RuntimeID
		if generation.agent != nil {
			data["agent_name"] = generation.agent.Name()
		}
	}
	rt.owner.emit(ctx, HookSessionEnded, data)
	if detachable, ok := binding.session.(DetachableAgentSession); ok {
		return detachable.Detach(ctx)
	}
	return binding.session.Close(ctx)
}

// dropSession closes and removes the cached in-memory session for cacheKey
// (the conversation id). No-op when absent.
func (rt *channelRuntime) dropSession(ctx context.Context, cacheKey string) {
	var closeBinding bool
	var stopGeneration *channelAgentGeneration
	rt.mu.Lock()
	binding, ok := rt.sessions[cacheKey]
	if ok {
		delete(rt.sessions, cacheKey)
		binding.retired = true
		if binding.active > 0 {
			if rt.retiredSessions == nil {
				rt.retiredSessions = map[*channelSessionBinding]struct{}{}
			}
			rt.retiredSessions[binding] = struct{}{}
		} else if !binding.closed {
			binding.closed = true
			binding.generation.sessions--
			closeBinding = true
			if binding.generation.retired.Load() && binding.generation.sessions == 0 && !binding.generation.stopped {
				binding.generation.stopped = true
				stopGeneration = binding.generation
			}
		}
	}
	rt.mu.Unlock()
	if closeBinding {
		_ = rt.closeSessionBinding(ctx, binding)
	}
	if stopGeneration != nil && stopGeneration.agent != nil {
		_ = stopGeneration.agent.Stop(ctx)
	}
	if closeBinding {
		binding.signalDone()
	}
}

// duplicateMessage marks an inbound platform message as seen and reports
// whether this channel already processed it recently.
func (rt *channelRuntime) duplicateMessage(msg *Message) bool {
	if msg == nil || msg.ID == "" {
		return false
	}
	key := msg.Platform + ":" + msg.ChatID + ":" + msg.ID
	now := time.Now()

	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.seen == nil {
		rt.seen = map[string]time.Time{}
	}
	if ts, ok := rt.seen[key]; ok && now.Sub(ts) < channelMessageDedupTTL {
		return true
	}
	if len(rt.seen) > 2048 {
		for k, ts := range rt.seen {
			if now.Sub(ts) >= channelMessageDedupTTL {
				delete(rt.seen, k)
			}
		}
		if len(rt.seen) > 2048 {
			rt.seen = map[string]time.Time{}
		}
	}
	rt.seen[key] = now
	return false
}

func (rt *channelRuntime) acceptsMessage(msg *Message) bool {
	if rt == nil || msg == nil {
		return false
	}
	if !isFeishuLikeChannel(rt.channel.Type) {
		return true
	}
	// Meeting control is an explicit command namespace. It must remain usable
	// even when normal group traffic is configured as mentions-only.
	if isMeetingCommand(msg.Text) || msg.Origin == OriginMeeting {
		return true
	}
	switch channelReplyScope(rt.channel) {
	case ReplyScopeAll:
		return true
	case ReplyScopeMentionsOnly:
		return msg.MentionedBot
	default:
		return msg.ChatType == "p2p" || msg.MentionedBot
	}
}

// close stops the platform connection and all sessions.
func (rt *channelRuntime) close(ctx context.Context) {
	if rt.cancel != nil {
		rt.cancel()
	}
	rt.mu.Lock()
	bindings := make(map[*channelSessionBinding]struct{}, len(rt.sessions)+len(rt.retiredSessions))
	for _, binding := range rt.sessions {
		bindings[binding] = struct{}{}
	}
	for binding := range rt.retiredSessions {
		bindings[binding] = struct{}{}
	}
	rt.sessions = map[string]*channelSessionBinding{}
	rt.retiredSessions = map[*channelSessionBinding]struct{}{}
	platform := rt.platform
	generations := map[*channelAgentGeneration]struct{}{}
	if generation := rt.ensureGenerationLocked(); generation != nil {
		generations[generation] = struct{}{}
	}
	for binding := range bindings {
		if !binding.closed {
			binding.closed = true
		}
		if binding.generation != nil {
			generations[binding.generation] = struct{}{}
		}
	}
	for generation := range generations {
		generation.stopped = true
	}
	rt.state = ChannelStateStopped
	rt.connected = false
	rt.terminal = true
	rt.mu.Unlock()
	for binding := range bindings {
		_ = rt.closeSessionBinding(ctx, binding)
	}
	for generation := range generations {
		if generation.agent != nil {
			_ = generation.agent.Stop(ctx)
		}
	}
	for binding := range bindings {
		binding.signalDone()
	}
	if platform != nil {
		_ = platform.Stop(ctx)
	}
}

// AttachChannel instantiates the channel's platform adapter and starts
// listening. agent may be nil for outbound-only channels (trigger push
// targets); inbound messages then fail with a descriptive reply. Errors from
// CreatePlatform are recorded as an error-state runtime so the console can
// surface them.
func (e *Engine) AttachChannel(ctx context.Context, ch Channel, agent Agent, workDir string, workspace ...WorkspaceInitOptions) error {
	e.DetachChannel(ch.ID)
	opts := WorkspaceInitOptions{AgentID: ch.AgentID, WorkDir: workDir}
	if len(workspace) > 0 {
		opts = workspace[0]
		if opts.AgentID == "" {
			opts.AgentID = ch.AgentID
		}
		if opts.WorkDir == "" {
			opts.WorkDir = workDir
		}
	}

	rt := &channelRuntime{
		owner:           e,
		channel:         ch,
		agent:           agent,
		workDir:         workDir,
		workspace:       opts,
		defaultSettings: opts.RuntimeDefaults,
		generation: &channelAgentGeneration{
			agent: agent, workDir: workDir, workspace: opts, defaultSettings: opts.RuntimeDefaults,
		},
		sessions:        map[string]*channelSessionBinding{},
		retiredSessions: map[*channelSessionBinding]struct{}{},
		seen:            map[string]time.Time{},
		controlTasks:    map[string]*channelControlState{},
		clearConfirm:    map[string]time.Time{},
		threadLists:     map[string][]NativeThread{},
		pendingTurns:    map[string]pendingInitialTurn{},
		state:           ChannelStateRunning,
		started:         time.Now(),
	}
	rt.setMeetingResponseMode(ChannelMeetingResponseMode(ch))
	rt.setDefinitionUpdatedAt(ch.UpdatedAt)

	cfg := make(map[string]any, len(ch.Config)+1)
	for k, v := range ch.Config {
		cfg[k] = v
	}
	cfg["project"] = "channel:" + ch.ID
	cfg["channel_name"] = ch.Name
	cfg["agent_name"] = opts.AgentName
	cfg["meeting_event_notify"] = func(event MeetingEvent) { e.publishMeetingEvent(ch.ID, event) }

	plat, err := CreatePlatform(ch.Type, cfg)
	if err != nil {
		rt.setState(ChannelStateError, err.Error())
		e.mu.Lock()
		e.channels[ch.ID] = rt
		e.mu.Unlock()
		return err
	}
	rt.platform = plat
	if _, ok := plat.(PlatformHealthReporter); ok {
		rt.state = ChannelStateStarting
		rt.connected = false
	} else {
		rt.connected = true
	}

	runCtx, cancel := context.WithCancel(ctx)
	rt.cancel = cancel
	rt.runCtx = runCtx

	e.mu.Lock()
	e.channels[ch.ID] = rt
	e.mu.Unlock()

	// Relay stamps the channel id and origin on inbound messages before they
	// enter the engine loop, so adapters stay channel-agnostic.
	relay := make(chan *Message, 64)
	go func() {
		for {
			select {
			case <-runCtx.Done():
				return
			case msg := <-relay:
				if msg == nil {
					continue
				}
				msg.ChannelID = ch.ID
				if msg.Origin == "" {
					msg.Origin = OriginChannel
				}
				select {
				case e.inbound <- msg:
				case <-runCtx.Done():
					return
				}
			}
		}
	}()

	go func() {
		err := plat.Start(runCtx, relay)
		switch {
		case runCtx.Err() != nil:
			rt.setState(ChannelStateStopped, "")
		case err != nil:
			e.log.Error("channel stopped", "channel", ch.Name, "type", ch.Type, "err", err)
			rt.setState(ChannelStateError, err.Error())
		default:
			rt.setState(ChannelStateStopped, "")
		}
	}()
	if reporter, ok := plat.(PlatformHealthReporter); ok {
		go e.monitorChannelHealth(runCtx, rt, reporter)
	}
	e.recoverRemoteTasks(rt)

	e.log.Info("channel attached", "channel", ch.Name, "type", ch.Type)
	return nil
}

// DetachChannel stops and removes a channel runtime. No-op when absent.
func (e *Engine) DetachChannel(id string) {
	e.mu.Lock()
	rt := e.channels[id]
	delete(e.channels, id)
	e.mu.Unlock()
	if rt != nil {
		rt.close(context.Background())
		e.log.Info("channel detached", "channel", rt.channel.Name)
	}
}

// AttachedChannels returns the currently attached channels keyed by id, with
// the UpdatedAt of the definition each runtime was built from (for reload
// diffing).
func (e *Engine) AttachedChannels() map[string]time.Time {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make(map[string]time.Time, len(e.channels))
	for id, rt := range e.channels {
		out[id] = rt.currentDefinitionUpdatedAt()
	}
	return out
}

// ChannelStatuses reports the live state of all attached channels.
func (e *Engine) ChannelStatuses() []ChannelStatus {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]ChannelStatus, 0, len(e.channels))
	for _, rt := range e.channels {
		out = append(out, rt.status())
	}
	return out
}

func (e *Engine) ChannelCodexControlCapability(channelID string) (CodexControlCapability, bool) {
	rt := e.channelRuntime(channelID)
	if rt == nil {
		return CodexControlCapability{}, false
	}
	agent, _, _ := rt.agentSnapshot()
	reporter, ok := agent.(CodexControlCapabilityReporter)
	if !ok {
		return CodexControlCapability{}, false
	}
	return reporter.CodexControlCapability(), true
}

func (e *Engine) channelRuntime(id string) *channelRuntime {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.channels[id]
}
