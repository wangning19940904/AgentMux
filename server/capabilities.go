package server

import (
	"net/http"

	"github.com/wangning19940904/AgentMux/contract"
	"github.com/wangning19940904/AgentMux/core"
)

// handleCapabilities is the single recommended handshake endpoint for
// external integrations. It replaces the legacy pattern of probing
// /api/v1/status + /api/v1/agent-instances + /api/v1/channels and lets SDKs
// feature-detect instead of guessing from version numbers.
func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	features := []string{"send"}
	if s.invoker != nil {
		features = append(features, "invocations")
		if _, ok := s.invoker.(core.StreamingInvoker); ok {
			features = append(features, "invocations.stream")
		}
		features = append(features, "openai.responses")
		if s.st != nil {
			features = append(features, "orchestrations")
		}
	}
	if s.st != nil {
		features = append(features, "triggers")
	}
	if s.usageFn != nil {
		features = append(features, "usage")
	}
	if s.cfg.Bridge.Enabled || s.cfg.Bridge.Token != "" || s.st != nil {
		features = append(features, "console.session")
	}
	if s.st != nil {
		features = append(features, "tenancy")
	}

	// The counts describe what the caller can see, so a tenant is told the
	// size of its own scope rather than the whole instance.
	principal := requestPrincipal(r)
	agents := map[string]any{"count": 0, "runtimes": availableAgentRuntimes()}
	channels := map[string]any{"count": 0}
	if s.st != nil {
		if items, err := s.scopedAgentInstances(r.Context(), principal); err == nil {
			agents["count"] = len(items)
		}
		if principal.IsTenant() {
			if items, err := s.st.ListChannelsForTenant(r.Context(), principal.TenantID); err == nil {
				channels["count"] = len(items)
			}
		} else if items, err := s.st.ListChannels(r.Context()); err == nil {
			channels["count"] = len(items)
		}
	}

	auth := map[string]any{"bridge_enabled": s.cfg.Bridge.Enabled}
	if principal.IsTenant() {
		auth["tenant"] = principal.TenantName
		auth["tenant_id"] = principal.TenantID
		auth["scope"] = "tenant"
	} else {
		auth["scope"] = "admin"
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"product":          "agentmux",
		"version":          s.version,
		"contract_version": contract.Version,
		"features":         features,
		"modules": map[string]any{
			"connect": moduleState(len(core.RegisteredPlatforms()) > 0, s.connect != nil),
			"router":  moduleState(len(core.RegisteredAgents()) > 0, s.invoker != nil),
			"ledger":  moduleState(true, s.usageFn != nil),
			"memory":  moduleState(len(core.RegisteredMemories()) > 0, s.memory != nil),
			"skills":  moduleState(len(core.RegisteredSkillManagers()) > 0, s.skills != nil),
			"mcp":     moduleState(len(core.RegisteredMCPRegistries()) > 0, s.mcp != nil),
			"guard":   moduleState(len(core.RegisteredGuards()) > 0, s.guard != nil),
		},
		"agents":   agents,
		"channels": channels,
		"projects": len(s.cfg.Projects),
		"auth":     auth,
	})
}

func moduleState(registered, active bool) map[string]bool {
	return map[string]bool{"registered": registered, "active": active}
}
