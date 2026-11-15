package superagent

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// Workspace claims: a running Ticket holds the directory its worker writes
// in, so two Tickets never edit the same files at once. (Not to be confused
// with claims.go, which is about which Sources claim which chats.)
//
// A claim is keyed by machine and directory. The machine is its machine UID
// when known, so one computer reached several ways counts once. A Ticket
// records its workspace when it is accepted, holds it from the start of its
// first turn (HeldSince) until it is ready, failed or stopped, and keeps
// holding it across retries and a daemon restart: claims are read from the
// stored Tickets, never kept anywhere else. A worker that gets its own copy
// of the directory (a per-conversation git worktree, or a Ticket's own work
// folder) holds nothing.
//
// An accept for a directory another Ticket holds is refused, naming that
// Ticket, unless the new Ticket depends on it. A queued Ticket whose
// directory is held waits for it.

const (
	// hostedDefaultDir stands for the directory another machine gives an
	// agent's workers when the operator listed none (hosted-workers/<owner>).
	hostedDefaultDir = "(default worker directory)"
	machineUIDTTL    = time.Minute
	// maxDependencyDepth bounds how far an accept's dependencies are followed
	// to find whether it waits for a Ticket holding its directory.
	maxDependencyDepth = 16
)

// workspaceOf is the directory a roster entry's worker would write in, and
// whether it gets its own copy. ok is false when that cannot be told before
// the worker starts, which claims nothing.
func (s *Service) workspaceOf(ctx context.Context, agent core.SuperAgent, place workerPlace) (core.SuperAgentTicketWorkspace, bool) {
	workspace := core.SuperAgentTicketWorkspace{HostID: place.HostID}
	switch {
	case place.Roster:
		worker := agent.Workers[place.index]
		var instance *core.AgentInstance
		if worker.HostID == "" {
			instance, _ = s.store.GetAgentInstance(ctx, worker.AgentID)
		} else if describer, ok := s.workers.(workerDescriber); ok {
			describeCtx, cancel := context.WithTimeout(ctx, machineCallWait)
			instance, _ = describer.Describe(describeCtx, worker)
			cancel()
		}
		if instance == nil || strings.TrimSpace(instance.WorkDir) == "" {
			return workspace, false
		}
		workspace.Dir = cleanDir(instance.WorkDir, worker.HostID == "")
		workspace.Isolated = strings.EqualFold(strings.TrimSpace(instance.WorkspaceMode), "worktree")
		return workspace, true
	case place.Place == "":
		// An auto worker in the agent's default directory.
		router, err := s.store.GetAgentInstance(ctx, agent.RouterAgentID)
		if err != nil || router == nil {
			return workspace, false
		}
		dir := strings.TrimSpace(agent.AutoWorkerDir)
		if dir == "" && !IsManagedAgent(*router) && !IsRouterAgent(*router) {
			dir = strings.TrimSpace(router.WorkDir)
		}
		if dir == "" {
			// Each Ticket works in its own folder.
			return core.SuperAgentTicketWorkspace{Isolated: true}, true
		}
		workspace.Dir = cleanDir(dir, true)
		workspace.Isolated = autoWorkerWorkspaceMode(ctx, workspace.Dir) == "worktree"
		return workspace, true
	default:
		machine, found := agentMachine(agent, place.Place)
		if !found {
			return workspace, false
		}
		if machine.HostID == "" {
			workspace.Dir = cleanDir(machine.Dir, true)
			workspace.Isolated = autoWorkerWorkspaceMode(ctx, workspace.Dir) == "worktree"
			return workspace, true
		}
		// On another machine, whether it is a git repository is learned
		// when the worker is launched.
		workspace.Dir = strings.TrimSpace(machine.Dir)
		if workspace.Dir == "" {
			workspace.Dir = hostedDefaultDir
		} else {
			workspace.Dir = cleanDir(workspace.Dir, false)
		}
		return workspace, true
	}
}

// workerDescriber is a WorkerRunner that can read a worker Agent's settings
// on its own machine; *Fleet implements it.
type workerDescriber interface {
	Describe(ctx context.Context, worker core.SuperAgentWorker) (*core.AgentInstance, error)
}

// Describe reads a worker Agent's settings on its own machine.
func (f *Fleet) Describe(ctx context.Context, worker core.SuperAgentWorker) (*core.AgentInstance, error) {
	if worker.HostID == "" {
		return f.Agents.GetAgentInstance(ctx, worker.AgentID)
	}
	return f.remoteAgent(ctx, worker)
}

// cleanDir normalizes a directory for comparison. A path on this machine is
// expanded; one on another machine is kept as written.
func cleanDir(dir string, local bool) string {
	dir = strings.TrimSpace(dir)
	if local {
		if expanded, err := expandHome(dir); err == nil {
			dir = expanded
		}
	}
	if dir == "" || dir == "~" {
		return dir
	}
	return filepath.Clean(dir)
}

// machineKey names a machine for claims: its UID when known, otherwise the
// way this machine reaches it.
func (s *Service) machineKey(hostID string) string {
	if uid := s.machineUID(hostID); uid != "" {
		return "uid:" + uid
	}
	if hostID == "" {
		return "local"
	}
	return "host:" + hostID
}

// machineUID is a machine's UID ("" for this machine's own), cached briefly.
func (s *Service) machineUID(hostID string) string {
	if s.machineUIDs == nil {
		return ""
	}
	s.placement.mu.Lock()
	cached, ok := s.placement.uids[hostID]
	s.placement.mu.Unlock()
	if ok && s.now().Sub(cached.at) < machineUIDTTL {
		return cached.uid
	}
	uid := s.machineUIDs(hostID)
	s.placement.mu.Lock()
	if s.placement.uids == nil {
		s.placement.uids = map[string]cachedUID{}
	}
	s.placement.uids[hostID] = cachedUID{uid: uid, at: s.now()}
	s.placement.mu.Unlock()
	return uid
}

type cachedUID struct {
	uid string
	at  time.Time
}

func (s *Service) workspaceKey(workspace core.SuperAgentTicketWorkspace) string {
	return s.machineKey(workspace.HostID) + "\x00" + workspace.Dir
}

// holdsWorkspace reports whether a Ticket holds its directory now.
func holdsWorkspace(ticket core.SuperAgentTicket) bool {
	return ticket.Workspace != nil && !ticket.Workspace.Isolated && ticket.Workspace.Dir != "" &&
		!ticket.Workspace.HeldSince.IsZero() &&
		(ticket.Status == core.SuperAgentTicketRunning || ticket.Status == core.SuperAgentTicketQueued)
}

// needsWorkspace reports whether a Ticket must hold its directory to run.
func needsWorkspace(ticket core.SuperAgentTicket) bool {
	return ticket.Workspace != nil && !ticket.Workspace.Isolated && ticket.Workspace.Dir != ""
}

// releaseWorkspace ends a Ticket's claim once it is ready, failed or stopped.
func releaseWorkspace(ticket *core.SuperAgentTicket) {
	if ticket.Workspace == nil {
		return
	}
	switch ticket.Status {
	case core.SuperAgentTicketReady, core.SuperAgentTicketFailed, core.SuperAgentTicketStopped:
		ticket.Workspace.HeldSince = time.Time{}
	}
}

// workspaceHolders are the Tickets of every agent on this machine that hold
// a directory now, by claim key.
func (s *Service) workspaceHolders(ctx context.Context) (map[string]core.SuperAgentTicket, error) {
	live, err := s.store.ListSuperAgentTicketsByStatus(ctx, core.SuperAgentTicketQueued, core.SuperAgentTicketRunning)
	if err != nil {
		return nil, err
	}
	holders := map[string]core.SuperAgentTicket{}
	for _, ticket := range live {
		if !holdsWorkspace(ticket) {
			continue
		}
		key := s.workspaceKey(*ticket.Workspace)
		// Two holders of one key can only come from before claims existed;
		// the older one is named.
		if current, ok := holders[key]; !ok || ticket.Workspace.HeldSince.Before(current.Workspace.HeldSince) {
			holders[key] = ticket
		}
	}
	return holders, nil
}

// pendingClaim is a non-isolated workspace an earlier accept of the same
// decision will hold.
type pendingClaim struct {
	ref       string
	key       string
	dependsOn []string
}

// claimCheck finds whether accepts conflict with directories held now or
// by earlier accepts of the same decision.
type claimCheck struct {
	s       *Service
	agent   core.SuperAgent
	holders map[string]core.SuperAgentTicket
	pending []pendingClaim
	loaded  bool
	err     error
}

func (c *claimCheck) load(ctx context.Context) error {
	if !c.loaded {
		c.loaded = true
		c.holders, c.err = c.s.workspaceHolders(ctx)
	}
	return c.err
}

// conflict says why an accept cannot use a place's directory, or "".
func (c *claimCheck) conflict(ctx context.Context, action routerAction, place workerPlace) string {
	workspace, ok := c.s.workspaceOf(ctx, c.agent, place)
	if !ok || workspace.Isolated || workspace.Dir == "" {
		return ""
	}
	if err := c.load(ctx); err != nil {
		return ""
	}
	key := c.s.workspaceKey(workspace)
	waits := c.dependencies(ctx, action)
	if holder, held := c.holders[key]; held && !waits[holder.ID] {
		owner := ""
		if holder.SuperAgentID != c.agent.ID {
			owner = " of agent " + holder.SuperAgentID
		}
		return fmt.Sprintf("%s on %s is in use by ticket %s (%q)%s since %s; add it to depends_on to run after it, choose another worker, or wait",
			workspace.Dir, c.s.machineLabel(workspace.HostID), holder.ID, holder.Title, owner, holder.Workspace.HeldSince.UTC().Format(time.RFC3339))
	}
	for _, earlier := range c.pending {
		if earlier.key == key && (earlier.ref == "" || !waits[earlier.ref]) {
			name := "an earlier accept in this block"
			if earlier.ref != "" {
				name = "the accept with ref " + earlier.ref
			}
			return fmt.Sprintf("%s on %s would be in use by %s; give this one depends_on that ref, or choose another worker", workspace.Dir, c.s.machineLabel(workspace.HostID), name)
		}
	}
	return ""
}

// claim records an accept's directory for the accepts after it.
func (c *claimCheck) claim(ctx context.Context, action routerAction, place workerPlace) {
	workspace, ok := c.s.workspaceOf(ctx, c.agent, place)
	if !ok || workspace.Isolated || workspace.Dir == "" {
		return
	}
	c.pending = append(c.pending, pendingClaim{ref: strings.TrimSpace(action.Ref), key: c.s.workspaceKey(workspace), dependsOn: actionDependencies(action)})
}

func actionDependencies(action routerAction) []string {
	var out []string
	for _, id := range append(slices.Clone(action.DependsOn), action.BaseTicket) {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// dependencies are the Tickets and refs an accept waits for, directly or
// through what those wait for.
func (c *claimCheck) dependencies(ctx context.Context, action routerAction) map[string]bool {
	seen := map[string]bool{}
	queue := actionDependencies(action)
	for depth := 0; len(queue) > 0 && depth < maxDependencyDepth; depth++ {
		var next []string
		for _, id := range queue {
			if seen[id] {
				continue
			}
			seen[id] = true
			if index := slices.IndexFunc(c.pending, func(claim pendingClaim) bool { return claim.ref == id }); index >= 0 {
				next = append(next, c.pending[index].dependsOn...)
				continue
			}
			if ticket, err := c.s.store.GetSuperAgentTicket(ctx, id); err == nil && ticket != nil {
				next = append(next, ticket.DependsOn...)
				if ticket.BaseTicket != "" {
					next = append(next, ticket.BaseTicket)
				}
			}
		}
		queue = next
	}
	return seen
}

// workspaceView is a held directory as the brain is shown it.
type workspaceView struct {
	Dir     string `json:"dir"`
	Machine string `json:"machine"`
	Ticket  string `json:"ticket_id"`
	Title   string `json:"title"`
	// Agent is set when another agent's Ticket holds it.
	Agent string `json:"agent,omitempty"`
	Since string `json:"since"`
}

// WorkspaceClaim is a held directory as the Console shows it.
type WorkspaceClaim struct {
	TicketID string    `json:"ticket_id"`
	Title    string    `json:"title"`
	AgentID  string    `json:"agent_id"`
	HostID   string    `json:"host_id,omitempty"`
	Machine  string    `json:"machine"`
	Dir      string    `json:"dir"`
	Since    time.Time `json:"since"`
}

// workspaceClaims lists the directories held now on the machines an agent's
// workers use: every agent's claims there, the agent's own first.
func (s *Service) workspaceClaims(ctx context.Context, agent core.SuperAgent) []WorkspaceClaim {
	holders, err := s.workspaceHolders(ctx)
	if err != nil {
		return nil
	}
	machines := map[string]bool{s.machineKey(""): true}
	for _, worker := range agent.Workers {
		machines[s.machineKey(worker.HostID)] = true
	}
	for _, machine := range agent.WorkerMachines {
		machines[s.machineKey(machine.HostID)] = true
	}
	var out []WorkspaceClaim
	for _, ticket := range holders {
		workspace := *ticket.Workspace
		if ticket.SuperAgentID != agent.ID && !machines[s.machineKey(workspace.HostID)] {
			continue
		}
		out = append(out, WorkspaceClaim{
			TicketID: ticket.ID, Title: ticket.Title, AgentID: ticket.SuperAgentID, HostID: workspace.HostID,
			Machine: s.machineLabel(workspace.HostID), Dir: workspace.Dir, Since: workspace.HeldSince,
		})
	}
	slices.SortFunc(out, func(a, b WorkspaceClaim) int {
		if (a.AgentID == agent.ID) != (b.AgentID == agent.ID) {
			if a.AgentID == agent.ID {
				return -1
			}
			return 1
		}
		return a.Since.Compare(b.Since)
	})
	return out
}

func (s *Service) workspaceViews(ctx context.Context, agent core.SuperAgent) []workspaceView {
	now := s.now()
	var out []workspaceView
	for _, claim := range s.workspaceClaims(ctx, agent) {
		view := workspaceView{Dir: claim.Dir, Machine: claim.Machine, Ticket: claim.TicketID, Title: claim.Title, Since: ago(now, claim.Since)}
		if claim.AgentID != agent.ID {
			view.Agent = claim.AgentID
		}
		out = append(out, view)
	}
	return out
}
