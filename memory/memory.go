// Package memory implements AgentNexus Memory: the unified, cross-agent and
// cross-session memory layer. The default backend stores entries in the
// SQLite SSOT; richer backends (vector stores, remote services) can register
// themselves via core.RegisterMemory under a different name.
package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/agentnexus/agentnexus/core"
	"github.com/agentnexus/agentnexus/store"
)

func init() {
	// The sqlite backend needs the shared store, so the factory here returns a
	// placeholder that newSQLite wires up at server-build time. Registration
	// keeps Memory discoverable via core.RegisteredMemories().
	core.RegisterMemory("sqlite", func(cfg map[string]any) (core.MemoryStore, error) {
		return &SQLiteStore{}, nil
	})
}

// SQLiteStore implements core.MemoryStore on top of the AgentNexus store.
type SQLiteStore struct {
	st *store.Store
}

var _ core.MemoryStore = (*SQLiteStore)(nil)

// New builds a store-backed memory layer.
func New(st *store.Store) *SQLiteStore { return &SQLiteStore{st: st} }

// Name returns the backend id.
func (m *SQLiteStore) Name() string { return "sqlite" }

// Put writes (inserts or updates) an entry and returns its id.
func (m *SQLiteStore) Put(ctx context.Context, e *core.MemoryEntry) (string, error) {
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
func (m *SQLiteStore) Get(ctx context.Context, id string) (*core.MemoryEntry, error) {
	return m.st.GetMemory(ctx, id)
}

// Search returns entries within scope matching query.
func (m *SQLiteStore) Search(ctx context.Context, scope, query string, limit int) ([]*core.MemoryEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	return m.st.SearchMemory(ctx, scope, query, limit)
}

// Delete removes an entry by id.
func (m *SQLiteStore) Delete(ctx context.Context, id string) error {
	return m.st.DeleteMemory(ctx, id)
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
