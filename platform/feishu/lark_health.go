package feishu

import (
	"context"
	"fmt"
	"strings"
	"time"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/wangning19940904/AgentMux/core"
)

func (c *larkClient) beginHealth() {
	now := time.Now()
	c.mu.Lock()
	c.closing = false
	c.healthState = core.ChannelStateStarting
	c.healthError = ""
	c.healthStartedAt = now
	c.connectedAt = time.Time{}
	c.lastHeartbeatAt = time.Time{}
	c.lastEventAt = time.Time{}
	c.lastInboundAt = time.Time{}
	c.mu.Unlock()
}

func (c *larkClient) markReady() {
	now := time.Now()
	c.mu.Lock()
	if !c.closing {
		c.healthState = core.ChannelStateRunning
		c.healthError = ""
		c.connectedAt = now
		// Treat readiness as the initial heartbeat. The server's first pong will
		// replace it before the heartbeat watchdog window expires.
		c.lastHeartbeatAt = now
	}
	c.mu.Unlock()
}

func (c *larkClient) markReconnecting() {
	c.mu.Lock()
	if !c.closing {
		c.healthState = core.ChannelStateReconnecting
		c.healthError = "Feishu WebSocket is reconnecting"
	}
	c.mu.Unlock()
}

func (c *larkClient) markDisconnected() {
	c.mu.Lock()
	if !c.closing {
		c.healthState = core.ChannelStateReconnecting
		c.healthError = "Feishu WebSocket disconnected; waiting for reconnect"
	}
	c.mu.Unlock()
}

func (c *larkClient) markError(err error) {
	c.mu.Lock()
	if !c.closing {
		c.healthState = core.ChannelStateError
		if err != nil {
			c.healthError = err.Error()
		} else {
			c.healthError = "Feishu WebSocket connection failed"
		}
	}
	c.mu.Unlock()
}

func (c *larkClient) markHeartbeat() {
	c.mu.Lock()
	if !c.closing {
		c.lastHeartbeatAt = time.Now()
		if c.healthState == core.ChannelStateDegraded {
			c.healthState = core.ChannelStateRunning
			c.healthError = ""
		}
	}
	c.mu.Unlock()
}

func (c *larkClient) markEvent() {
	c.mu.Lock()
	if !c.closing {
		c.lastEventAt = time.Now()
	}
	c.mu.Unlock()
}

func (c *larkClient) markInbound() {
	c.mu.Lock()
	if !c.closing {
		c.lastInboundAt = time.Now()
	}
	c.mu.Unlock()
}

func (c *larkClient) ChannelHealth() core.PlatformHealth {
	now := time.Now()
	c.mu.Lock()
	health := core.PlatformHealth{
		State:           c.healthState,
		Connected:       c.healthState == core.ChannelStateRunning,
		Error:           c.healthError,
		CheckedAt:       now,
		ConnectedAt:     c.connectedAt,
		LastHeartbeatAt: c.lastHeartbeatAt,
		LastEventAt:     c.lastEventAt,
		LastInboundAt:   c.lastInboundAt,
	}
	startedAt := c.healthStartedAt
	c.mu.Unlock()

	if health.State == "" {
		health.State = core.ChannelStateStarting
	}
	if health.State == core.ChannelStateStarting && !startedAt.IsZero() && now.Sub(startedAt) > larkWSStartupTimeout {
		health.State = core.ChannelStateDegraded
		health.Error = "Feishu WebSocket did not become ready within 45 seconds"
	}
	if health.State == core.ChannelStateRunning {
		lastActivity := health.LastHeartbeatAt
		if health.LastEventAt.After(lastActivity) {
			lastActivity = health.LastEventAt
		}
		if !lastActivity.IsZero() && now.Sub(lastActivity) > larkWSHeartbeatTimeout {
			health.State = core.ChannelStateDegraded
			health.Connected = false
			health.Error = fmt.Sprintf("Feishu WebSocket heartbeat is stale (no pong or event for %s); restart the channel", now.Sub(lastActivity).Round(time.Second))
		}
	}
	return health
}

type larkWSHealthLogger struct {
	client   *larkClient
	delegate larkcore.Logger
}

func (l *larkWSHealthLogger) Debug(ctx context.Context, args ...interface{}) {
	if strings.Contains(fmt.Sprint(args...), "receive pong") {
		l.client.markHeartbeat()
	}
	if l.delegate != nil {
		l.delegate.Debug(ctx, args...)
	}
}

func (l *larkWSHealthLogger) Info(ctx context.Context, args ...interface{}) {
	if l.delegate != nil {
		l.delegate.Info(ctx, args...)
	}
}

func (l *larkWSHealthLogger) Warn(ctx context.Context, args ...interface{}) {
	if l.delegate != nil {
		l.delegate.Warn(ctx, args...)
	}
}

func (l *larkWSHealthLogger) Error(ctx context.Context, args ...interface{}) {
	if l.delegate != nil {
		l.delegate.Error(ctx, args...)
	}
}
