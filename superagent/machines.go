package superagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// An agent can launch a worker on another machine. The agent, its Tickets and
// their history stay on the machine the agent lives on; the worker's session
// and files are on the machine it runs on. The roster names such a worker
// auto:<framework>@<machine>.

const (
	// hostedWorkerPrefix names the Agents a machine keeps for agents that
	// live elsewhere. It differs from the prefix of a machine's own auto
	// workers, so an agent here and one elsewhere with the same id never
	// share or delete each other's worker.
	hostedWorkerPrefix = "sawr_"

	hostedWorkersPath       = "/api/v1/super-agents/workers/launch"
	hostedWorkersRemovePath = "/api/v1/super-agents/workers/release"
	steerPath               = "/api/v1/invocations/steer"

	machineGuidance = "Launches a new %s worker on %s with its own session and directory there."
)

// ErrInvalid marks a request this machine refuses as it stands.
var ErrInvalid = errors.New("invalid request")

// ErrMachineOutdated means the machine's AgentMux predates hosted workers.
var ErrMachineOutdated = errors.New("this machine's AgentMux is too old to host workers for another machine; update it")

func machineWorkerRosterID(runtimeID, hostID string) string {
	return autoWorkerRosterID(runtimeID) + "@" + hostID
}

// parseAutoWorkerID splits auto:<framework> and auto:<framework>@<machine>.
func parseAutoWorkerID(id string) (runtimeID, hostID string, ok bool) {
	rest, ok := strings.CutPrefix(id, autoWorkerID+":")
	if !ok {
		return "", "", false
	}
	runtimeID, hostID, _ = strings.Cut(rest, "@")
	return runtimeID, hostID, runtimeID != ""
}

func hostedWorkerAgentID(ownerID, runtimeID string) string {
	return hostedWorkerPrefix + ownerID + "_" + runtimeID
}

// MachineFleet is what a WorkerRunner offers when it reaches other machines.
type MachineFleet interface {
	// MachineName resolves a machine the hub knows.
	MachineName(hostID string) (string, bool)
	// Runtimes lists the frameworks installed on the machine.
	Runtimes(ctx context.Context, hostID string) ([]string, error)
	// Launch has the machine keep a worker for an agent of this one.
	Launch(ctx context.Context, hostID string, request core.HostedWorkerRequest) (core.HostedWorker, error)
	// Release has the machine drop the workers it keeps for the agent.
	Release(ctx context.Context, hostID, ownerID string) error
}

func (s *Service) machineFleet() MachineFleet {
	fleet, _ := s.workers.(MachineFleet)
	return fleet
}

func agentMachine(agent core.SuperAgent, hostID string) (core.SuperAgentMachine, bool) {
	index := slices.IndexFunc(agent.WorkerMachines, func(machine core.SuperAgentMachine) bool { return machine.HostID == hostID })
	if index < 0 {
		return core.SuperAgentMachine{}, false
	}
	return agent.WorkerMachines[index], true
}

// normalizeWorkerMachines checks the machines an agent may launch workers on.
// Their directories are on those machines, and a machine may be offline while
// it is being configured, so both are checked when a worker is launched.
func (s *Service) normalizeWorkerMachines(agent *core.SuperAgent) error {
	if !agent.AutoWorkers || len(agent.WorkerMachines) == 0 {
		agent.WorkerMachines = nil
		return nil
	}
	fleet := s.machineFleet()
	if fleet == nil {
		return errors.New("this AgentMux has no other machines to launch workers on")
	}
	seen := map[string]bool{}
	machines := make([]core.SuperAgentMachine, 0, len(agent.WorkerMachines))
	for _, machine := range agent.WorkerMachines {
		machine.HostID = strings.TrimSpace(machine.HostID)
		machine.Dir = strings.TrimSpace(machine.Dir)
		machine.Capabilities = strings.TrimSpace(machine.Capabilities)
		if machine.HostID == "" {
			return errors.New("each worker machine must name a machine")
		}
		if strings.ContainsAny(machine.HostID, "@ \t\n") {
			return fmt.Errorf("machine id %q cannot be used for workers", machine.HostID)
		}
		if seen[machine.HostID] {
			return fmt.Errorf("machine %q is listed twice", machine.HostID)
		}
		seen[machine.HostID] = true
		if _, known := fleet.MachineName(machine.HostID); !known {
			return fmt.Errorf("machine %q is not connected to this AgentMux", machine.HostID)
		}
		if machine.Dir != "" && !strings.HasPrefix(machine.Dir, "/") && machine.Dir != "~" && !strings.HasPrefix(machine.Dir, "~/") {
			return fmt.Errorf("the directory on machine %q must be an absolute path or start with ~/", machine.HostID)
		}
		machines = append(machines, machine)
	}
	agent.WorkerMachines = machines
	return nil
}

// machineStatuses asks each of the agent's machines what it can run.
func (s *Service) machineStatuses(ctx context.Context, agent core.SuperAgent) []core.SuperAgentMachineStatus {
	statuses := make([]core.SuperAgentMachineStatus, len(agent.WorkerMachines))
	fleet := s.machineFleet()
	var wg sync.WaitGroup
	for index, machine := range agent.WorkerMachines {
		statuses[index] = core.SuperAgentMachineStatus{HostID: machine.HostID, Name: machine.HostID}
		if fleet == nil {
			statuses[index].Detail = "other machines are unavailable"
			continue
		}
		name, known := fleet.MachineName(machine.HostID)
		if !known {
			statuses[index].Detail = "machine is no longer connected to this AgentMux"
			continue
		}
		statuses[index].Name = name
		wg.Add(1)
		go func() {
			defer wg.Done()
			statusCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			runtimes, err := fleet.Runtimes(statusCtx, machine.HostID)
			if err != nil {
				statuses[index].Detail = err.Error()
				return
			}
			statuses[index].Available, statuses[index].Runtimes = true, runtimes
		}()
	}
	wg.Wait()
	return statuses
}

// checkMachineWorker is launchMachineWorker without launching anything.
func checkMachineWorker(agent core.SuperAgent, workerID, runtimeID, hostID string) error {
	if !agent.AutoWorkers {
		return errors.New("this Super Agent cannot launch workers; choose one from the roster")
	}
	if _, allowed := agentMachine(agent, hostID); !allowed || !slices.Contains(agent.WorkerRuntimes, runtimeID) {
		return fmt.Errorf("worker %q is not on the roster", workerID)
	}
	return nil
}

// launchMachineWorker gives a new Ticket a worker on another machine. The
// operator's settings are sent as they are; the machine applies its own
// checks to them and answers with the Agent it keeps for this agent.
func (s *Service) launchMachineWorker(ctx context.Context, agent core.SuperAgent, runtimeID, hostID string) (core.SuperAgentWorker, error) {
	workerID := machineWorkerRosterID(runtimeID, hostID)
	if err := checkMachineWorker(agent, workerID, runtimeID, hostID); err != nil {
		return core.SuperAgentWorker{}, fmt.Errorf("router chose worker %q: %w", workerID, err)
	}
	fleet := s.machineFleet()
	if fleet == nil {
		return core.SuperAgentWorker{}, errors.New("this AgentMux has no other machines to launch workers on")
	}
	machine, _ := agentMachine(agent, hostID)
	launchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	hosted, err := fleet.Launch(launchCtx, hostID, core.HostedWorkerRequest{
		OwnerID: agent.ID, OwnerName: agent.Name, RuntimeID: runtimeID,
		Dir: machine.Dir, ApprovalMode: agent.AutoWorkerApprovalMode,
	})
	if err != nil {
		return core.SuperAgentWorker{}, err
	}
	return core.SuperAgentWorker{ID: workerID, Label: hosted.Name, HostID: hostID, AgentID: hosted.AgentID}, nil
}

// releaseMachineWorkers has machines drop the workers they keep for an agent.
// It is best effort: an offline machine keeps an idle Agent nobody can reach.
func (s *Service) releaseMachineWorkers(ctx context.Context, agentID string, machines []core.SuperAgentMachine) {
	fleet := s.machineFleet()
	if fleet == nil {
		return
	}
	for _, machine := range machines {
		releaseCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := fleet.Release(releaseCtx, machine.HostID, agentID); err != nil {
			s.log.Warn("release workers on machine", "agent", agentID, "machine", machine.HostID, "err", err)
		}
		cancel()
	}
}

// removedMachines are the machines in before that are not in after.
func removedMachines(before, after []core.SuperAgentMachine) []core.SuperAgentMachine {
	var removed []core.SuperAgentMachine
	for _, machine := range before {
		if !slices.ContainsFunc(after, func(kept core.SuperAgentMachine) bool { return kept.HostID == machine.HostID }) {
			removed = append(removed, machine)
		}
	}
	return removed
}

// HostWorker keeps a worker on this machine for an agent that lives on
// another. Nothing in the request is trusted to be sensible here: the
// framework must be installed, the directory must exist, and the approval
// mode must be one this machine lets unattended workers use. The worker uses
// this machine's own provider route, tools and environment.
func (s *Service) HostWorker(ctx context.Context, request core.HostedWorkerRequest) (core.HostedWorker, error) {
	ownerID, runtimeID := strings.TrimSpace(request.OwnerID), strings.TrimSpace(request.RuntimeID)
	if err := validSuperAgentID(ownerID); err != nil || len(ownerID) > 128 {
		return core.HostedWorker{}, fmt.Errorf("%w: invalid owner id", ErrInvalid)
	}
	if runtimeID == "" || (s.runtimeAvailable != nil && !s.runtimeAvailable(runtimeID)) {
		return core.HostedWorker{}, fmt.Errorf("%w: worker framework %q is not installed on this machine", ErrInvalid, runtimeID)
	}
	mode := strings.TrimSpace(request.ApprovalMode)
	if mode == "" {
		mode = defaultAutoWorkerApproval(runtimeID)
	} else if err := validateAutoWorkerApproval(mode); err != nil {
		return core.HostedWorker{}, fmt.Errorf("%w: %s", ErrInvalid, err)
	} else if modes := core.ApprovalModeValuesForRuntime(runtimeID); len(modes) > 0 && !core.ApprovalModeSupported(runtimeID, mode) {
		return core.HostedWorker{}, fmt.Errorf("%w: approval mode %q is not supported by %s", ErrInvalid, mode, runtimeID)
	}
	workDir, err := s.hostedWorkerDir(ownerID, request.Dir)
	if err != nil {
		return core.HostedWorker{}, err
	}
	id := hostedWorkerAgentID(ownerID, runtimeID)
	now := s.now().UTC()
	createdAt := now
	existing, err := s.store.GetAgentInstance(ctx, id)
	if err != nil {
		return core.HostedWorker{}, err
	}
	if existing != nil {
		if !IsManagedAgent(*existing) {
			return core.HostedWorker{}, fmt.Errorf("%w: agent %q already exists on this machine", ErrConflict, id)
		}
		createdAt = existing.CreatedAt
	}
	name := fmt.Sprintf("%s worker (%s)", firstNonEmpty(strings.TrimSpace(request.OwnerName), ownerID), runtimeID)
	instance := core.AgentInstance{
		ID: id, Name: name, RuntimeID: runtimeID, SessionBackend: "structured",
		WorkDir: workDir, WorkspaceMode: autoWorkerWorkspaceMode(ctx, workDir),
		DefaultApprovalMode: mode, Enabled: true, Source: autoWorkerAgentSource,
		CreatedAt: createdAt, UpdatedAt: now,
	}
	if err := s.store.UpsertAgentInstance(ctx, &instance); err != nil {
		return core.HostedWorker{}, err
	}
	return core.HostedWorker{AgentID: id, Name: name, WorkDir: workDir, WorkspaceMode: instance.WorkspaceMode}, nil
}

func (s *Service) hostedWorkerDir(ownerID, dir string) (string, error) {
	if dir = strings.TrimSpace(dir); dir != "" {
		expanded, err := expandHome(dir)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(expanded) {
			return "", fmt.Errorf("%w: worker directory %q must be an absolute path", ErrInvalid, dir)
		}
		if info, err := os.Stat(expanded); err != nil || !info.IsDir() {
			return "", fmt.Errorf("%w: worker directory %q does not exist on this machine", ErrInvalid, dir)
		}
		return expanded, nil
	}
	state, err := s.stateDir()
	if err != nil {
		return "", err
	}
	work := filepath.Join(state, "hosted-workers", ownerID, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return "", fmt.Errorf("create worker directory: %w", err)
	}
	return work, nil
}

// ReleaseHostedWorkers removes the workers this machine keeps for an agent
// that lives on another. Their files stay.
func (s *Service) ReleaseHostedWorkers(ctx context.Context, ownerID string) (int, error) {
	ownerID = strings.TrimSpace(ownerID)
	if err := validSuperAgentID(ownerID); err != nil {
		return 0, fmt.Errorf("%w: invalid owner id", ErrInvalid)
	}
	instances, err := s.store.ListAgentInstances(ctx)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, instance := range instances {
		if !IsManagedAgent(instance) || instance.ID != hostedWorkerAgentID(ownerID, instance.RuntimeID) {
			continue
		}
		if err := s.store.DeleteAgentInstance(ctx, instance.ID); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// --- the Fleet's side: calling another machine ---

func (f *Fleet) MachineName(hostID string) (string, bool) {
	if f.Hosts == nil {
		return "", false
	}
	host, ok := f.Hosts.Get(hostID)
	if !ok {
		return "", false
	}
	return firstNonEmpty(host.Name, hostID), true
}

// remoteJSON calls a machine's API and decodes its answer.
func (f *Fleet) remoteJSON(ctx context.Context, hostID, method, path string, body, output any) (int, error) {
	host, client, err := f.remoteClient(hostID)
	if err != nil {
		return 0, err
	}
	var payload []byte
	if body != nil {
		if payload, err = json.Marshal(body); err != nil {
			return 0, err
		}
	}
	request, err := remoteRequest(ctx, host, method, path, bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, retryableError{fmt.Errorf("%s: %w", host.Name, err)}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		message := fmt.Errorf("%s: %s", host.Name, remoteErrorMessage(response))
		if response.StatusCode >= http.StatusBadGateway {
			return response.StatusCode, retryableError{message}
		}
		return response.StatusCode, message
	}
	if output == nil {
		return response.StatusCode, nil
	}
	if err := json.NewDecoder(response.Body).Decode(output); err != nil {
		// An older daemon answers an unknown API path with the Console page.
		return http.StatusNotFound, fmt.Errorf("%s: unexpected answer", host.Name)
	}
	return response.StatusCode, nil
}

func (f *Fleet) Runtimes(ctx context.Context, hostID string) ([]string, error) {
	var runtimes []string
	if _, err := f.remoteJSON(ctx, hostID, http.MethodGet, "/api/v1/agents", nil, &runtimes); err != nil {
		return nil, err
	}
	return runtimes, nil
}

func (f *Fleet) Launch(ctx context.Context, hostID string, request core.HostedWorkerRequest) (core.HostedWorker, error) {
	var hosted core.HostedWorker
	status, err := f.remoteJSON(ctx, hostID, http.MethodPost, hostedWorkersPath, request, &hosted)
	if status == http.StatusNotFound || status == http.StatusMethodNotAllowed || (err == nil && hosted.AgentID == "") {
		name, _ := f.MachineName(hostID)
		return core.HostedWorker{}, fmt.Errorf("%s: %w", firstNonEmpty(name, hostID), ErrMachineOutdated)
	}
	return hosted, err
}

func (f *Fleet) Release(ctx context.Context, hostID, ownerID string) error {
	status, err := f.remoteJSON(ctx, hostID, http.MethodPost, hostedWorkersRemovePath, map[string]string{"owner_id": ownerID}, nil)
	if status == http.StatusNotFound || status == http.StatusMethodNotAllowed {
		return nil // Nothing there could have hosted a worker.
	}
	return err
}

// steerRemote adds text to a turn running on another machine. A machine that
// cannot do it leaves the input for the next turn, as before.
func (f *Fleet) steerRemote(ctx context.Context, worker core.SuperAgentWorker, conversationID, text string) error {
	var answer struct {
		Code string `json:"code"`
	}
	body := map[string]string{"agent_id": worker.AgentID, "conversation_id": conversationID, "text": text}
	status, err := f.remoteJSON(ctx, worker.HostID, http.MethodPost, steerPath, body, &answer)
	switch {
	case status == http.StatusConflict:
		return core.ErrInvocationIdle
	case status == http.StatusNotFound, status == http.StatusMethodNotAllowed, status == http.StatusNotImplemented:
		return core.ErrSteerUnsupported
	}
	return err
}
