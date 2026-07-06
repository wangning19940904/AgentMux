package core

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// channelRuntime holds one live console-managed channel: the platform
// connection, the bound agent and its per-chat sessions.
type channelRuntime struct {
	channel  Channel
	platform Platform
	agent    Agent
	workDir  string
	cancel   context.CancelFunc

	mu       sync.Mutex
	sessions map[string]AgentSession // chatID -> session
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

// session returns the agent session for chatID, creating one when needed.
func (rt *channelRuntime) session(ctx context.Context, chatID string) (AgentSession, bool, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if s, ok := rt.sessions[chatID]; ok {
		return s, false, nil
	}
	if rt.agent == nil {
		return nil, false, fmt.Errorf("channel %q has no agent bound", rt.channel.Name)
	}
	s, err := rt.agent.StartSession(ctx, rt.workDir)
	if err != nil {
		return nil, false, err
	}
	rt.sessions[chatID] = s
	return s, true, nil
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
func (e *Engine) AttachChannel(ctx context.Context, ch Channel, agent Agent, workDir string) error {
	e.DetachChannel(ch.ID)

	rt := &channelRuntime{
		channel:  ch,
		agent:    agent,
		workDir:  workDir,
		sessions: map[string]AgentSession{},
		state:    ChannelStateRunning,
		started:  time.Now(),
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

// handleChannelMessage routes an inbound message from an attached channel to
// the bound agent and streams responses back through the channel's platform.
func (e *Engine) handleChannelMessage(ctx context.Context, msg *Message, data map[string]string) {
	rt := e.channelRuntime(msg.ChannelID)
	if rt == nil {
		e.log.Warn("no runtime for channel message", "channel_id", msg.ChannelID)
		return
	}

	sess, created, err := rt.session(ctx, msg.ChatID)
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

	_, _ = e.streamTurn(ctx, sess, msg.Text, func(text string) {
		if err := rt.platform.Reply(ctx, msg, text); err != nil {
			e.log.Error("channel reply", "channel", rt.channel.Name, "err", err)
		}
	}, data)
	e.emit(ctx, HookMessageSent, data)
}
