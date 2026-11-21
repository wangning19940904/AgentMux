package superagent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/framework"
	"github.com/wangning19940904/AgentMux/framework/health"
)

// Placement: where a new Ticket runs. The roster the brain sees marks each
// framework whose health record on its machine is failing or usage-limited.
// An accept's machine_policy says what happens when the place it names cannot
// take work now: fixed (the default) refuses the action, so the brain chooses
// again; flexible lets the host move the Ticket to another place this agent
// may already use, and records the move on the Ticket. Health comes from the
// records each machine keeps (framework/health); placement never runs a check.
// A runtime whose state is unknown is never held back.

const (
	// machineInfoTTL bounds how old a machine's frameworks and health may be
	// when an accept is checked. The roster the brain just saw fills it, so an
	// accept seldom waits on the network.
	machineInfoTTL = 60 * time.Second
	// runFailureTTL bounds how long a framework failure seen in a Ticket on
	// another machine marks it here, unless that machine checks it sooner.
	runFailureTTL   = time.Hour
	machineCallWait = 10 * time.Second
)

// LocalHealth is the health service of this machine's frameworks;
// *health.Service implements it.
type LocalHealth interface {
	Status(ctx context.Context, kind string) (health.Record, error)
	RecordFailure(ctx context.Context, kind, errText string) (health.Record, bool, error)
	Settings(ctx context.Context) (health.Settings, error)
}

// MachineHealthFleet is a WorkerRunner that can read another machine's
// framework health records, as that machine's GET /api/v1/frameworks/health
// returns them.
type MachineHealthFleet interface {
	Health(ctx context.Context, hostID string) ([]health.Record, error)
}

// machineInfo is what a machine said recently: its frameworks and their
// health. err is set when it could not be reached.
type machineInfo struct {
	at       time.Time
	err      error
	runtimes []string
	health   map[string]health.Record
	// healthAt is when health was read; zero when the machine cannot say.
	healthAt time.Time
}

// placementCache keeps what machines said, and failures of real runs seen
// on other machines.
type placementCache struct {
	mu       sync.Mutex
	machines map[string]machineInfo
	failures map[string]health.Record
	// uids are machine UIDs by host id, for workspace claims.
	uids map[string]cachedUID
}

func failureKey(hostID, runtime string) string { return hostID + "|" + runtime }

// workerPlace is where a roster entry's work runs.
type workerPlace struct {
	WorkerID string
	// Runtime is the framework; empty when the machine did not say.
	Runtime string
	// Profile is an auto worker's profile; empty for the plain one.
	Profile string
	HostID  string
	// Place names the directory: an auto worker's path ID ("" for its
	// default directory) or a roster worker's id.
	Place  string
	Roster bool
	index  int
}

// placeOf resolves a roster entry without launching anything.
func (s *Service) placeOf(ctx context.Context, agent core.SuperAgent, workerID string) (workerPlace, error) {
	if runtimeID, profile, pathID, ok := parseAutoWorker(workerID); ok {
		place := workerPlace{WorkerID: workerID, Runtime: runtimeID, Profile: profile, Place: pathID}
		if pathID != "" {
			machine, found := agentMachine(agent, pathID)
			if !found {
				return workerPlace{}, fmt.Errorf("worker %q is not on the roster", workerID)
			}
			place.HostID = machine.HostID
		}
		return place, nil
	}
	index := slices.IndexFunc(agent.Workers, func(worker core.SuperAgentWorker) bool { return worker.ID == workerID })
	if index < 0 {
		return workerPlace{}, fmt.Errorf("worker %q is not on the roster", workerID)
	}
	worker := agent.Workers[index]
	place := workerPlace{WorkerID: workerID, HostID: worker.HostID, Place: "roster:" + worker.ID, Roster: true, index: index}
	if worker.HostID == "" {
		if instance, err := s.store.GetAgentInstance(ctx, worker.AgentID); err == nil && instance != nil {
			place.Runtime = instance.RuntimeID
		}
	} else {
		statusCtx, cancel := context.WithTimeout(ctx, machineCallWait)
		place.Runtime = s.workers.Status(statusCtx, worker).RuntimeID
		cancel()
	}
	return place, nil
}

// candidatePlaces are every roster entry the agent may name.
func (s *Service) candidatePlaces(ctx context.Context, agent core.SuperAgent) []workerPlace {
	var out []workerPlace
	for _, worker := range agent.Workers {
		if place, err := s.placeOf(ctx, agent, worker.ID); err == nil {
			out = append(out, place)
		}
	}
	if !agent.AutoWorkers {
		return out
	}
	for _, runtimeID := range agent.WorkerRuntimes {
		for _, profile := range runtimeVariants(agent, runtimeID) {
			out = append(out, workerPlace{WorkerID: autoRosterID(runtimeID, profile, ""), Runtime: runtimeID, Profile: profile})
		}
	}
	for _, machine := range agent.WorkerMachines {
		pathID := machinePathID(machine)
		for _, runtimeID := range agent.WorkerRuntimes {
			for _, profile := range runtimeVariants(agent, runtimeID) {
				out = append(out, workerPlace{WorkerID: autoRosterID(runtimeID, profile, pathID), Runtime: runtimeID, Profile: profile, HostID: machine.HostID, Place: pathID})
			}
		}
	}
	for index := range out {
		out[index].index = index
	}
	return out
}

// machineState returns what a machine said within machineInfoTTL, asking it when
// nothing that recent is known.
func (s *Service) machineState(ctx context.Context, hostID string) machineInfo {
	s.placement.mu.Lock()
	info, ok := s.placement.machines[hostID]
	s.placement.mu.Unlock()
	if ok && s.now().Sub(info.at) < machineInfoTTL {
		return info
	}
	return s.refreshMachine(ctx, hostID)
}

// refreshMachine asks a machine for its frameworks and their health.
func (s *Service) refreshMachine(ctx context.Context, hostID string) machineInfo {
	info := machineInfo{at: s.now()}
	fleet := s.machineFleet()
	if fleet == nil {
		info.err = errors.New("other machines are unavailable")
		return info
	}
	callCtx, cancel := context.WithTimeout(ctx, machineCallWait)
	defer cancel()
	info.runtimes, info.err = fleet.Runtimes(callCtx, hostID)
	s.placement.mu.Lock()
	previous := s.placement.machines[hostID]
	s.placement.mu.Unlock()
	if info.err == nil && !previous.healthAt.IsZero() && s.now().Sub(previous.healthAt) < machineInfoTTL/2 {
		// The Console asks every few seconds; health records change slowly,
		// so a recent read is reused.
		info.health, info.healthAt = previous.health, previous.healthAt
	} else if info.err == nil {
		if reader, ok := s.workers.(MachineHealthFleet); ok {
			// A machine too old to keep health records leaves them unknown.
			if records, err := reader.Health(callCtx, hostID); err == nil {
				info.health, info.healthAt = map[string]health.Record{}, s.now()
				for _, record := range records {
					info.health[record.Kind] = record
				}
			}
		}
	}
	if ctx.Err() == nil {
		s.placement.mu.Lock()
		if s.placement.machines == nil {
			s.placement.machines = map[string]machineInfo{}
		}
		s.placement.machines[hostID] = info
		s.placement.mu.Unlock()
	}
	return info
}

// runtimeHealth is a framework's health on a machine. Anything that cannot
// be read is unknown, which holds nothing back.
func (s *Service) runtimeHealth(ctx context.Context, hostID, runtime string) health.Record {
	unknown := health.Record{Kind: runtime, State: health.StateUnknown}
	if runtime == "" {
		return unknown
	}
	if hostID == "" {
		if s.healthRecords == nil {
			return unknown
		}
		record, err := s.healthRecords.Status(ctx, runtime)
		if err != nil {
			return unknown
		}
		return record
	}
	info := s.machineState(ctx, hostID)
	record, ok := info.health[runtime]
	if !ok {
		record = unknown
	}
	s.placement.mu.Lock()
	failure, failed := s.placement.failures[failureKey(hostID, runtime)]
	s.placement.mu.Unlock()
	// A failure seen here wins until the machine's own record is newer.
	if failed && s.now().Sub(failure.CheckedAt) < runFailureTTL && !record.CheckedAt.After(failure.CheckedAt) {
		return failure
	}
	return record
}

func unhealthy(record health.Record) bool {
	return record.State == health.StateFailing || record.State == health.StateUsageLimit
}

// healthLine says why a framework cannot take work now.
func healthLine(record health.Record) string {
	line := string(record.State)
	if record.State == health.StateUsageLimit {
		line = "usage-limited"
		if record.ResetsAt != nil {
			line += " until " + record.ResetsAt.UTC().Format(time.RFC3339)
		}
	}
	if reason := oneLine(record.Reason, 200); reason != "" {
		line += ": " + reason
	}
	return line
}

// placeProblem says why a place cannot take a new Ticket now, or "" when it
// can. A machine that cannot be reached, a framework missing there, and a
// framework failing or at its usage limit are problems; unknown health is not.
func (s *Service) placeProblem(ctx context.Context, agent core.SuperAgent, place workerPlace) string {
	where := "this machine"
	if place.HostID != "" {
		where = place.HostID
		if fleet := s.machineFleet(); fleet != nil {
			if name, ok := fleet.MachineName(place.HostID); ok {
				where = name
			}
		}
		if place.Roster {
			statusCtx, cancel := context.WithTimeout(ctx, machineCallWait)
			status := s.workers.Status(statusCtx, agent.Workers[place.index])
			cancel()
			if !status.Available {
				return fmt.Sprintf("%s on %s cannot be reached: %s", place.WorkerID, where, firstNonEmpty(status.Detail, "no answer"))
			}
		} else {
			info := s.machineState(ctx, place.HostID)
			if info.err != nil {
				return fmt.Sprintf("machine %s is offline: %s", where, oneLine(info.err.Error(), 200))
			}
			if !slices.Contains(info.runtimes, place.Runtime) {
				return fmt.Sprintf("%s is not installed on %s", place.Runtime, where)
			}
		}
	}
	if record := s.runtimeHealth(ctx, place.HostID, place.Runtime); unhealthy(record) {
		return fmt.Sprintf("%s on %s is %s", place.Runtime, where, healthLine(record))
	}
	return ""
}

// placement is where preflight decided an accept runs.
type placement struct {
	place workerPlace
	// moved is set when the host moved the Ticket from where the brain named.
	moved *core.SuperAgentTicketPlacement
}

// checkMachinePolicy validates an accept's machine_policy.
func checkMachinePolicy(policy string) error {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "", core.MachinePolicyFixed, core.MachinePolicyFlexible:
		return nil
	}
	return fmt.Errorf("machine_policy must be %q or %q", core.MachinePolicyFixed, core.MachinePolicyFlexible)
}

// place decides where an accept runs. avoid reports a further reason a place
// cannot take the Ticket, such as a directory another Ticket holds; it may
// be nil.
func (s *Service) place(ctx context.Context, agent core.SuperAgent, action routerAction, avoid func(workerPlace) string) (placement, error) {
	named, err := s.placeOf(ctx, agent, action.WorkerID)
	if err != nil {
		return placement{}, err
	}
	problem := s.placeProblem(ctx, agent, named)
	if problem == "" && avoid != nil {
		problem = avoid(named)
	}
	if problem == "" {
		return placement{place: named}, nil
	}
	if strings.ToLower(strings.TrimSpace(action.MachinePolicy)) != core.MachinePolicyFlexible {
		return placement{}, fmt.Errorf("worker %s cannot take this ticket now: %s. Choose another worker from the roster, wait, or accept with \"machine_policy\":\"flexible\" to let AgentMux pick another place", action.WorkerID, problem)
	}
	candidates := s.candidatePlaces(ctx, agent)
	// Closest first: the same directory, then the same framework, then the
	// same profile of it, then the same machine, and otherwise roster order.
	score := func(candidate workerPlace) int {
		value := 0
		if candidate.HostID == named.HostID && candidate.Place == named.Place {
			value += 8
		}
		if candidate.Runtime != "" && candidate.Runtime == named.Runtime {
			value += 4
			if candidate.Profile == named.Profile {
				value += 2
			}
		}
		if candidate.HostID == named.HostID {
			value++
		}
		return value
	}
	sort.SliceStable(candidates, func(i, j int) bool { return score(candidates[i]) > score(candidates[j]) })
	var tried []string
	for _, candidate := range candidates {
		if candidate.WorkerID == named.WorkerID {
			continue
		}
		reason := s.placeProblem(ctx, agent, candidate)
		if reason == "" && avoid != nil {
			reason = avoid(candidate)
		}
		if reason != "" {
			tried = append(tried, candidate.WorkerID+" ("+reason+")")
			continue
		}
		moved := &core.SuperAgentTicketPlacement{
			Policy: core.MachinePolicyFlexible, RequestedWorkerID: named.WorkerID, WorkerID: candidate.WorkerID,
			HostID: candidate.HostID, Machine: s.machineLabel(candidate.HostID), Reason: problem, At: s.now().UTC(),
		}
		return placement{place: candidate, moved: moved}, nil
	}
	message := fmt.Sprintf("worker %s cannot take this ticket now (%s), and no other place this agent may use can", action.WorkerID, problem)
	if len(tried) > 0 {
		message += ": " + strings.Join(tried, "; ")
	}
	return placement{}, errors.New(message)
}

// machineLabel names a machine for people.
func (s *Service) machineLabel(hostID string) string {
	if hostID == "" {
		return "this machine"
	}
	if fleet := s.machineFleet(); fleet != nil {
		if name, ok := fleet.MachineName(hostID); ok {
			return name
		}
	}
	return hostID
}

// noteRunFailure feeds a failed turn into the health of the framework that
// ran it. Only failures the framework owns count (health.ClassifyRunError).
// On this machine the record is stored, unless periodic checks are on, in
// which case the engine already observed the failure and confirms it. On
// another machine it marks that framework here until the machine's own record
// is newer.
func (s *Service) noteRunFailure(ctx context.Context, agent core.SuperAgent, worker core.SuperAgentWorker, runErr error) {
	if runErr == nil || errors.Is(runErr, context.Canceled) {
		return
	}
	text := runErr.Error()
	kind, ok := health.ClassifyRunError(text)
	if !ok {
		return
	}
	runtime := ""
	if runtimeID, _, auto := parseAutoWorkerID(worker.ID); auto {
		runtime = runtimeID
	} else if place, err := s.placeOf(ctx, agent, worker.ID); err == nil {
		runtime = place.Runtime
	}
	if runtime == "" {
		return
	}
	if worker.HostID == "" {
		if s.healthRecords == nil {
			return
		}
		if settings, err := s.healthRecords.Settings(ctx); err == nil && settings.Enabled {
			return
		}
		if _, _, err := s.healthRecords.RecordFailure(ctx, runtime, text); err != nil {
			s.log.Warn("record framework failure", "runtime", runtime, "err", err)
		}
		return
	}
	state := health.StateFailing
	if kind == framework.SmokeUsageLimit {
		state = health.StateUsageLimit
	}
	s.placement.mu.Lock()
	if s.placement.failures == nil {
		s.placement.failures = map[string]health.Record{}
	}
	s.placement.failures[failureKey(worker.HostID, runtime)] = health.Record{
		Kind: runtime, State: state, Reason: oneLine(text, 300), ErrorKind: kind,
		Source: health.SourceRun, CheckedAt: s.now().UTC(),
	}
	s.placement.mu.Unlock()
}

// rosterHealth is the health of every framework on the agent's roster, on
// each machine it runs there: this machine's worker frameworks, each listed
// machine's, and each roster worker's.
func (s *Service) rosterHealth(ctx context.Context, agent core.SuperAgent, statuses []core.SuperAgentWorkerStatus) []core.SuperAgentRuntimeHealth {
	seen := map[string]bool{}
	var out []core.SuperAgentRuntimeHealth
	add := func(hostID, runtime string) {
		key := failureKey(hostID, runtime)
		if runtime == "" || seen[key] {
			return
		}
		seen[key] = true
		record := s.runtimeHealth(ctx, hostID, runtime)
		entry := core.SuperAgentRuntimeHealth{HostID: hostID, Runtime: runtime, State: string(record.State), ResetsAt: record.ResetsAt}
		if entry.State == "" {
			entry.State = string(health.StateUnknown)
		}
		if unhealthy(record) {
			entry.Reason = oneLine(record.Reason, 300)
		}
		out = append(out, entry)
	}
	if agent.AutoWorkers {
		for _, runtimeID := range agent.WorkerRuntimes {
			add("", runtimeID)
		}
		for _, machine := range agent.WorkerMachines {
			for _, runtimeID := range agent.WorkerRuntimes {
				add(machine.HostID, runtimeID)
			}
		}
	}
	for index, worker := range agent.Workers {
		if index < len(statuses) {
			add(worker.HostID, statuses[index].RuntimeID)
		}
	}
	return out
}

// healthIndex looks a framework's health up by machine.
func healthIndex(records []core.SuperAgentRuntimeHealth) map[string]core.SuperAgentRuntimeHealth {
	index := make(map[string]core.SuperAgentRuntimeHealth, len(records))
	for _, record := range records {
		index[failureKey(record.HostID, record.Runtime)] = record
	}
	return index
}

// rosterHealthMark is what the roster says about a framework that cannot
// take work now; "" otherwise.
func rosterHealthMark(record core.SuperAgentRuntimeHealth, ok bool) string {
	if !ok || (record.State != string(health.StateFailing) && record.State != string(health.StateUsageLimit)) {
		return ""
	}
	return healthLine(health.Record{State: health.State(record.State), Reason: record.Reason, ResetsAt: record.ResetsAt})
}

// placementNote explains health marks and machine_policy to the brain. It is
// shown only when it applies: some entry is marked, or work can go to more
// than one machine.
const placementNote = `Entries with "health" have a framework that is failing or at its usage limit on that machine, as its own records say; AgentMux refuses an accept there. An accept may add "machine_policy": "fixed" (the default) runs it exactly where you named or refuses it so you can choose again; "flexible" lets AgentMux move it to another entry of this roster when the one you named cannot take it now (offline machine, failing or usage-limited framework, busy directory); the ticket then shows where it went under "moved".`
