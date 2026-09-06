package bootstrap

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/internal/traeauth"
)

type authStoreStub struct{ agents []core.AgentInstance }

func (s authStoreStub) ListAgentInstances(context.Context) ([]core.AgentInstance, error) {
	return s.agents, nil
}

func TestAuthMaintenanceTargetsEnabledLocalTraeAgentsAndLogsTransitions(t *testing.T) {
	st := authStoreStub{agents: []core.AgentInstance{
		{ID: "active", RuntimeID: "traecli", Enabled: true, Env: map[string]string{"TRAE_HOME": "/custom/trae"}},
		{ID: "disabled", RuntimeID: "traecli"},
		{ID: "provider", RuntimeID: "traecli", Enabled: true, ProviderID: "provider"},
		{ID: "codex", RuntimeID: "codex", Enabled: true},
	}}
	var output bytes.Buffer
	log := slog.New(slog.NewTextHandler(&output, nil))
	previous := make(map[string]string)
	calls := 0
	failure := traeauth.ErrLoginRequired
	ensure := func(ctx context.Context, env map[string]string) error {
		calls++
		if env["TRAE_HOME"] != "/custom/trae" {
			t.Fatal("wrong credential scope")
		}
		return failure
	}
	for range 2 {
		refreshAgentAuth(context.Background(), st, log, previous, ensure)
	}
	if calls != 2 || strings.Count(output.String(), "requires attention") != 1 {
		t.Fatalf("calls=%d logs=%s", calls, output.String())
	}
	failure = nil
	refreshAgentAuth(context.Background(), st, log, previous, ensure)
	if len(previous) != 0 || strings.Count(output.String(), "renewal recovered") != 1 {
		t.Fatalf("recovery: %v %s", previous, output.String())
	}
}

func TestRuntimeStopJoinsAuthMaintenance(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &Runtime{ctx: ctx, cancel: cancel}
	finished := make(chan struct{})
	r.authWG.Add(1)
	go func() { defer r.authWG.Done(); <-ctx.Done(); close(finished) }()
	r.Stop()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("auth maintenance was not stopped")
	}
}
