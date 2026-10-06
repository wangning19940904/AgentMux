package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// Channel identity and portraits change infrequently. Retain the last good
// value while refreshing, and coalesce concurrent list/avatar requests.
type channelResourceCache[T any] struct {
	mu      sync.Mutex
	entries map[string]*channelCacheEntry[T]
}

type channelCacheEntry[T any] struct {
	value     T
	hasValue  bool
	updatedAt time.Time
	expiresAt time.Time
	pending   chan struct{}
	err       error
}

func (c *channelResourceCache[T]) get(ctx context.Context, key string, limit int, fetch func(context.Context) (T, error)) (T, error) {
	c.mu.Lock()
	if c.entries == nil {
		c.entries = make(map[string]*channelCacheEntry[T])
	}
	entry := c.entries[key]
	if entry == nil {
		if len(c.entries) >= limit {
			for oldKey, old := range c.entries {
				if old.pending == nil {
					delete(c.entries, oldKey)
					break
				}
			}
		}
		entry = &channelCacheEntry[T]{}
		c.entries[key] = entry
	}
	now := time.Now()
	usable := entry.hasValue && now.Sub(entry.updatedAt) < time.Hour
	if now.Before(entry.expiresAt) && entry.pending == nil {
		value, err := entry.value, entry.err
		if usable {
			err = nil
		}
		c.mu.Unlock()
		return value, err
	}
	if entry.pending == nil {
		entry.pending = make(chan struct{})
		go func() {
			// Shared refresh must survive a particular viewer navigating away.
			refreshCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			value, err := fetch(refreshCtx)
			c.mu.Lock()
			defer c.mu.Unlock()
			entry.err = err
			entry.expiresAt = time.Now().Add(30 * time.Second)
			if err == nil {
				entry.value, entry.hasValue, entry.updatedAt = value, true, time.Now()
				entry.expiresAt = time.Now().Add(5 * time.Minute)
			}
			close(entry.pending)
			entry.pending = nil
		}()
	}
	if usable {
		value := entry.value
		c.mu.Unlock()
		return value, nil
	}
	pending := entry.pending
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	case <-pending:
		c.mu.Lock()
		defer c.mu.Unlock()
		return entry.value, entry.err
	}
}

func channelIdentityCacheKey(ch core.Channel) string {
	// Credential changes select a new entry without retaining secrets as keys.
	digest := sha256.Sum256([]byte(ch.Type + "\x00" + ch.Config["app_id"] + "\x00" + ch.Config["app_secret"]))
	return hex.EncodeToString(digest[:])
}

type channelAvatarData struct {
	contentType string
	body        []byte
}
