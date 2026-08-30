package mcp

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

func TestRegistryRoundTripAndValidation(t *testing.T) {
	st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	registry := New(st)
	if err := registry.Upsert(context.Background(), &core.MCPServer{Name: ""}); err == nil {
		t.Fatal("expected empty name validation")
	}
	definition := &core.MCPServer{Name: "files", Command: "npx", Enabled: true}
	if err := registry.Upsert(context.Background(), definition); err != nil {
		t.Fatal(err)
	}
	items, err := registry.List(context.Background())
	if err != nil || len(items) != 1 || items[0].Transport != "stdio" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if err := registry.Delete(context.Background(), "files"); err != nil {
		t.Fatal(err)
	}
}
