package server

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func TestChannelCacheCoalescesAndServesStaleOnFailure(t *testing.T) {
	var cache channelResourceCache[string]
	var calls atomic.Int32
	gate := make(chan struct{})
	fetch := func(context.Context) (string, error) { calls.Add(1); <-gate; return "portrait", nil }
	var viewers sync.WaitGroup
	for i := 0; i < 8; i++ {
		viewers.Add(1)
		go func() {
			defer viewers.Done()
			value, err := cache.get(context.Background(), "bot", 16, fetch)
			if err != nil || value != "portrait" {
				t.Errorf("value=%q err=%v", value, err)
			}
		}()
	}
	close(gate)
	viewers.Wait()
	if calls.Load() != 1 {
		t.Fatalf("fetches = %d", calls.Load())
	}
	cache.mu.Lock()
	cache.entries["bot"].expiresAt = time.Now().Add(-time.Second)
	cache.mu.Unlock()
	refresh := make(chan struct{})
	value, err := cache.get(context.Background(), "bot", 16, func(context.Context) (string, error) {
		<-refresh
		return "", errors.New("upstream offline")
	})
	if err != nil || value != "portrait" {
		t.Fatalf("stale read: value=%q err=%v", value, err)
	}
	cache.mu.Lock()
	pending := cache.entries["bot"].pending
	cache.mu.Unlock()
	close(refresh)
	<-pending
	value, err = cache.get(context.Background(), "bot", 16, fetch)
	if err != nil || value != "portrait" || calls.Load() != 1 {
		t.Fatalf("failed refresh discarded stale cache: value=%q err=%v calls=%d", value, err, calls.Load())
	}
}

func TestChannelIdentityCacheChangesWithCredentials(t *testing.T) {
	channel := core.Channel{Type: "feishu", Config: map[string]string{"app_id": "app", "app_secret": "old"}}
	old := channelIdentityCacheKey(channel)
	channel.Config["app_secret"] = "new"
	if old == channelIdentityCacheKey(channel) {
		t.Fatal("rotated credentials reused old identity")
	}
}
