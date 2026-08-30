package guard

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

func TestPolicyGuardMatchesPriorityAndFallback(t *testing.T) {
	st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "guard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertGuardPolicy(context.Background(), &core.GuardPolicy{
		ID: "deny-shell", Tool: "shell", Action: "execute", Decision: "deny", Priority: 10,
	}); err != nil {
		t.Fatal(err)
	}
	guard := New(st, core.GuardAsk)
	decision, err := guard.Evaluate(context.Background(), &core.GuardRequest{Tool: "shell", Action: "execute"})
	if err != nil || decision != core.GuardDeny {
		t.Fatalf("decision=%q err=%v", decision, err)
	}
	decision, err = guard.Evaluate(context.Background(), &core.GuardRequest{Tool: "file", Action: "read"})
	if err != nil || decision != core.GuardAsk {
		t.Fatalf("fallback=%q err=%v", decision, err)
	}
}
