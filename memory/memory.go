// Package memory implements AgentMux Memory: the unified, cross-agent and
// cross-session memory layer. The default backend stores entries in the
// PostgreSQL SSOT; richer backends (vector stores, remote services) can register
// themselves via core.RegisterMemory under a different name.
package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

func init() {
	// The PostgreSQL backend needs the shared store, so the factory returns a
	// placeholder that New wires up at server-build time. Registration
	// keeps Memory discoverable via core.RegisteredMemories().
	core.RegisterMemory("postgres", func(cfg map[string]any) (core.MemoryStore, error) {
		return &PostgreSQLStore{}, nil
	})
}

// PostgreSQLStore implements core.MemoryStore on top of the AgentMux store.
type PostgreSQLStore struct {
	st *store.Store
}

var _ core.MemoryStore = (*PostgreSQLStore)(nil)

// New builds a store-backed memory layer.
func New(st *store.Store) *PostgreSQLStore { return &PostgreSQLStore{st: st} }

// Name returns the backend id.
func (m *PostgreSQLStore) Name() string { return "postgres" }

// Put writes (inserts or updates) an entry and returns its id.
func (m *PostgreSQLStore) Put(ctx context.Context, e *core.MemoryEntry) (string, error) {
	now := time.Now()
	if e.ID == "" {
		e.ID = newID()
		e.CreatedAt = now
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = now
	}
	e.UpdatedAt = now
	if e.Scope == "" {
		e.Scope = "global"
	}
	if err := m.st.PutMemory(ctx, e); err != nil {
		return "", err
	}
	return e.ID, nil
}

// Get fetches a single entry by id.
func (m *PostgreSQLStore) Get(ctx context.Context, id string) (*core.MemoryEntry, error) {
	return m.st.GetMemory(ctx, id)
}

// Search returns entries within scope matching query.
func (m *PostgreSQLStore) Search(ctx context.Context, scope, query string, limit int) ([]*core.MemoryEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	return m.st.SearchMemory(ctx, scope, query, limit)
}

// Delete removes an entry by id.
func (m *PostgreSQLStore) Delete(ctx context.Context, id string) error {
	return m.st.DeleteMemory(ctx, id)
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
