package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
)

// Ownership enforcement for the resource types tenants can reach.
//
// Reads are narrowed by the store's scoped list queries; writes and executions
// go through accessLevel below. Ownership is stamped on create, and a tenant
// can never move a resource to another owner: the owner fields on an incoming
// payload are ignored and replaced with the caller's own identity.

// accessLevel reports the strongest level a principal holds on one resource.
// An empty result means no access at all.
func (s *Server) accessLevel(ctx context.Context, principal *core.Principal, resourceType, resourceID, ownerTenantID, visibility string) string {
	if !principal.IsTenant() {
		return core.GrantLevelManage
	}
	if ownerTenantID != "" && ownerTenantID == principal.TenantID {
		return core.GrantLevelManage
	}
	level := ""
	// Public resources are readable and runnable by every tenant, but only
	// their owner (or the admin) may change them.
	if visibility == core.VisibilityPublic {
		level = core.GrantLevelUse
	}
	// An explicit grant can only widen access, never narrow what public
	// visibility already allows.
	if s.st != nil {
		granted, err := s.st.GrantLevelFor(ctx, principal.TenantID, resourceType, resourceID)
		if err == nil {
			level = core.StrongerGrant(level, granted)
		}
	}
	return level
}

// authorizeAgent resolves an agent and checks the principal's access. It
// returns false after writing the response when access is denied.
func (s *Server) authorizeAgent(w http.ResponseWriter, r *http.Request, agentID, required string) (*core.AgentInstance, bool) {
	principal := requestPrincipal(r)
	if s.st == nil {
		writeErr(w, http.StatusServiceUnavailable, "store unavailable")
		return nil, false
	}
	// config.toml agents are host infrastructure with no owner. They stay
	// admin-only rather than becoming implicitly shared.
	if strings.HasPrefix(agentID, "config:") {
		if principal.IsTenant() {
			writeNotVisible(w, "agent")
			return nil, false
		}
		agent, found, err := s.findAgentInstance(r.Context(), agentID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return nil, false
		}
		if !found {
			writeErr(w, http.StatusNotFound, "agent not found")
			return nil, false
		}
		return &agent, true
	}
	agent, err := s.st.GetAgentInstance(r.Context(), agentID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	if agent == nil {
		writeErr(w, http.StatusNotFound, "agent not found")
		return nil, false
	}
	level := s.accessLevel(r.Context(), principal, core.ResourceTypeAgent, agent.ID, agent.OwnerTenantID, agent.Visibility)
	if !core.GrantSatisfies(level, required) {
		s.denyResource(w, principal, "agent", level)
		return nil, false
	}
	return agent, true
}

// authorizeChannel mirrors authorizeAgent for channels.
func (s *Server) authorizeChannel(w http.ResponseWriter, r *http.Request, channelID, required string) (*core.Channel, bool) {
	principal := requestPrincipal(r)
	if s.st == nil {
		writeErr(w, http.StatusServiceUnavailable, "store unavailable")
		return nil, false
	}
	channel, err := s.st.GetChannel(r.Context(), channelID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	if channel == nil {
		writeErr(w, http.StatusNotFound, "channel not found")
		return nil, false
	}
	level := s.accessLevel(r.Context(), principal, core.ResourceTypeChannel, channel.ID, channel.OwnerTenantID, channel.Visibility)
	if !core.GrantSatisfies(level, required) {
		s.denyResource(w, principal, "channel", level)
		return nil, false
	}
	return channel, true
}

// authorizeProvider checks an instance-owned Provider against explicit tenant
// grants. Providers contain shared routing credentials and therefore have no
// tenant ownership or public visibility shortcut.
func (s *Server) authorizeProvider(w http.ResponseWriter, r *http.Request, providerID, required string) (*core.Provider, bool) {
	principal := requestPrincipal(r)
	if s.provider == nil || s.st == nil {
		writeErr(w, http.StatusServiceUnavailable, "provider service unavailable")
		return nil, false
	}
	provider, err := s.provider.Get(r.Context(), providerID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	if provider == nil {
		writeErr(w, http.StatusNotFound, "provider not found")
		return nil, false
	}
	if !principal.IsTenant() {
		return provider, true
	}
	level, err := s.st.GrantLevelFor(r.Context(), principal.TenantID, core.ResourceTypeProvider, provider.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	if !core.GrantSatisfies(level, required) {
		s.denyResource(w, principal, "provider", level)
		return nil, false
	}
	return provider, true
}

// authorizeTrigger mirrors authorizeAgent for triggers. Triggers have no
// public visibility of their own.
func (s *Server) authorizeTrigger(w http.ResponseWriter, r *http.Request, triggerID, required string) (*core.Trigger, bool) {
	principal := requestPrincipal(r)
	if s.st == nil {
		writeErr(w, http.StatusServiceUnavailable, "store unavailable")
		return nil, false
	}
	trigger, err := s.st.GetTrigger(r.Context(), triggerID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	if trigger == nil {
		writeErr(w, http.StatusNotFound, "trigger not found")
		return nil, false
	}
	level := s.accessLevel(r.Context(), principal, core.ResourceTypeTrigger, trigger.ID, trigger.OwnerTenantID, "")
	if !core.GrantSatisfies(level, required) {
		s.denyResource(w, principal, "trigger", level)
		return nil, false
	}
	return trigger, true
}

// denyResource distinguishes "you cannot see this" from "you can see it but
// may not change it", which is the difference an integrator needs to debug a
// permission problem.
func (s *Server) denyResource(w http.ResponseWriter, principal *core.Principal, kind, held string) {
	if held == "" {
		writeNotVisible(w, kind)
		return
	}
	writeErr(w, http.StatusForbidden,
		"tenant \""+principal.TenantName+"\" holds only "+held+" access on this "+kind)
}

// writeNotVisible hides existence rather than confirming it to a tenant that
// has no access, so resource IDs cannot be probed across tenants.
func writeNotVisible(w http.ResponseWriter, kind string) {
	writeErr(w, http.StatusNotFound, kind+" not found")
}

// stampAgentOwnership fixes the ownership fields of an incoming agent payload.
// A tenant always writes its own name into the owner and may not publish a
// resource; the admin keeps full control of both fields.
func (s *Server) stampAgentOwnership(ctx context.Context, principal *core.Principal, agent *core.AgentInstance, existing *core.AgentInstance) {
	if principal.IsTenant() {
		agent.OwnerTenantID = principal.TenantID
		if existing != nil {
			agent.Visibility = existing.Visibility
		} else {
			agent.Visibility = core.VisibilityPrivate
		}
		return
	}
	if existing != nil && strings.TrimSpace(agent.OwnerTenantID) == "" {
		agent.OwnerTenantID = existing.OwnerTenantID
	}
	agent.Visibility = core.NormalizeVisibility(agent.Visibility)
}

// stampChannelOwnership mirrors stampAgentOwnership for channels.
func (s *Server) stampChannelOwnership(principal *core.Principal, channel *core.Channel, existing *core.Channel) {
	if principal.IsTenant() {
		channel.OwnerTenantID = principal.TenantID
		if existing != nil {
			channel.Visibility = existing.Visibility
		} else {
			channel.Visibility = core.VisibilityPrivate
		}
		return
	}
	if existing != nil && strings.TrimSpace(channel.OwnerTenantID) == "" {
		channel.OwnerTenantID = existing.OwnerTenantID
	}
	channel.Visibility = core.NormalizeVisibility(channel.Visibility)
}

// labelAgentOwners fills OwnerTenantName so the admin Console can show which
// application owns each row without a lookup per row.
func (s *Server) labelAgentOwners(ctx context.Context, items []core.AgentInstance) {
	if s.st == nil || len(items) == 0 {
		return
	}
	names, err := s.st.TenantNames(ctx)
	if err != nil || len(names) == 0 {
		return
	}
	for i := range items {
		if items[i].OwnerTenantID != "" {
			items[i].OwnerTenantName = names[items[i].OwnerTenantID]
		}
	}
}

// labelChannelOwners mirrors labelAgentOwners for channels.
func (s *Server) labelChannelOwners(ctx context.Context, items []core.Channel) {
	if s.st == nil || len(items) == 0 {
		return
	}
	names, err := s.st.TenantNames(ctx)
	if err != nil || len(names) == 0 {
		return
	}
	for i := range items {
		if items[i].OwnerTenantID != "" {
			items[i].OwnerTenantName = names[items[i].OwnerTenantID]
		}
	}
}

// authorizeInvocationTarget checks that the caller may run the requested
// PostgreSQL-backed Agent instance.
func (s *Server) authorizeInvocationTarget(w http.ResponseWriter, r *http.Request, agentID string) bool {
	principal := requestPrincipal(r)
	if !principal.IsTenant() {
		return true
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		writeErr(w, http.StatusBadRequest, "agent_id is required")
		return false
	}
	agent, authorized := s.authorizeAgent(w, r, agentID, core.GrantLevelUse)
	if !authorized {
		return false
	}
	return s.authorizeInvocationProvider(w, r, agent)
}

// authorizeInvocationProvider makes Provider grants effective at runtime, not
// merely in the Console picker. Agents using local login have no Provider and
// remain runnable; explicit and active-route Providers require use access.
func (s *Server) authorizeInvocationProvider(w http.ResponseWriter, r *http.Request, agent *core.AgentInstance) bool {
	principal := requestPrincipal(r)
	if !principal.IsTenant() || agent == nil || s.provider == nil {
		return true
	}
	provider, err := s.agentProvider(r.Context(), agent)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if provider == nil {
		return true
	}
	_, authorized := s.authorizeProvider(w, r, provider.ID, core.GrantLevelUse)
	return authorized
}

// authorizeOpenAIInvocationTarget is authorizeInvocationTarget for the
// OpenAI-compatible layer, which reports failures in OpenAI's error shape.
func (s *Server) authorizeOpenAIInvocationTarget(w http.ResponseWriter, r *http.Request, invocation core.InvocationRequest) bool {
	principal := requestPrincipal(r)
	if !principal.IsTenant() {
		return true
	}
	agentID := strings.TrimSpace(invocation.AgentID)
	if agentID == "" {
		writeOpenAIError(w, http.StatusBadRequest,
			"an agent target is required", "invalid_request_error", "model", "invalid_target")
		return false
	}
	agent, err := s.st.GetAgentInstance(r.Context(), agentID)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error(), "server_error", nil, "store_error")
		return false
	}
	if agent == nil {
		writeOpenAIError(w, http.StatusNotFound, "the requested model was not found", "invalid_request_error", "model", "model_not_found")
		return false
	}
	level := s.accessLevel(r.Context(), principal, core.ResourceTypeAgent, agent.ID, agent.OwnerTenantID, agent.Visibility)
	if !core.GrantSatisfies(level, core.GrantLevelUse) {
		writeOpenAIError(w, http.StatusNotFound, "the requested model was not found", "invalid_request_error", "model", "model_not_found")
		return false
	}
	if s.provider != nil {
		provider, providerErr := s.agentProvider(r.Context(), agent)
		if providerErr != nil {
			writeOpenAIError(w, http.StatusInternalServerError, providerErr.Error(), "server_error", nil, "store_error")
			return false
		}
		if provider != nil {
			providerLevel, grantErr := s.st.GrantLevelFor(r.Context(), principal.TenantID, core.ResourceTypeProvider, provider.ID)
			if grantErr != nil {
				writeOpenAIError(w, http.StatusInternalServerError, grantErr.Error(), "server_error", nil, "store_error")
				return false
			}
			if !core.GrantSatisfies(providerLevel, core.GrantLevelUse) {
				writeOpenAIError(w, http.StatusNotFound, "the requested model was not found", "invalid_request_error", "model", "model_not_found")
				return false
			}
		}
	}
	return true
}

// visibleAgentIDs is the set of agent IDs a principal may reference. It backs
// the checks on endpoints that take an agent ID indirectly, such as usage
// reporting and orchestration task targets.
func (s *Server) visibleAgentIDs(ctx context.Context, principal *core.Principal) (map[string]bool, error) {
	if !principal.IsTenant() || s.st == nil {
		return nil, nil
	}
	agents, err := s.st.ListAgentInstancesForTenant(ctx, principal.TenantID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(agents))
	for _, agent := range agents {
		out[agent.ID] = true
	}
	return out, nil
}
