package memory

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

func TestPostgreSQLStoreRoundTrip(t *testing.T) {
	st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	memory := New(st)
	id, err := memory.Put(context.Background(), &core.MemoryEntry{Scope: "agent:a", Content: "remember me"})
	if err != nil || id == "" {
		t.Fatalf("Put id=%q err=%v", id, err)
	}
	items, err := memory.Search(context.Background(), "agent:a", "remember", 20)
	if err != nil || len(items) != 1 || items[0].ID != id {
		t.Fatalf("Search items=%+v err=%v", items, err)
	}
	if err := memory.Delete(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}
