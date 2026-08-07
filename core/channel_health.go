package core

import (
	"context"
	"time"
)

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
