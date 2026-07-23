package core

import (
	"context"
	"sync"
	"testing"
	"time"
)

type healthTestPlatform struct {
	*fakePlatform

	healthMu sync.Mutex
	health   PlatformHealth
}

func (p *healthTestPlatform) ChannelHealth() PlatformHealth {
	p.healthMu.Lock()
	defer p.healthMu.Unlock()
	return p.health
}

func (p *healthTestPlatform) setHealth(health PlatformHealth) {
	p.healthMu.Lock()
	p.health = health
	p.healthMu.Unlock()
}

func TestChannelHealthMonitorDetectsFailureAndRecovery(t *testing.T) {
	oldInterval := channelHealthCheckInterval
	channelHealthCheckInterval = 10 * time.Millisecond
	t.Cleanup(func() { channelHealthCheckInterval = oldInterval })

	now := time.Now()
	platform := &healthTestPlatform{
		fakePlatform: newFakePlatform("health-test"),
		health: PlatformHealth{
			State:     ChannelStateReconnecting,
			Connected: false,
			Error:     "socket disconnected",
			CheckedAt: now,
		},
	}
	restore := stubPlatformFactory(t, "health-test", platform)
	defer restore()

	engine := NewEngine(nil, NewHookRunner(nil, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	channel := Channel{ID: "health-c1", Name: "health channel", Type: "health-test", Enabled: true}
	if err := engine.AttachChannel(ctx, channel, nil, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.DetachChannel(channel.ID) })

	waitFor(t, "disconnected channel health", func() bool {
		statuses := engine.ChannelStatuses()
		return len(statuses) == 1 && statuses[0].State == ChannelStateReconnecting &&
			!statuses[0].Connected && statuses[0].Error == "socket disconnected" &&
			statuses[0].LastCheckedAt.Equal(now)
	})

	heartbeatAt := time.Now()
	platform.setHealth(PlatformHealth{
		State:           ChannelStateRunning,
		Connected:       true,
		CheckedAt:       heartbeatAt,
		ConnectedAt:     heartbeatAt,
		LastHeartbeatAt: heartbeatAt,
	})
	waitFor(t, "recovered channel health", func() bool {
		statuses := engine.ChannelStatuses()
		return len(statuses) == 1 && statuses[0].State == ChannelStateRunning &&
			statuses[0].Connected && statuses[0].Error == "" &&
			statuses[0].LastHeartbeatAt.Equal(heartbeatAt)
	})
}
