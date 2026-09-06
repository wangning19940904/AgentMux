package bootstrap

import (
	"context"
	"log/slog"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/internal/traeauth"
)

type authAgentStore interface {
	ListAgentInstances(context.Context) ([]core.AgentInstance, error)
}

func maintainFrameworkAuth(ctx context.Context, st authAgentStore, log *slog.Logger) {
	ticker := time.NewTicker(traeauth.RefreshInterval)
	defer ticker.Stop()
	previous := make(map[string]string)
	for {
		if ctx.Err() != nil {
			return
		}
		refreshAgentAuth(ctx, st, log, previous, traeauth.Ensure)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func refreshAgentAuth(ctx context.Context, st authAgentStore, log *slog.Logger, previous map[string]string, ensure func(context.Context, map[string]string) error) {
	agents, err := st.ListAgentInstances(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Warn("could not list agents for login renewal")
		}
		return
	}
	active := make(map[string]bool)
	for _, agent := range agents {
		if ctx.Err() != nil {
			return
		}
		if !agent.Enabled || agent.RuntimeID != "traecli" || agent.ProviderID != "" {
			continue
		}
		active[agent.ID] = true
		err := ensure(ctx, agent.Env)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			if previous[agent.ID] != "" {
				log.Info("TRAE login renewal recovered", "agent_id", agent.ID)
			}
			delete(previous, agent.ID)
			continue
		}
		// Report transitions only, not an identical warning every five minutes.
		if previous[agent.ID] != err.Error() {
			log.Warn("TRAE login renewal requires attention", "agent_id", agent.ID, "detail", err.Error())
			previous[agent.ID] = err.Error()
		}
	}
	for id := range previous {
		if !active[id] {
			delete(previous, id)
		}
	}
}
