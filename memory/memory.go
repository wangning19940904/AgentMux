// Package memory implements AgentMux Memory: the unified, cross-agent and
// cross-session memory layer. The default backend stores entries in the
// PostgreSQL SSOT; richer backends (vector stores, remote services) can register
// themselves by implementing the consumer-owned Repository/MemoryStore ports.
package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

type Repository interface {
	PutMemory(context.Context, *core.MemoryEntry) error
	GetMemory(context.Context, string) (*core.MemoryEntry, error)
	SearchMemory(context.Context, string, string, int) ([]*core.MemoryEntry, error)
	DeleteMemory(context.Context, string) error
}

// PostgreSQLStore implements core.MemoryStore on top of the AgentMux store.
type PostgreSQLStore struct {
	st Repository
}

var _ core.MemoryStore = (*PostgreSQLStore)(nil)

// New builds a store-backed memory layer.
func New(st Repository) *PostgreSQLStore { return &PostgreSQLStore{st: st} }

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
