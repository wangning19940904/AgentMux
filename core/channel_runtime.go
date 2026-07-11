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

	mu       sync.Mutex
	sessions map[string]AgentSession // chatID -> session
	seen     map[string]time.Time
	state    string
	errMsg   string
	started  time.Time
}

func (rt *channelRuntime) setState(state, errMsg string) {
	rt.mu.Lock()
	rt.state = state
	rt.errMsg = errMsg
	rt.mu.Unlock()
}

func (rt *channelRuntime) status() ChannelStatus {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return ChannelStatus{
		ChannelID: rt.channel.ID,
		State:     rt.state,
		Error:     rt.errMsg,
		StartedAt: rt.started,
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
func (rt *channelRuntime) session(ctx context.Context, chatID, chatType string) (AgentSession, *Conversation, bool, error) {
	if rt.agent == nil {
		return nil, nil, false, fmt.Errorf("channel %q has no agent bound", rt.channel.Name)
	}
	opts := rt.workspace
	if opts.WorkDir == "" {
		opts.WorkDir = rt.workDir
	}
	conv, workDir, err := rt.owner.prepareConversation(ctx, rt.scope(), chatID, chatType, opts, rt.workDir)
	if err != nil {
		return nil, nil, false, err
	}
	cacheKey := chatID
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
	for _, setting := range []RuntimeSetting{RuntimeSettingModel, RuntimeSettingReasoningEffort, RuntimeSettingServiceTier} {
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
	rt.mu.Unlock()
	for _, s := range sessions {
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

	runCtx, cancel := context.WithCancel(ctx)
	rt.cancel = cancel

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
func (e *Engine) handleChannelMessage(ctx context.Context, msg *Message, data map[string]string) {
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

	sess, conv, created, err := rt.session(ctx, msg.ChatID, msg.ChatType)
	if err != nil {
		e.log.Error("start channel session", "channel", rt.channel.Name, "err", err)
		e.emit(ctx, HookError, withError(data, err))
		if replyErr := rt.platform.Reply(ctx, msg, "failed to start agent session: "+err.Error()); replyErr != nil {
			e.log.Error("channel reply", "channel", rt.channel.Name, "err", replyErr)
		}
		return
	}
	if created {
		e.emit(ctx, HookSessionStarted, data)
	}
	defer e.persistConversationTurn(ctx, conv, sess)
	defaults := rt.runtimeDefaults()
	if e.handleRuntimeSettingsAction(ctx, sess, msg, &defaults, rt.workspace.AgentID, func(state RuntimeSettingsPickerState) bool {
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
		return
	}
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
		return
	}

	mode, ok := channelReplyMode(rt.channel)
	if !ok {
		e.log.Warn("unknown channel reply mode, falling back to stream_message", "channel", rt.channel.Name, "mode", rt.channel.Config[ChannelConfigReplyMode])
	}
	if mode == ReplyModeStreamCard {
		if sr, ok := rt.platform.(StreamReplier); ok {
			e.streamTurnCard(ctx, sr, sess, msg, data)
			e.emit(ctx, HookMessageSent, data)
			return
		}
		e.log.Warn("channel reply mode stream_card not supported, falling back to stream_message", "channel", rt.channel.Name, "type", rt.channel.Type)
	}
	if mr, ok := rt.platform.(StreamMessageReplier); ok {
		e.streamTurnMessage(ctx, mr, sess, msg, data)
		e.emit(ctx, HookMessageSent, data)
		return
	}

	_, _ = e.streamTurn(ctx, sess, msg.Text, func(text string) {
		if err := rt.platform.Reply(ctx, msg, text); err != nil {
			e.log.Error("channel reply", "channel", rt.channel.Name, "err", err)
		}
	}, data)
	e.emit(ctx, HookMessageSent, data)
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
	e.resetConversation(ctx, rt.scope(), msg.ChatID, msg.ChatType, rt.workspace.AgentID, rt.dropSession)
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
