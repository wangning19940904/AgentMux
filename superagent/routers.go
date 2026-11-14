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

// Router agents are Agent instances made only to route. They run unattended in
// their framework's default approval mode, Auto where it has one, get only
// the MCP servers listed on them, and never have skills, CLIs, channels or
// schedules. Their charter, sent with every event, is what tells them how to
// treat untrusted text. They are kept apart from other Agents, and only they
// can be chosen as a router. Each belongs to one Super Agent and works in its
// home; until a Super Agent routes with it, it has a scratch directory.
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
	// Name follows the Super Agent that routes with it, as "<name> router",
	// unless several Super Agents share it.
	// Model, ReasoningEffort and ServiceTier are empty for the framework's
	// defaults. A ServiceTier such as "priority" is the framework's fast mode.
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	ServiceTier     string `json:"service_tier,omitempty"`
	// ApprovalMode is what the router may do without asking. Empty keeps a
	// saved router's mode, and gives a new one its framework's default.
	ApprovalMode string `json:"approval_mode,omitempty"`
	// MCPServers are the MCP servers the router may use. Empty means none.
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

// RouterFramework says whether an installed framework can route, which
// approval modes its routers may run in, and whether AgentMux can give its
// routers MCP servers.
type RouterFramework struct {
	RuntimeID           string   `json:"runtime_id"`
	CanRoute            bool     `json:"can_route"`
	MCPServers          bool     `json:"mcp_servers"`
	ApprovalModes       []string `json:"approval_modes"`
	DefaultApprovalMode string   `json:"default_approval_mode,omitempty"`
	Reason              string   `json:"reason,omitempty"`
}

// RouterList is what the Console needs to choose or create a router agent.
type RouterList struct {
	Routers    []Router          `json:"routers"`
	Frameworks []RouterFramework `json:"frameworks"`
	// MCPServers are the enabled MCP servers a router may be given.
	MCPServers []string `json:"mcp_servers"`
}

// routerApprovalModes are the approval modes a router may run in: any its
// framework offers but manual, since nobody is there to approve. It is nil for
// a framework whose modes AgentMux does not know.
func routerApprovalModes(runtimeID string) []string {
	var modes []string
	for _, mode := range core.ApprovalModeValuesForRuntime(runtimeID) {
		if mode != core.ApprovalModeManual {
			modes = append(modes, mode)
		}
	}
	return modes
}

// defaultRouterApproval is the mode a new router runs in: Auto, where the
// framework approves what looks safe and declines the rest, so the router can
// keep its memory and look things up without a blanket licence to run any
// command. A framework without Auto gets Auto edit, then YOLO, then plan.
func defaultRouterApproval(runtimeID string) string {
	modes := routerApprovalModes(runtimeID)
	for _, mode := range []string{core.ApprovalModeAuto, core.ApprovalModeAutoEdit, core.ApprovalModeYolo, core.ApprovalModePlan} {
		if slices.Contains(modes, mode) {
			return mode
		}
	}
	return ""
}

// checkRouterRuntime refuses frameworks that cannot run unattended.
func checkRouterRuntime(runtimeID string) error {
	if modes := core.ApprovalModeValuesForRuntime(runtimeID); len(modes) > 0 && len(routerApprovalModes(runtimeID)) == 0 {
		return fmt.Errorf("the router runs unattended, so it needs an approval mode other than manual, which %s does not have; choose another framework", runtimeID)
	}
	return nil
}

// routerApproval resolves the approval mode a router is saved with: the one
// asked for, else the one it has, else its framework's default.
func routerApproval(runtimeID, requested, current string) (string, error) {
	modes := routerApprovalModes(runtimeID)
	mode := strings.TrimSpace(requested)
	if mode == "" && slices.Contains(modes, current) {
		mode = current
	}
	if mode == "" {
		return defaultRouterApproval(runtimeID), nil
	}
	if len(modes) == 0 {
		return "", fmt.Errorf("AgentMux does not know %s's approval modes; leave the router's approval mode empty", runtimeID)
	}
	if !slices.Contains(modes, mode) {
		return "", fmt.Errorf("a %s router can run in %s, not %q", runtimeID, strings.Join(modes, ", "), mode)
	}
	return mode, nil
}

// routerMCPSupported mirrors the frameworks whose MCP configuration the
// workspace initializer can write (workspace.reconcileMCP).
func routerMCPSupported(runtimeID string) bool {
	return runtimeID == "claudecode" || runtimeID == "codex"
}

func routerFramework(runtimeID string) RouterFramework {
	framework := RouterFramework{
		RuntimeID: runtimeID, CanRoute: true, MCPServers: routerMCPSupported(runtimeID),
		ApprovalModes: routerApprovalModes(runtimeID), DefaultApprovalMode: defaultRouterApproval(runtimeID),
	}
	if framework.ApprovalModes == nil {
		framework.ApprovalModes = []string{}
	}
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
		ReasoningEffort: instance.DefaultReasoningEffort, ServiceTier: instance.DefaultServiceTier,
		ApprovalMode: instance.DefaultApprovalMode,
		MCPServers:   append([]string{}, instance.MCPServers...), UsedBy: users,
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
// runs unattended in a mode its framework offers, with no skills, CLIs,
// channels or schedules, and only the listed MCP servers.
func (s *Service) SaveRouter(ctx context.Context, router Router) (Router, error) {
	router.ID = strings.TrimSpace(router.ID)
	router.Name = strings.TrimSpace(router.Name)
	router.RuntimeID = strings.TrimSpace(router.RuntimeID)
	router.Model = strings.TrimSpace(router.Model)
	router.ReasoningEffort = strings.TrimSpace(router.ReasoningEffort)
	router.ServiceTier = strings.TrimSpace(router.ServiceTier)
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
		if router.Name == "" {
			router.Name = existing.Name
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
	approval, err := routerApproval(router.RuntimeID, router.ApprovalMode, instance.DefaultApprovalMode)
	if err != nil {
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
	// writes the MCP configuration it is given: its Super Agent's home, or a
	// scratch directory until it has one.
	workDir := instance.WorkDir
	if workDir == "" {
		if workDir, err = s.routerWorkDir(instance.ID); err != nil {
			return Router{}, err
		}
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return Router{}, fmt.Errorf("create router directory: %w", err)
	}
	instance.Name = router.Name
	instance.WorkDir, instance.WorkspaceMode = workDir, "shared"
	instance.DefaultModel = router.Model
	instance.DefaultReasoningEffort, instance.DefaultServiceTier = router.ReasoningEffort, router.ServiceTier
	instance.DefaultApprovalMode = approval
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

// routerName is the name of a router agent that one Super Agent routes with.
func routerName(superAgentName string) string { return superAgentName + " router" }

// checkRouterOwner refuses a router agent that another Super Agent routes
// with, since a router works in its Super Agent's home. router is the
// resolved router; an ordinary Agent kept as the router from before router
// agents existed is not checked.
func (s *Service) checkRouterOwner(ctx context.Context, superAgentID string, router *core.AgentInstance) error {
	if router == nil || router.ID == "" || !IsRouterAgent(*router) {
		return nil
	}
	users, err := s.routerUsers(ctx)
	if err != nil {
		return err
	}
	for _, user := range users[router.ID] {
		if user.ID != superAgentID {
			return conflictError(fmt.Sprintf("router agent %q belongs to %s; create a router for this Super Agent", router.Name, user.Name))
		}
	}
	return nil
}

// bindRouter creates the Super Agent's home and makes its router agent work
// there, so the router can read every Ticket's folder. An ordinary Agent kept
// as the router keeps its own directory.
func (s *Service) bindRouter(ctx context.Context, agent core.SuperAgent) error {
	home, err := s.ensureHome(agent.ID)
	if err != nil {
		return err
	}
	router, err := s.store.GetAgentInstance(ctx, agent.RouterAgentID)
	if err != nil || router == nil || !IsRouterAgent(*router) {
		return err
	}
	if router.WorkDir == home && router.WorkspaceMode == "shared" {
		return nil
	}
	previous := router.WorkDir
	router.WorkDir, router.WorkspaceMode, router.UpdatedAt = home, "shared", s.now().UTC()
	if err := s.store.UpsertAgentInstance(ctx, router); err != nil {
		return err
	}
	if scratch, err := s.routerWorkDir(router.ID); err == nil && previous == scratch {
		_ = os.RemoveAll(scratch)
	}
	return nil
}

// deleteOwnedRouter removes a deleted Super Agent's router agent, unless
// another Super Agent still routes with it.
func (s *Service) deleteOwnedRouter(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	router, err := s.store.GetAgentInstance(ctx, id)
	if err != nil || router == nil || !IsRouterAgent(*router) {
		return err
	}
	if err := s.DeleteRouter(ctx, id); err != nil && !errors.Is(err, ErrConflict) && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

// adoptRouters gives every Super Agent a router agent of its own that works
// in its home. Super Agents that shared a router before each router belonged
// to one keep it in the order they were created; the others get a copy.
func (s *Service) adoptRouters(ctx context.Context) {
	agents, err := s.store.ListSuperAgents(ctx)
	if err != nil {
		s.log.Error("list super agents to adopt routers", "err", err)
		return
	}
	slices.SortFunc(agents, func(a, b core.SuperAgent) int {
		if order := a.CreatedAt.Compare(b.CreatedAt); order != 0 {
			return order
		}
		return strings.Compare(a.ID, b.ID)
	})
	owners := map[string]bool{}
	for _, agent := range agents {
		router, err := s.store.GetAgentInstance(ctx, agent.RouterAgentID)
		if err != nil {
			s.log.Error("load router to adopt", "super_agent", agent.ID, "err", err)
			continue
		}
		if router == nil || !IsRouterAgent(*router) {
			continue
		}
		if owners[router.ID] {
			now := s.now().UTC()
			copied := *router
			copied.ID, copied.Name, copied.WorkDir = routerIDPrefix+uuid.NewString(), routerName(agent.Name), ""
			copied.Source = routerAgentSource
			copied.CreatedAt, copied.UpdatedAt = now, now
			if err := s.store.UpsertAgentInstance(ctx, &copied); err != nil {
				s.log.Error("copy shared router", "super_agent", agent.ID, "router", router.ID, "err", err)
				continue
			}
			agent.RouterAgentID = copied.ID
			if err := s.store.UpsertSuperAgent(ctx, agent); err != nil {
				s.log.Error("give super agent its own router", "super_agent", agent.ID, "err", err)
				continue
			}
			router = &copied
		}
		owners[router.ID] = true
		if err := s.bindRouter(ctx, agent); err != nil {
			s.log.Error("move router to super agent home", "super_agent", agent.ID, "err", err)
		}
	}
}

// defaultSuperAgentName names a Super Agent left unnamed after its router's
// framework. A router agent is named after its Super Agent, so its own name
// would only come back with a second suffix.
func defaultSuperAgentName(router core.AgentInstance) string {
	if !IsRouterAgent(router) && strings.TrimSpace(router.Name) != "" {
		return router.Name
	}
	if router.RuntimeID == "" {
		return "Super Agent"
	}
	return fmt.Sprintf("Super Agent (%s)", router.RuntimeID)
}

// nameRouter names a router agent after the only Super Agent that routes
// with it. A router several Super Agents share keeps its name.
func (s *Service) nameRouter(ctx context.Context, agent core.SuperAgent) error {
	router, err := s.store.GetAgentInstance(ctx, agent.RouterAgentID)
	if err != nil || router == nil || !IsRouterAgent(*router) {
		return err
	}
	users, err := s.routerUsers(ctx)
	if err != nil {
		return err
	}
	for _, user := range users[router.ID] {
		if user.ID != agent.ID {
			return nil
		}
	}
	if name := routerName(agent.Name); router.Name != name {
		router.Name, router.UpdatedAt = name, s.now().UTC()
		return s.store.UpsertAgentInstance(ctx, router)
	}
	return nil
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
		return conflictError(fmt.Sprintf("%s routes with %s; choose another router for it first", strings.Join(names, ", "), instance.Name))
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

// validateRouter checks that a Super Agent may route with an Agent: only a
// router agent may route. A Super Agent saved before routers had their own
// configuration may keep the ordinary Agent it routes with, as current.
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
	if !IsRouterAgent(*router) && id != current {
		return nil, fmt.Errorf("%q is not a router agent; choose a router agent, or create one", router.Name)
	}
	return router, nil
}
