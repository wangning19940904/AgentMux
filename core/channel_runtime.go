package core

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

const channelMessageDedupTTL = 10 * time.Minute

var channelHealthCheckInterval = 15 * time.Second

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
	sessions            map[string]AgentSession // chatID -> session
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
	rt.mu.Unlock()
}

// scope returns the conversation scope namespace for this channel.
func (rt *channelRuntime) scope() string { return "channel:" + rt.channel.ID }

// session returns the agent session for chatID, creating one when needed. It
// also returns the durable conversation (nil when no conversation store is
// attached) so callers can persist turn activity and native session ids.
func (rt *channelRuntime) session(ctx context.Context, msg *Message) (AgentSession, *Conversation, bool, error) {
	if rt.agent == nil {
		return nil, nil, false, fmt.Errorf("channel %q has no agent bound", rt.channel.Name)
	}
	if msg == nil {
		return nil, nil, false, fmt.Errorf("channel message is required")
	}
	opts := rt.workspace
	if opts.WorkDir == "" {
		opts.WorkDir = rt.workDir
	}
	conversationKey := ResolveConversationKey(msg)
	conv, workDir, err := rt.owner.prepareConversation(ctx, rt.scope(), msg.ChatID, msg.ChatType, conversationKey, opts, rt.workDir)
	if err != nil {
		return nil, nil, false, err
	}
	cacheKey := conversationKey
	if conv != nil {
		cacheKey = conv.ID
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()
	if s, ok := rt.sessions[cacheKey]; ok {
		return s, conv, false, nil
	}
	s, err := rt.owner.startAgentSession(ctx, rt.agent, workDir, conv)
	if err != nil {
		return nil, nil, false, err
	}
	// The live Agent object was created when the channel attached. Apply the
	// persisted Agent defaults to each newly created conversation so a settings
	// card change affects future sessions without restarting the channel, while
	// leaving already cached sessions untouched.
	rt.applyRuntimeDefaults(s)
	rt.sessions[cacheKey] = s
	return s, conv, true, nil
}

func (rt *channelRuntime) applyRuntimeDefaults(sess AgentSession) {
	settings, ok := RuntimeSettingsForSession(sess)
	if !ok {
		return
	}
	defaults := rt.defaultSettings
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

// dropSession closes and removes the cached in-memory session for cacheKey
// (the conversation id). No-op when absent.
func (rt *channelRuntime) dropSession(ctx context.Context, cacheKey string) {
	rt.mu.Lock()
	s, ok := rt.sessions[cacheKey]
	if ok {
		delete(rt.sessions, cacheKey)
	}
	rt.mu.Unlock()
	if ok && s != nil {
		data := map[string]string{
			"channel_id": rt.channel.ID, "agent_id": rt.workspace.AgentID, "runtime_id": rt.workspace.RuntimeID,
			"session_id": sessionObservationID(s), "conversation_id": cacheKey,
		}
		if rt.agent != nil {
			data["agent_name"] = rt.agent.Name()
		}
		rt.owner.emit(ctx, HookSessionEnded, data)
		_ = s.Close(ctx)
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
	sessions := rt.sessions
	rt.sessions = map[string]AgentSession{}
	platform := rt.platform
	agent := rt.agent
	rt.state = ChannelStateStopped
	rt.connected = false
	rt.terminal = true
	rt.mu.Unlock()
	for cacheKey, s := range sessions {
		data := map[string]string{
			"channel_id": rt.channel.ID, "agent_id": rt.workspace.AgentID, "runtime_id": rt.workspace.RuntimeID,
			"session_id": sessionObservationID(s), "conversation_id": cacheKey,
		}
		if agent != nil {
			data["agent_name"] = agent.Name()
		}
		rt.owner.emit(ctx, HookSessionEnded, data)
		_ = s.Close(ctx)
	}
	if agent != nil {
		_ = agent.Stop(ctx)
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
		sessions:        map[string]AgentSession{},
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
	if rt == nil || rt.agent == nil {
		return CodexControlCapability{}, false
	}
	reporter, ok := rt.agent.(CodexControlCapabilityReporter)
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
