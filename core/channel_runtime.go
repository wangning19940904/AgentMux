package core

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
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

	mu              sync.Mutex
	sessions        map[string]AgentSession // chatID -> session
	seen            map[string]time.Time
	state           string
	errMsg          string
	started         time.Time
	connected       bool
	connectedAt     time.Time
	lastCheckedAt   time.Time
	lastHeartbeatAt time.Time
	lastEventAt     time.Time
	lastInboundAt   time.Time
	terminal        bool

	controlMu    sync.Mutex
	controlTasks map[string]*channelControlState
	clearConfirm map[string]time.Time
	threadLists  map[string][]NativeThread
	pendingTurns map[string]pendingInitialTurn
}

type pendingInitialTurn struct {
	message         *Message
	requiredSetting RuntimeSetting
}

func (rt *channelRuntime) setState(state, errMsg string) {
	rt.mu.Lock()
	rt.state = state
	rt.errMsg = errMsg
	rt.connected = false
	rt.terminal = true
	rt.mu.Unlock()
}

func (rt *channelRuntime) applyHealth(health PlatformHealth) (previous string, changed bool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.terminal {
		return rt.state, false
	}
	previous = rt.state
	state := health.State
	switch state {
	case ChannelStateStarting, ChannelStateRunning, ChannelStateReconnecting, ChannelStateDegraded, ChannelStateError:
	default:
		if health.Connected {
			state = ChannelStateRunning
		} else {
			state = ChannelStateDegraded
		}
	}
	rt.state = state
	rt.connected = health.Connected
	rt.errMsg = health.Error
	rt.connectedAt = health.ConnectedAt
	rt.lastCheckedAt = health.CheckedAt
	rt.lastHeartbeatAt = health.LastHeartbeatAt
	rt.lastEventAt = health.LastEventAt
	rt.lastInboundAt = health.LastInboundAt
	return previous, previous != state
}

func (rt *channelRuntime) status() ChannelStatus {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return ChannelStatus{
		ChannelID:       rt.channel.ID,
		State:           rt.state,
		Connected:       rt.connected,
		Error:           rt.errMsg,
		StartedAt:       rt.started,
		ConnectedAt:     rt.connectedAt,
		LastCheckedAt:   rt.lastCheckedAt,
		LastHeartbeatAt: rt.lastHeartbeatAt,
		LastEventAt:     rt.lastEventAt,
		LastInboundAt:   rt.lastInboundAt,
	}
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

	cfg := make(map[string]any, len(ch.Config)+1)
	for k, v := range ch.Config {
		cfg[k] = v
	}
	cfg["project"] = "channel:" + ch.ID

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
				msg.Origin = OriginChannel
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

func (e *Engine) monitorChannelHealth(ctx context.Context, rt *channelRuntime, reporter PlatformHealthReporter) {
	check := func() {
		health := reporter.ChannelHealth()
		if health.CheckedAt.IsZero() {
			health.CheckedAt = time.Now()
		}
		previous, changed := rt.applyHealth(health)
		if !changed {
			return
		}
		state := rt.status().State
		unhealthy := state == ChannelStateReconnecting || state == ChannelStateDegraded || state == ChannelStateError
		if unhealthy {
			errMsg := health.Error
			if errMsg == "" {
				errMsg = "channel connection is " + state
			}
			e.log.Warn("channel health warning", "channel", rt.channel.Name, "type", rt.channel.Type, "state", state, "err", errMsg)
			e.emit(context.Background(), HookError, map[string]string{
				"channel_id": rt.channel.ID,
				"channel":    rt.channel.Name,
				"platform":   rt.channel.Type,
				"origin":     "channel_health",
				"state":      state,
				"error":      errMsg,
			})
		} else if state == ChannelStateRunning && previous != ChannelStateRunning {
			e.log.Info("channel health recovered", "channel", rt.channel.Name, "type", rt.channel.Type)
		}
	}

	check()
	ticker := time.NewTicker(channelHealthCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check()
		}
	}
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
		out[id] = rt.channel.UpdatedAt
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

func (e *Engine) duplicateChannelMessage(msg *Message) bool {
	rt := e.channelRuntime(msg.ChannelID)
	return rt != nil && rt.duplicateMessage(msg)
}

// handleChannelMessage routes an inbound message from an attached channel to
// the bound agent and streams responses back through the channel's platform.
func (e *Engine) handleChannelMessageDirect(ctx context.Context, msg *Message, data map[string]string) {
	rt := e.channelRuntime(msg.ChannelID)
	if rt == nil {
		e.log.Warn("no runtime for channel message", "channel_id", msg.ChannelID)
		return
	}

	if e.handleConversationCommand(ctx, rt, msg) {
		e.emit(ctx, HookMessageSent, data)
		return
	}

	reactionID := ""
	if msg.RuntimeSettingsAction == nil {
		reactionID = e.addChannelAckReaction(ctx, rt, msg)
		defer e.deleteChannelAckReaction(ctx, rt, msg, reactionID)
	}

	sess, conv, created, err := rt.session(ctx, msg)
	if err != nil {
		e.log.Error("start channel session", "channel", rt.channel.Name, "err", err)
		e.emit(ctx, HookError, withError(data, err))
		if replyErr := rt.platform.Reply(ctx, msg, "failed to start agent session: "+err.Error()); replyErr != nil {
			e.log.Error("channel reply", "channel", rt.channel.Name, "err", replyErr)
		}
		return
	}
	data["agent_id"] = rt.workspace.AgentID
	data["runtime_id"] = rt.workspace.RuntimeID
	if rt.agent != nil {
		data["agent_name"] = rt.agent.Name()
	}
	data["session_id"] = sessionObservationID(sess)
	if conv != nil {
		data["conversation_id"] = conv.ID
	}
	rt.attachRemoteSession(ResolveConversationKey(msg), sess, conv)
	rt.decorateRemoteTaskData(ResolveConversationKey(msg), data)
	if created {
		e.emit(ctx, HookSessionStarted, data)
	}
	defer e.persistConversationTurn(ctx, conv, sess)
	defaults := rt.runtimeDefaults()
	actionApplied := false
	if e.handleRuntimeSettingsAction(ctx, sess, msg, &defaults, rt.workspace.AgentID, func(state RuntimeSettingsPickerState) bool {
		actionApplied = state.Notice == ""
		picker, ok := rt.platform.(RuntimeSettingsPickerReplier)
		if !ok {
			return false
		}
		if err := picker.UpdateRuntimeSettingsPicker(ctx, msg, state); err != nil {
			e.log.Error("channel runtime settings picker update", "channel", rt.channel.Name, "err", err)
			return false
		}
		return true
	}, func(text string) {
		if err := rt.platform.Reply(ctx, msg, text); err != nil {
			e.log.Error("channel runtime settings reply", "channel", rt.channel.Name, "err", err)
		}
	}) {
		if msg.RuntimeSettingsAction != nil && msg.RuntimeSettingsAction.Scope == RuntimeSettingsScopeAgent {
			rt.setRuntimeDefaults(defaults)
		}
		e.emit(ctx, HookMessageSent, data)
		if actionApplied {
			if pending := rt.takePendingInitialTurn(msg, *msg.RuntimeSettingsAction); pending != nil {
				e.handleChannelMessage(ctx, pending, eventData(pending))
			}
		}
		return
	}
	settingsCommand, settingsCommandParsed := parseRuntimeSettingsCommand(msg.Text)
	if e.handleRuntimeSettingsCommand(sess, msg.Text, func(text string) {
		if err := rt.platform.Reply(ctx, msg, text); err != nil {
			e.log.Error("channel reply", "channel", rt.channel.Name, "err", err)
		}
	}, func(state RuntimeSettingsPickerState) bool {
		picker, ok := rt.platform.(RuntimeSettingsPickerReplier)
		if !ok {
			return false
		}
		state.AgentDefaultsEditable = rt.workspace.AgentID != "" && !strings.HasPrefix(rt.workspace.AgentID, "config:") && e.runtimeSettingsDefaults != nil
		state.RuntimeDefaults = defaults
		if err := picker.ReplyRuntimeSettingsPicker(ctx, msg, state); err != nil {
			e.log.Error("channel runtime settings picker reply", "channel", rt.channel.Name, "err", err)
			return false
		}
		return true
	}, func(state ModelPickerState) bool {
		mp, ok := rt.platform.(ModelPickerReplier)
		if !ok {
			return false
		}
		if err := mp.ReplyModelPicker(ctx, msg, state); err != nil {
			e.log.Error("channel model picker reply", "channel", rt.channel.Name, "err", err)
			return false
		}
		return true
	}) {
		e.emit(ctx, HookMessageSent, data)
		if settingsCommandParsed && runtimeSettingsCommandApplied(sess, settingsCommand) {
			if pending := rt.takePendingInitialTurn(msg, RuntimeSettingsAction{
				Scope: RuntimeSettingsScopeConversation, Setting: settingsCommand.Setting,
			}); pending != nil {
				e.handleChannelMessage(ctx, pending, eventData(pending))
			}
		}
		return
	}
	if created && conv != nil && conv.MessageCount == 0 {
		setting, required := rt.initialRuntimeConfigurationSetting(sess)
		if required {
			rt.storePendingInitialTurn(msg, setting)
		}
		if required && rt.promptInitialRuntimeConfiguration(ctx, sess, msg, setting) {
			e.emit(ctx, HookMessageSent, data)
			return
		}
		if required {
			rt.discardPendingInitialTurn(msg)
		}
	}

	agentMsg := channelMessageForAgent(rt.channel, msg)
	mode, ok := channelReplyMode(rt.channel)
	if rt.remoteControlEnabled() && data["task_id"] != "" && isFeishuLikeChannel(rt.channel.Type) {
		// Codex remote-control tasks always use one durable status card in
		// Feishu/Lark. The classic reply_mode remains unchanged for ordinary
		// channels and runtime/model control messages.
		mode, ok = ReplyModeStreamCard, true
	}
	if !ok {
		e.log.Warn("unknown channel reply mode, falling back to stream_message", "channel", rt.channel.Name, "mode", rt.channel.Config[ChannelConfigReplyMode])
	}
	if mode == ReplyModeStreamCard {
		if sr, ok := rt.platform.(StreamReplier); ok {
			e.streamTurnCard(ctx, sr, sess, agentMsg, data)
			e.emit(ctx, HookMessageSent, data)
			return
		}
		e.log.Warn("channel reply mode stream_card not supported, falling back to stream_message", "channel", rt.channel.Name, "type", rt.channel.Type)
	}
	if mr, ok := rt.platform.(StreamMessageReplier); ok {
		e.streamTurnMessage(ctx, mr, sess, agentMsg, data)
		e.emit(ctx, HookMessageSent, data)
		return
	}

	_, _ = e.streamTurn(ctx, sess, agentMsg.Text, func(text string) {
		if err := rt.platform.Reply(ctx, msg, text); err != nil {
			e.log.Error("channel reply", "channel", rt.channel.Name, "err", err)
		}
	}, data)
	e.emit(ctx, HookMessageSent, data)
}

func (rt *channelRuntime) initialRuntimeConfigurationSetting(sess AgentSession) (RuntimeSetting, bool) {
	settings, ok := RuntimeSettingsForSession(sess)
	if !ok {
		return "", false
	}
	caps := settings.RuntimeSettingsCapabilities()
	if strings.TrimSpace(settings.CurrentRuntimeSettings().Model) == "" && len(caps.Models) > 1 {
		return RuntimeSettingModel, true
	}
	// Approval mode is owned by the bound Agent. Only ask when the Agent has no
	// usable default; legacy channel-level approval_mode values are ignored.
	agentDefault := strings.TrimSpace(rt.defaultSettings.ApprovalMode)
	if len(caps.ApprovalModes) > 1 && (agentDefault == "" || !runtimeOptionContains(caps.ApprovalModes, agentDefault)) {
		return RuntimeSettingApprovalMode, true
	}
	return "", false
}

func (rt *channelRuntime) promptInitialRuntimeConfiguration(ctx context.Context, sess AgentSession, msg *Message, setting RuntimeSetting) bool {
	settings, ok := RuntimeSettingsForSession(sess)
	if !ok {
		return false
	}
	state := runtimeSettingsPickerState(settings, RuntimeSettingsScopeConversation, RuntimeSettings{}, false)
	command := "/approval <模式>"
	if setting == RuntimeSettingModel {
		state.Notice = "这是新工作目录的首次对话。请先选择模型；选择后将自动继续刚才的消息。"
		command = "/model <模型>"
	} else {
		state.Notice = "这是新工作目录的首次对话。请先确认审批模式；选择后将自动继续刚才的消息。"
	}
	if picker, ok := rt.platform.(RuntimeSettingsPickerReplier); ok {
		if err := picker.ReplyRuntimeSettingsPicker(ctx, msg, state); err == nil {
			return true
		} else {
			rt.owner.log.Warn("reply first-workspace runtime settings picker", "channel", rt.channel.Name, "setting", setting, "err", err)
		}
	}
	values := runtimeOptionValues(settings.RuntimeSettingsCapabilities().Options(setting))
	text := state.Notice + "\n可选值：" + strings.Join(values, ", ") + "\n发送 " + command + " 完成配置。"
	if err := rt.platform.Reply(ctx, msg, text); err != nil {
		rt.owner.log.Error("channel runtime settings configuration reply", "channel", rt.channel.Name, "setting", setting, "err", err)
	}
	return true
}

func (rt *channelRuntime) storePendingInitialTurn(msg *Message, requiredSetting RuntimeSetting) {
	if rt == nil || msg == nil {
		return
	}
	key := ResolveConversationKey(msg)
	if key == "" {
		return
	}
	clone := *msg
	if len(msg.Images) > 0 {
		clone.Images = make([][]byte, len(msg.Images))
		for i, image := range msg.Images {
			clone.Images[i] = append([]byte(nil), image...)
		}
	}
	clone.RuntimeSettingsAction = nil
	clone.AgentInteractionAction = nil
	clone.InteractionMessageID = ""
	clone.Callback = nil
	clone.LogOnly = false

	rt.mu.Lock()
	if rt.pendingTurns == nil {
		rt.pendingTurns = map[string]pendingInitialTurn{}
	}
	rt.pendingTurns[key] = pendingInitialTurn{message: &clone, requiredSetting: requiredSetting}
	rt.mu.Unlock()
}

func (rt *channelRuntime) discardPendingInitialTurn(msg *Message) {
	if rt == nil || msg == nil {
		return
	}
	key := ResolveConversationKey(msg)
	rt.mu.Lock()
	pending, ok := rt.pendingTurns[key]
	if ok && pending.message != nil && pending.message.ID == msg.ID {
		delete(rt.pendingTurns, key)
	}
	rt.mu.Unlock()
}

func (rt *channelRuntime) takePendingInitialTurn(msg *Message, action RuntimeSettingsAction) *Message {
	if rt == nil || msg == nil || action.Setting == RuntimeSettingScope {
		return nil
	}
	if action.Scope != "" && action.Scope != RuntimeSettingsScopeConversation {
		return nil
	}
	key := ResolveConversationKey(msg)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	pending, ok := rt.pendingTurns[key]
	if !ok || pending.requiredSetting != action.Setting {
		return nil
	}
	delete(rt.pendingTurns, key)
	return pending.message
}

func runtimeSettingsCommandApplied(sess AgentSession, command runtimeSettingsCommand) bool {
	if command.List {
		return false
	}
	settings, ok := RuntimeSettingsForSession(sess)
	if !ok || !settings.RuntimeSettingsCapabilities().Supports(command.Setting) {
		return false
	}
	current := settings.CurrentRuntimeSettings().Value(command.Setting)
	if command.Reset {
		return current == settings.DefaultRuntimeSettings().Value(command.Setting)
	}
	return current == command.Value
}

func (e *Engine) addChannelAckReaction(ctx context.Context, rt *channelRuntime, msg *Message) string {
	if rt == nil || msg == nil || msg.ID == "" || !channelAckReactionEnabled(rt.channel) {
		return ""
	}
	reacter, ok := rt.platform.(MessageReactioner)
	if !ok {
		return ""
	}
	emoji := chooseAckReactionEmoji(rt.channel)
	if emoji == "" {
		return ""
	}
	reactionID, err := reacter.AddReaction(ctx, msg, emoji)
	if err != nil {
		e.log.Warn("add channel ack reaction", "channel", rt.channel.Name, "message_id", msg.ID, "emoji", emoji, "err", err)
		return ""
	}
	return reactionID
}

func (e *Engine) deleteChannelAckReaction(ctx context.Context, rt *channelRuntime, msg *Message, reactionID string) {
	if reactionID == "" || rt == nil || msg == nil {
		return
	}
	reacter, ok := rt.platform.(MessageReactioner)
	if !ok {
		return
	}
	if err := reacter.DeleteReaction(ctx, msg, reactionID); err != nil {
		e.log.Warn("delete channel ack reaction", "channel", rt.channel.Name, "message_id", msg.ID, "reaction_id", reactionID, "err", err)
	}
}

// handleConversationCommand intercepts control commands like /new and /clear
// that end the active conversation for a chat (soft delete) so the next
// message starts fresh. It reports whether the message was a command and was
// handled (and thus should not be forwarded to the agent).
func (e *Engine) handleConversationCommand(ctx context.Context, rt *channelRuntime, msg *Message) bool {
	if !isConversationCommand(msg.Text) {
		return false
	}
	e.resetConversation(ctx, rt.scope(), msg.ChatID, msg.ChatType, ResolveConversationKey(msg), rt.workspace.AgentID, rt.dropSession)
	if replyErr := rt.platform.Reply(ctx, msg, conversationResetReply); replyErr != nil {
		e.log.Error("channel reply", "channel", rt.channel.Name, "err", replyErr)
	}
	return true
}

func isFeishuLikeChannel(typ string) bool {
	return typ == "feishu" || typ == "lark"
}

func channelReplyScope(ch Channel) string {
	switch strings.TrimSpace(ch.Config[ChannelConfigReplyScope]) {
	case ReplyScopeAll:
		return ReplyScopeAll
	case ReplyScopeMentionsOnly:
		return ReplyScopeMentionsOnly
	default:
		return ReplyScopeDMAndMentions
	}
}

func channelReplyMode(ch Channel) (string, bool) {
	switch strings.TrimSpace(ch.Config[ChannelConfigReplyMode]) {
	case "", ReplyModeStreamMessage:
		return ReplyModeStreamMessage, true
	case ReplyModeStreamCard:
		return ReplyModeStreamCard, true
	default:
		return ReplyModeStreamMessage, false
	}
}

func channelAckReactionEnabled(ch Channel) bool {
	if !isFeishuLikeChannel(ch.Type) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(ch.Config[ChannelConfigAckReaction])) {
	case "", "true", "1", "yes", "on":
		return true
	default:
		return false
	}
}

func chooseAckReactionEmoji(ch Channel) string {
	raw := strings.TrimSpace(ch.Config[ChannelConfigAckReactionEmojis])
	if raw == "" {
		raw = DefaultAckReactionEmojis
	}
	parts := strings.Split(raw, ",")
	emojis := make([]string, 0, len(parts))
	for _, part := range parts {
		if emoji := strings.TrimSpace(part); emoji != "" {
			emojis = append(emojis, emoji)
		}
	}
	if len(emojis) == 0 {
		return ""
	}
	return emojis[rand.Intn(len(emojis))]
}
