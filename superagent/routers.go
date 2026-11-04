package superagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wangning19940904/AgentMux/core"
)

// Router agents are Agent instances made only to route. The router reads
// untrusted events, so the host keeps their configuration narrower than an
// ordinary Agent's: they always run in the framework's read-only mode, get only
// the MCP servers listed on them, and never have skills, CLIs, channels or
// schedules. They are kept apart from other Agents, can be shared between Super
// Agents, and only they can be chosen as a router.
const routerAgentSource = "super_agent_router"

const routerIDPrefix = "sar_"

// IsRouterAgent reports whether an Agent instance is a router agent. Routers
// created per framework before routers had their own configuration were tagged
// like auto workers, and count too.
func IsRouterAgent(agent core.AgentInstance) bool {
	return agent.Source == routerAgentSource ||
		(agent.Source == autoWorkerAgentSource && strings.HasPrefix(agent.ID, routerIDPrefix))
}

// Router is a router agent as the Console configures it.
type Router struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	RuntimeID string `json:"runtime_id"`
	// Model is empty for the framework's default.
	Model string `json:"model,omitempty"`
	// MCPServers are the only tools the router has beyond the framework's
	// read-only ones. Empty means none.
	MCPServers []string `json:"mcp_servers"`
	// UsedBy lists the Super Agents that route with it. It is read only.
	UsedBy    []RouterUser `json:"used_by"`
	CreatedAt time.Time    `json:"created_at,omitzero"`
	UpdatedAt time.Time    `json:"updated_at,omitzero"`
}

// RouterUser is a Super Agent that routes with a router agent.
type RouterUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// RouterFramework says whether an installed framework can route, and whether
// AgentMux can give its routers MCP servers.
type RouterFramework struct {
	RuntimeID  string `json:"runtime_id"`
	CanRoute   bool   `json:"can_route"`
	MCPServers bool   `json:"mcp_servers"`
	Reason     string `json:"reason,omitempty"`
}

// RouterList is what the Console needs to choose or create a router agent.
type RouterList struct {
	Routers    []Router          `json:"routers"`
	Frameworks []RouterFramework `json:"frameworks"`
	// MCPServers are the enabled MCP servers a router may be given.
	MCPServers []string `json:"mcp_servers"`
}

// checkRouterRuntime refuses frameworks that cannot be held to read-only.
func checkRouterRuntime(runtimeID string) error {
	if modes := core.ApprovalModeValuesForRuntime(runtimeID); len(modes) > 0 && !core.ApprovalModeSupported(runtimeID, core.ApprovalModePlan) {
		return fmt.Errorf("the router reads untrusted events, so it needs a read-only mode, which %s does not have; choose another framework", runtimeID)
	}
	return nil
}

// routerMCPSupported mirrors the frameworks whose MCP configuration the
// workspace initializer can write (workspace.reconcileMCP).
func routerMCPSupported(runtimeID string) bool {
	return runtimeID == "claudecode" || runtimeID == "codex"
}

func routerFramework(runtimeID string) RouterFramework {
	framework := RouterFramework{RuntimeID: runtimeID, CanRoute: true, MCPServers: routerMCPSupported(runtimeID)}
	if err := checkRouterRuntime(runtimeID); err != nil {
		framework.CanRoute, framework.Reason = false, err.Error()
	}
	return framework
}

func routerView(instance core.AgentInstance, users []RouterUser) Router {
	if users == nil {
		users = []RouterUser{}
	}
	return Router{
		ID: instance.ID, Name: instance.Name, RuntimeID: instance.RuntimeID, Model: instance.DefaultModel,
		MCPServers: append([]string{}, instance.MCPServers...), UsedBy: users,
		CreatedAt: instance.CreatedAt, UpdatedAt: instance.UpdatedAt,
	}
}

func (s *Service) routerUsers(ctx context.Context) (map[string][]RouterUser, error) {
	agents, err := s.store.ListSuperAgents(ctx)
	if err != nil {
		return nil, err
	}
	users := map[string][]RouterUser{}
	for _, agent := range agents {
		users[agent.RouterAgentID] = append(users[agent.RouterAgentID], RouterUser{ID: agent.ID, Name: agent.Name})
	}
	return users, nil
}

// ListRouters returns the router agents, the installed frameworks that can
// route, and the MCP servers a router may be given.
func (s *Service) ListRouters(ctx context.Context) (RouterList, error) {
	instances, err := s.store.ListAgentInstances(ctx)
	if err != nil {
		return RouterList{}, err
	}
	users, err := s.routerUsers(ctx)
	if err != nil {
		return RouterList{}, err
	}
	list := RouterList{Routers: []Router{}, Frameworks: []RouterFramework{}, MCPServers: []string{}}
	for _, instance := range instances {
		if IsRouterAgent(instance) {
			list.Routers = append(list.Routers, routerView(instance, users[instance.ID]))
		}
	}
	slices.SortFunc(list.Routers, func(a, b Router) int {
		if order := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); order != 0 {
			return order
		}
		return strings.Compare(a.ID, b.ID)
	})
	for _, runtimeID := range core.RegisteredAgents() {
		if s.runtimeAvailable == nil || s.runtimeAvailable(runtimeID) {
			list.Frameworks = append(list.Frameworks, routerFramework(runtimeID))
		}
	}
	if s.mcp != nil {
		definitions, err := s.mcp.List(ctx)
		if err != nil {
			return RouterList{}, fmt.Errorf("list mcp servers: %w", err)
		}
		for _, definition := range definitions {
			if definition.Enabled {
				list.MCPServers = append(list.MCPServers, definition.Name)
			}
		}
		slices.Sort(list.MCPServers)
	}
	return list, nil
}

// SaveRouter creates or updates a router agent. Whatever was sent, the result
// runs read-only, with no tools but the listed MCP servers.
func (s *Service) SaveRouter(ctx context.Context, router Router) (Router, error) {
	router.ID = strings.TrimSpace(router.ID)
	router.Name = strings.TrimSpace(router.Name)
	router.RuntimeID = strings.TrimSpace(router.RuntimeID)
	router.Model = strings.TrimSpace(router.Model)
	now := s.now().UTC()
	var instance core.AgentInstance
	if router.ID != "" {
		existing, err := s.store.GetAgentInstance(ctx, router.ID)
		if err != nil {
			return Router{}, err
		}
		if existing == nil {
			return Router{}, fmt.Errorf("router agent %q: %w", router.ID, ErrNotFound)
		}
		if !IsRouterAgent(*existing) {
			return Router{}, fmt.Errorf("%q is not a router agent", existing.Name)
		}
		if router.RuntimeID == "" {
			router.RuntimeID = existing.RuntimeID
		}
		if router.RuntimeID != existing.RuntimeID {
			return Router{}, errors.New("a router agent's framework cannot change; create another router agent instead")
		}
		instance = *existing
	} else {
		if router.RuntimeID == "" {
			return Router{}, errors.New("choose a framework for the router agent")
		}
		if s.runtimeAvailable != nil && !s.runtimeAvailable(router.RuntimeID) {
			return Router{}, fmt.Errorf("agent framework %q is not installed on this machine", router.RuntimeID)
		}
		instance = core.AgentInstance{
			ID: routerIDPrefix + uuid.NewString(), RuntimeID: router.RuntimeID,
			SessionBackend: "structured", CreatedAt: now,
		}
	}
	if err := checkRouterRuntime(router.RuntimeID); err != nil {
		return Router{}, err
	}
	servers, err := s.validateRouterMCP(ctx, router.RuntimeID, router.MCPServers)
	if err != nil {
		return Router{}, err
	}
	if router.Name == "" {
		router.Name = fmt.Sprintf("Router (%s)", router.RuntimeID)
	}
	// Each router has its own directory, since that is where the workspace
	// writes the MCP configuration it is given.
	workDir, err := s.routerWorkDir(instance.ID)
	if err != nil {
		return Router{}, err
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return Router{}, fmt.Errorf("create router directory: %w", err)
	}
	instance.Name = router.Name
	instance.WorkDir, instance.WorkspaceMode = workDir, "shared"
	instance.DefaultModel = router.Model
	instance.DefaultApprovalMode = core.ApprovalModePlan
	instance.MCPServers = servers
	instance.Skills, instance.CLIs, instance.ChannelBindings, instance.Schedules = nil, nil, nil, nil
	instance.SystemPrompt = ""
	instance.Enabled, instance.Source, instance.UpdatedAt = true, routerAgentSource, now
	if err := s.store.UpsertAgentInstance(ctx, &instance); err != nil {
		return Router{}, err
	}
	users, err := s.routerUsers(ctx)
	if err != nil {
		return Router{}, err
	}
	return routerView(instance, users[instance.ID]), nil
}

// DeleteRouter removes a router agent that no Super Agent routes with.
func (s *Service) DeleteRouter(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	instance, err := s.store.GetAgentInstance(ctx, id)
	if err != nil {
		return err
	}
	if instance == nil {
		return fmt.Errorf("router agent %q: %w", id, ErrNotFound)
	}
	if !IsRouterAgent(*instance) {
		return fmt.Errorf("%q is not a router agent", instance.Name)
	}
	users, err := s.routerUsers(ctx)
	if err != nil {
		return err
	}
	if len(users[id]) > 0 {
		names := make([]string, 0, len(users[id]))
		for _, user := range users[id] {
			names = append(names, user.Name)
		}
		return fmt.Errorf("%s routes with %s; choose another router for it first: %w", strings.Join(names, ", "), instance.Name, ErrConflict)
	}
	if err := s.store.DeleteAgentInstance(ctx, id); err != nil {
		return err
	}
	if dir, err := s.routerWorkDir(id); err == nil && filepath.Base(dir) == id {
		_ = os.RemoveAll(dir)
	}
	return nil
}

func (s *Service) routerWorkDir(id string) (string, error) {
	if id == "" || id != filepath.Base(id) {
		return "", fmt.Errorf("invalid router agent id %q", id)
	}
	state, err := s.stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "super-agents", "routers", id), nil
}

func (s *Service) validateRouterMCP(ctx context.Context, runtimeID string, names []string) ([]string, error) {
	names = normalizeActors(names)
	if len(names) == 0 {
		return nil, nil
	}
	if !routerMCPSupported(runtimeID) {
		return nil, fmt.Errorf("AgentMux cannot give a %s router MCP servers; remove them, or use Claude Code or Codex", runtimeID)
	}
	if s.mcp == nil {
		return nil, errors.New("the MCP registry is unavailable")
	}
	definitions, err := s.mcp.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list mcp servers: %w", err)
	}
	enabled := map[string]bool{}
	known := map[string]bool{}
	for _, definition := range definitions {
		known[definition.Name], enabled[definition.Name] = true, definition.Enabled
	}
	for _, name := range names {
		if !known[name] {
			return nil, fmt.Errorf("mcp server %q is not registered", name)
		}
		if !enabled[name] {
			return nil, fmt.Errorf("mcp server %q is disabled", name)
		}
	}
	return names, nil
}

// validateRouter keeps the blast radius of a prompt injection small. Only a
// router agent may route, and only in read-only mode, which it is saved with
// but which the Agents API could still change. A Super Agent saved before
// routers had their own configuration may keep the ordinary Agent it routes
// with, as current; that one only has to stay out of yolo.
func (s *Service) validateRouter(ctx context.Context, id, current string) (*core.AgentInstance, error) {
	if id == "" {
		return nil, errors.New("choose a router agent, or create one")
	}
	router, err := s.store.GetAgentInstance(ctx, id)
	if err != nil {
		return nil, err
	}
	if router == nil {
		return nil, fmt.Errorf("router agent %q not found on this machine", id)
	}
	if IsRouterAgent(*router) {
		if router.DefaultApprovalMode != core.ApprovalModePlan {
			return nil, fmt.Errorf("router agent %q must run in plan mode, and runs in %q; save it again under Super Agents", router.Name, router.DefaultApprovalMode)
		}
		return router, nil
	}
	if id != current {
		return nil, fmt.Errorf("%q is not a router agent; choose a router agent, or create one", router.Name)
	}
	if router.DefaultApprovalMode == core.ApprovalModeYolo {
		return nil, errors.New("the router reads untrusted events, so its Agent cannot use the yolo approval mode; choose plan mode")
	}
	return router, nil
}
