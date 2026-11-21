package superagent

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/wangning19940904/AgentMux/core"
)

// Bounded dispatch. A worker running a Ticket of an agent that allows it may
// hand a sub-task to another worker on the agent's roster, a helper, and read
// the helper's result. The host enforces every limit:
//
//   - the agent's dispatch setting is on;
//   - the call carries the Ticket's own token, which the worker was given in
//     its first input, so it acts for that Ticket and agent only;
//   - one level: a helper is given no token, and a token names a Ticket;
//   - at most maxRunningHelpers helpers run at once for a Ticket, and
//     maxTicketHelpers are started in all;
//   - the helper's worker is one the agent's roster allows;
//   - helpers run only while the Ticket's turn runs, and stop when it ends.
//
// A worker reaches the host through its own machine's AgentMux. A machine
// that does not hold the Ticket passes the call up its hub link: through the
// worker relay to the agent's machine, or to the hub itself when the agent
// lives there.

const (
	DispatchStartPath = "/api/v1/super-agents/dispatch/start"
	DispatchReadPath  = "/api/v1/super-agents/dispatch/read"
	// The hub takes calls for its own agents up the node link here.
	DispatchUplinkStartPath = "/uplink/v1/dispatch/start"
	DispatchUplinkReadPath  = "/uplink/v1/dispatch/read"

	// DispatchHomeHub is the home of a Ticket whose agent lives on the hub.
	DispatchHomeHub = "hub"

	DispatchCallStart = "start"
	DispatchCallRead  = "read"

	maxRunningHelpers      = 3
	maxTicketHelpers       = 12
	maxHelperInstructions  = 20000
	maxHelperTitle         = 200
	maxHelperResult        = 60000
	maxDispatchWait        = 30 * time.Second
	dispatchTokenPrefix    = "amd1"
	helperIDPrefix         = "hlp_"
	helperRecordTimeout    = 10 * time.Second
	dispatchForwardTimeout = 2 * time.Minute
)

// DispatchHost is what the service needs from the daemon to offer dispatch:
// where this machine's API listens, the command a local worker runs, and how
// this machine is known to its hub.
type DispatchHost struct {
	// Home names this machine to the hub: its device id when it is linked to
	// a hub, DispatchHomeHub when it is the hub, or "".
	Home func() string
	// Uplink reaches the hub, or is nil.
	Uplink HubRelay
	// Addr is this machine's API on the loopback address, as host:port.
	Addr string
	// Command is the amux executable a local worker can run, or "" when there
	// is none; local workers then get the HTTP calls instead.
	Command string
}

// dispatcher keeps the helpers that run now, by parent Ticket.
type dispatcher struct {
	mu      sync.Mutex
	host    DispatchHost
	running map[string]map[string]*helperRun
	wg      sync.WaitGroup
}

type helperRun struct {
	cancel context.CancelFunc
	done   chan struct{}
	// reason says why the helper was stopped, once it was.
	reason string
}

// SetDispatchHost tells the service how workers reach this machine.
func (s *Service) SetDispatchHost(host DispatchHost) {
	s.dispatch.mu.Lock()
	s.dispatch.host = host
	s.dispatch.mu.Unlock()
}

func (s *Service) dispatchHost() DispatchHost {
	s.dispatch.mu.Lock()
	defer s.dispatch.mu.Unlock()
	return s.dispatch.host
}

func (s *Service) dispatchHome() string {
	if home := s.dispatchHost().Home; home != nil {
		return home()
	}
	return ""
}

// DispatchRequest is a worker's call. Start names a worker (or a runtime,
// short for auto:<runtime>) and instructions; read names a helper.
type DispatchRequest struct {
	Token        string `json:"token"`
	Worker       string `json:"worker,omitempty"`
	Runtime      string `json:"runtime,omitempty"`
	Title        string `json:"title,omitempty"`
	Instructions string `json:"instructions,omitempty"`
	ID           string `json:"id,omitempty"`
	// WaitSeconds lets a read wait, up to 30 seconds, for a running helper
	// to finish.
	WaitSeconds int `json:"wait_seconds,omitempty"`
}

// DispatchResult is a helper as its parent's worker sees it.
type DispatchResult struct {
	ID         string    `json:"id"`
	Status     string    `json:"status"`
	WorkerID   string    `json:"worker_id"`
	Title      string    `json:"title,omitempty"`
	Result     string    `json:"result,omitempty"`
	Error      string    `json:"error,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
	// Running is how many of the Ticket's helpers run now.
	Running int `json:"running"`
}

// DispatchError is a refused call, with the HTTP status that says why.
type DispatchError struct {
	Status  int
	Message string
}

func (e DispatchError) Error() string { return e.Message }

func dispatchRefusal(status int, format string, args ...any) error {
	return DispatchError{Status: status, Message: fmt.Sprintf(format, args...)}
}

// DispatchElsewhere says the Ticket lives on another machine, which Home
// names to the hub.
type DispatchElsewhere struct{ Home string }

func (e DispatchElsewhere) Error() string {
	return "this Ticket's agent is on another machine"
}

// dispatchToken is a parsed token: amd1.<home>.<ticket id>.<secret>.
type dispatchToken struct {
	home, ticketID string
}

func parseDispatchToken(token string) (dispatchToken, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(token), dispatchTokenPrefix+".")
	if !ok {
		return dispatchToken{}, false
	}
	last := strings.LastIndex(rest, ".")
	if last < 0 {
		return dispatchToken{}, false
	}
	rest, secret := rest[:last], rest[last+1:]
	middle := strings.LastIndex(rest, ".")
	if middle < 0 || secret == "" {
		return dispatchToken{}, false
	}
	parsed := dispatchToken{home: rest[:middle], ticketID: rest[middle+1:]}
	// Only a Ticket holds a token; a helper is never given one.
	if !strings.HasPrefix(parsed.ticketID, "tkt_") {
		return dispatchToken{}, false
	}
	return parsed, true
}

func dispatchKey(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

// offerDispatch gives a new Ticket its token and tells its worker about
// helpers, when the agent allows dispatch and the worker can reach this
// machine. It changes nothing otherwise.
func (s *Service) offerDispatch(agent core.SuperAgent, ticket *core.SuperAgentTicket) {
	if !agent.Dispatch || len(ticket.Inputs) == 0 {
		return
	}
	host := s.dispatchHost()
	home := ""
	if host.Home != nil {
		home = host.Home()
	}
	if ticket.Worker.HostID != "" && home == "" {
		// A worker on another machine reaches this one only through a hub.
		return
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		s.log.Warn("no dispatch token for a ticket", "ticket", ticket.ID, "err", err)
		return
	}
	token := strings.Join([]string{dispatchTokenPrefix, home, ticket.ID, base64.RawURLEncoding.EncodeToString(secret)}, ".")
	ticket.DispatchKey = dispatchKey(token)
	ticket.Inputs[0] += "\n\n" + dispatchBrief(agent, *ticket, host, token)
}

// dispatchWorkerIDs are the workers a helper may run on: the roster and the
// auto workers the agent may launch.
func dispatchWorkerIDs(agent core.SuperAgent) []string {
	var ids []string
	for _, worker := range agent.Workers {
		ids = append(ids, worker.ID)
	}
	if agent.AutoWorkers {
		for _, runtimeID := range agent.WorkerRuntimes {
			for _, profile := range runtimeVariants(agent, runtimeID) {
				ids = append(ids, autoRosterID(runtimeID, profile, ""))
				for _, machine := range agent.WorkerMachines {
					ids = append(ids, autoRosterID(runtimeID, profile, machinePathID(machine)))
				}
			}
		}
	}
	return ids
}

// dispatchBrief tells a Ticket's worker how to start and read helpers, and
// the limits the host keeps.
func dispatchBrief(agent core.SuperAgent, ticket core.SuperAgentTicket, host DispatchHost, token string) string {
	var brief strings.Builder
	brief.WriteString("HELPERS: this agent lets you hand a self-contained sub-task to another of its workers, a helper, and read its result while you work on this Ticket.\n")
	command := "amux"
	if ticket.Worker.HostID == "" {
		command = host.Command
	}
	switch {
	case command != "":
		addr := ""
		if ticket.Worker.HostID == "" && host.Addr != "" {
			addr = " --addr " + host.Addr
		}
		fmt.Fprintf(&brief, "- Start one: %s dispatch start%s --token %s --worker <worker> --title \"<a few words>\" --instructions \"<what to do, where, and what to report back>\"\n", command, addr, token)
		brief.WriteString("  It prints the helper's id. Use --instructions-file <path> for long instructions.\n")
		fmt.Fprintf(&brief, "- Read one: %s dispatch read%s --token %s --wait 10m <helper id>\n", command, addr, token)
		brief.WriteString("  It prints the helper's state and, once it finished, its final answer. Without --wait it answers at once.\n")
		if ticket.Worker.HostID != "" {
			brief.WriteString("  amux is AgentMux's command on your machine; if it is not on your PATH, it is ~/.agentmux/bin/amux.\n")
		}
	default:
		fmt.Fprintf(&brief, "- Start one: curl -s -X POST http://%s%s -H 'Content-Type: application/json' -d '{\"token\":\"%s\",\"worker\":\"<worker>\",\"title\":\"<a few words>\",\"instructions\":\"<what to do, where, and what to report back>\"}'\n", host.Addr, DispatchStartPath, token)
		brief.WriteString("  The answer's id is the helper's id.\n")
		fmt.Fprintf(&brief, "- Read one: curl -s -X POST http://%s%s -H 'Content-Type: application/json' -d '{\"token\":\"%s\",\"id\":\"<helper id>\",\"wait_seconds\":30}'\n", host.Addr, DispatchReadPath, token)
		brief.WriteString("  The answer's status is running until the helper finished; then result is its final answer.\n")
	}
	if ids := dispatchWorkerIDs(agent); len(ids) > 0 {
		brief.WriteString("Workers you may name: " + strings.Join(ids, ", ") + ".\n")
	}
	fmt.Fprintf(&brief, "Limits AgentMux enforces: at most %d helpers run at once for this Ticket, and %d are started in all; a helper cannot start helpers of its own; a helper sees only your instructions, not this conversation, and works in its own worker's directory, so say which files, branch or repository it needs; every helper still running is stopped when your turn ends, so read the results you need before you finish. ", maxRunningHelpers, maxTicketHelpers)
	brief.WriteString("Use a helper only for work that can go on in parallel with yours. The token works for this Ticket only: use it in these commands and do not write it into files.")
	return brief.String()
}

// helperInput is all a helper is told: who asked and what to do.
func helperInput(agent core.SuperAgent, ticket core.SuperAgentTicket, helper core.SuperAgentHelper) string {
	var input strings.Builder
	fmt.Fprintf(&input, "You are helping another worker of the agent %q. It is working on the Ticket %q and handed you the sub-task below. Do only this sub-task. Your final message is returned to that worker as your result, so end with what it needs: the answer, what you changed and where, and anything you could not do. You cannot hand work to other workers.\n", agent.Name, ticket.Title)
	if helper.Title != "" {
		input.WriteString("\nSUB-TASK: " + helper.Title + "\n")
	}
	input.WriteString("\n" + helper.Instructions + "\n")
	return input.String()
}

// dispatchTicket checks a call's token and returns its Ticket and agent.
func (s *Service) dispatchTicket(ctx context.Context, token string) (*core.SuperAgentTicket, *core.SuperAgent, error) {
	parsed, ok := parseDispatchToken(token)
	if !ok {
		return nil, nil, dispatchRefusal(http.StatusUnauthorized, "this is not a dispatch token")
	}
	ticket, err := s.store.GetSuperAgentTicket(ctx, parsed.ticketID)
	if err != nil {
		return nil, nil, err
	}
	if ticket == nil {
		if parsed.home != "" && parsed.home != s.dispatchHome() {
			return nil, nil, DispatchElsewhere{Home: parsed.home}
		}
		return nil, nil, dispatchRefusal(http.StatusUnauthorized, "the token names no Ticket on this machine")
	}
	if ticket.DispatchKey == "" || subtle.ConstantTimeCompare([]byte(ticket.DispatchKey), []byte(dispatchKey(token))) != 1 {
		return nil, nil, dispatchRefusal(http.StatusUnauthorized, "the token is not this Ticket's")
	}
	agent, err := s.store.GetSuperAgent(ctx, ticket.SuperAgentID)
	if err != nil {
		return nil, nil, err
	}
	if agent == nil {
		return nil, nil, dispatchRefusal(http.StatusUnauthorized, "the Ticket's agent no longer exists")
	}
	if !agent.Dispatch {
		return nil, nil, dispatchRefusal(http.StatusForbidden, "%s does not allow its workers to start helpers", agent.Name)
	}
	return ticket, agent, nil
}

// Dispatch carries out a worker's call: DispatchCallStart or DispatchCallRead.
func (s *Service) Dispatch(ctx context.Context, call string, request DispatchRequest) (DispatchResult, error) {
	switch call {
	case DispatchCallStart:
		return s.DispatchStart(ctx, request)
	case DispatchCallRead:
		return s.DispatchRead(ctx, request)
	}
	return DispatchResult{}, dispatchRefusal(http.StatusNotFound, "unknown dispatch call %q", call)
}

// DispatchStart starts a helper for the token's Ticket.
func (s *Service) DispatchStart(ctx context.Context, request DispatchRequest) (DispatchResult, error) {
	ticket, agent, err := s.dispatchTicket(ctx, request.Token)
	if err != nil {
		return DispatchResult{}, err
	}
	instructions := strings.TrimSpace(request.Instructions)
	if instructions == "" {
		return DispatchResult{}, dispatchRefusal(http.StatusBadRequest, "instructions are required")
	}
	if len([]rune(instructions)) > maxHelperInstructions {
		return DispatchResult{}, dispatchRefusal(http.StatusBadRequest, "instructions are longer than %d characters", maxHelperInstructions)
	}
	workerID := strings.TrimSpace(request.Worker)
	if workerID == "" && strings.TrimSpace(request.Runtime) != "" {
		workerID = autoWorkerID + ":" + strings.TrimSpace(request.Runtime)
	}
	if workerID == "" {
		return DispatchResult{}, dispatchRefusal(http.StatusBadRequest, "name a worker or a runtime")
	}
	if err := s.checkWorker(*agent, workerID); err != nil {
		return DispatchResult{}, dispatchRefusal(http.StatusForbidden, "%s", err.Error())
	}
	helper := core.SuperAgentHelper{
		ID: helperIDPrefix + uuid.NewString(), WorkerID: workerID,
		Title: oneLine(request.Title, maxHelperTitle), Instructions: instructions,
		Status: core.SuperAgentHelperRunning, CreatedAt: s.now().UTC(),
	}
	helper.ConversationID = "superagent-" + helper.ID

	base := s.ctx
	if base == nil {
		base = context.Background()
	}
	runCtx, cancel := context.WithTimeout(base, workerTimeout)
	run := &helperRun{cancel: cancel, done: make(chan struct{})}
	running, err := s.reserveHelper(ctx, *agent, ticket.ID, helper, run)
	if err != nil {
		cancel()
		return DispatchResult{}, err
	}
	s.dispatch.wg.Add(1)
	go s.runHelper(runCtx, *agent, ticket.ID, helper, run)
	result := helperResult(helper)
	result.Running = running
	return result, nil
}

// reserveHelper records a new helper on a running Ticket within the limits,
// and returns how many of its helpers run now. The Ticket's turn ending in
// the meantime is seen here or stops the helper (endHelpers): the check and
// the record are one step under the dispatcher's lock.
func (s *Service) reserveHelper(ctx context.Context, agent core.SuperAgent, ticketID string, helper core.SuperAgentHelper, run *helperRun) (int, error) {
	s.dispatch.mu.Lock()
	defer s.dispatch.mu.Unlock()
	s.mu.Lock()
	_, turnRunning := s.running[agent.ID+"|"+ticketID]
	s.mu.Unlock()
	if !turnRunning {
		return 0, dispatchRefusal(http.StatusConflict, "the Ticket is not running; helpers run only during its worker's turn")
	}
	if len(s.dispatch.running[ticketID]) >= maxRunningHelpers {
		return 0, dispatchRefusal(http.StatusConflict, "%d helpers already run for this Ticket, the most at once; read one before starting another", maxRunningHelpers)
	}
	var refused error
	err := s.quietlyMutateTicket(ctx, ticketID, func(ticket *core.SuperAgentTicket) bool {
		if ticket.Status != core.SuperAgentTicketRunning {
			refused = dispatchRefusal(http.StatusConflict, "the Ticket is not running")
			return false
		}
		if len(ticket.Helpers) >= maxTicketHelpers {
			refused = dispatchRefusal(http.StatusConflict, "this Ticket already started %d helpers, the most it may", maxTicketHelpers)
			return false
		}
		ticket.Helpers = append(slices.Clone(ticket.Helpers), helper)
		return true
	})
	if err == nil {
		err = refused
	}
	if err != nil {
		return 0, err
	}
	if s.dispatch.running == nil {
		s.dispatch.running = map[string]map[string]*helperRun{}
	}
	if s.dispatch.running[ticketID] == nil {
		s.dispatch.running[ticketID] = map[string]*helperRun{}
	}
	s.dispatch.running[ticketID][helper.ID] = run
	return len(s.dispatch.running[ticketID]), nil
}

// runHelper launches the helper's worker and runs its one turn, then records
// how it ended.
func (s *Service) runHelper(ctx context.Context, agent core.SuperAgent, ticketID string, helper core.SuperAgentHelper, run *helperRun) {
	defer s.dispatch.wg.Done()
	defer close(run.done)
	defer run.cancel()
	worker, err := s.assignedWorker(ctx, agent, helper.WorkerID)
	if err == nil {
		err = ctx.Err()
	}
	answer := ""
	if err == nil {
		helper.Worker = worker
		parent := core.SuperAgentTicket{ID: ticketID}
		if current, getErr := s.store.GetSuperAgentTicket(ctx, ticketID); getErr == nil && current != nil {
			parent = *current
		}
		s.recordHelper(ticketID, helper.ID, func(record *core.SuperAgentHelper) { record.Worker = worker })
		// The worker's running turn polls for this one, so it must not wait
		// for a place that turn holds.
		helperCtx := core.WithAwaitedTurn(core.WithTurnClass(ctx, core.TurnTicket))
		answer, err = s.workers.Run(helperCtx, worker, helper.ConversationID, helperInput(agent, parent, helper), RunOptions{})
	}
	s.dispatch.mu.Lock()
	reason := run.reason
	s.dispatch.mu.Unlock()
	now := s.now().UTC()
	s.recordHelper(ticketID, helper.ID, func(record *core.SuperAgentHelper) {
		record.FinishedAt = now
		switch {
		case err == nil:
			record.Status = core.SuperAgentHelperDone
			record.Result = truncateRunes(strings.TrimSpace(answer), maxHelperResult)
		case reason != "" || errors.Is(ctx.Err(), context.Canceled):
			record.Status = core.SuperAgentHelperStopped
			record.Error = firstNonEmpty(reason, "AgentMux stopped")
		default:
			record.Status = core.SuperAgentHelperFailed
			record.Error = oneLine(err.Error(), 2000)
		}
	})
	s.dispatch.mu.Lock()
	if helpers := s.dispatch.running[ticketID]; helpers[helper.ID] == run {
		delete(helpers, helper.ID)
		if len(helpers) == 0 {
			delete(s.dispatch.running, ticketID)
		}
	}
	s.dispatch.mu.Unlock()
}

// recordHelper changes one helper's record on its Ticket.
func (s *Service) recordHelper(ticketID, helperID string, change func(*core.SuperAgentHelper)) {
	ctx, cancel := context.WithTimeout(context.Background(), helperRecordTimeout)
	defer cancel()
	err := s.quietlyMutateTicket(ctx, ticketID, func(ticket *core.SuperAgentTicket) bool {
		index := slices.IndexFunc(ticket.Helpers, func(helper core.SuperAgentHelper) bool { return helper.ID == helperID })
		if index < 0 {
			return false
		}
		// Copied, so no reader of the Ticket it came from sees the change.
		ticket.Helpers = slices.Clone(ticket.Helpers)
		change(&ticket.Helpers[index])
		return true
	})
	if err != nil {
		s.log.Warn("record a ticket's helper", "ticket", ticketID, "helper", helperID, "err", err)
	}
}

// endHelpers stops a Ticket's helpers when its turn ends, for whatever
// reason. Helpers a daemon that stopped left marked running are closed too.
func (s *Service) endHelpers(ticketID string) {
	s.dispatch.mu.Lock()
	runs := s.dispatch.running[ticketID]
	delete(s.dispatch.running, ticketID)
	for _, run := range runs {
		run.reason = "the parent Ticket's turn ended"
		run.cancel()
	}
	s.dispatch.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), helperRecordTimeout)
	defer cancel()
	now := s.now().UTC()
	err := s.quietlyMutateTicket(ctx, ticketID, func(ticket *core.SuperAgentTicket) bool {
		changed := false
		ticket.Helpers = slices.Clone(ticket.Helpers)
		for index := range ticket.Helpers {
			helper := &ticket.Helpers[index]
			if _, live := runs[helper.ID]; live || helper.Status != core.SuperAgentHelperRunning {
				continue
			}
			helper.Status, helper.Error, helper.FinishedAt = core.SuperAgentHelperStopped, "AgentMux stopped while it ran", now
			changed = true
		}
		return changed
	})
	if err != nil {
		s.log.Warn("close a ticket's helpers", "ticket", ticketID, "err", err)
	}
}

// DispatchRead returns one of the token's Ticket's helpers, waiting a while
// for a running one to finish when asked to.
func (s *Service) DispatchRead(ctx context.Context, request DispatchRequest) (DispatchResult, error) {
	ticket, _, err := s.dispatchTicket(ctx, request.Token)
	if err != nil {
		return DispatchResult{}, err
	}
	id := strings.TrimSpace(request.ID)
	helper, ok := findHelper(*ticket, id)
	if !ok {
		return DispatchResult{}, dispatchRefusal(http.StatusNotFound, "this Ticket has no helper %q", id)
	}
	s.dispatch.mu.Lock()
	run := s.dispatch.running[ticket.ID][id]
	s.dispatch.mu.Unlock()
	if helper.Status == core.SuperAgentHelperRunning && run != nil && request.WaitSeconds > 0 {
		timer := time.NewTimer(min(time.Duration(request.WaitSeconds)*time.Second, maxDispatchWait))
		select {
		case <-run.done:
		case <-timer.C:
		case <-ctx.Done():
		}
		timer.Stop()
		if current, err := s.store.GetSuperAgentTicket(ctx, ticket.ID); err == nil && current != nil {
			ticket = current
			helper, _ = findHelper(*ticket, id)
		}
	}
	s.dispatch.mu.Lock()
	running := len(s.dispatch.running[ticket.ID])
	live := s.dispatch.running[ticket.ID][id] != nil
	s.dispatch.mu.Unlock()
	if helper.Status == core.SuperAgentHelperRunning && !live && run == nil {
		// Left marked running by a daemon that stopped.
		helper.Status, helper.Error = core.SuperAgentHelperStopped, "AgentMux stopped while it ran"
	}
	result := helperResult(helper)
	result.Running = running
	return result, nil
}

func findHelper(ticket core.SuperAgentTicket, id string) (core.SuperAgentHelper, bool) {
	index := slices.IndexFunc(ticket.Helpers, func(helper core.SuperAgentHelper) bool { return helper.ID == id })
	if id == "" || index < 0 {
		return core.SuperAgentHelper{}, false
	}
	return ticket.Helpers[index], true
}

func helperResult(helper core.SuperAgentHelper) DispatchResult {
	return DispatchResult{
		ID: helper.ID, Status: helper.Status, WorkerID: helper.WorkerID, Title: helper.Title,
		Result: helper.Result, Error: helper.Error, CreatedAt: helper.CreatedAt, FinishedAt: helper.FinishedAt,
	}
}

// helperLines are a Ticket's running helpers as the brain is shown them.
func helperLines(ticket core.SuperAgentTicket) []string {
	var lines []string
	for _, helper := range ticket.Helpers {
		if helper.Status != core.SuperAgentHelperRunning {
			continue
		}
		line := helper.ID + " on " + helper.WorkerID + ", running"
		if helper.Title != "" {
			line += ": " + helper.Title
		}
		lines = append(lines, line)
	}
	return lines
}

// cutHelpers shortens helpers for a list across machines.
func cutHelpers(helpers []core.SuperAgentHelper, limit int) []core.SuperAgentHelper {
	if len(helpers) == 0 {
		return nil
	}
	out := slices.Clone(helpers)
	for index := range out {
		out[index].Instructions = truncateRunes(out[index].Instructions, limit)
		out[index].Result = truncateRunes(out[index].Result, limit)
	}
	return out
}

// ForwardDispatch passes a call for a Ticket on another machine up this
// machine's hub link, and returns the answer as it came.
func (s *Service) ForwardDispatch(ctx context.Context, call, home string, body []byte) (int, []byte, error) {
	if call != DispatchCallStart && call != DispatchCallRead {
		return 0, nil, fmt.Errorf("unknown dispatch call %q", call)
	}
	host := s.dispatchHost()
	var transport http.RoundTripper
	if host.Uplink != nil {
		transport = host.Uplink.Uplink()
	}
	if transport == nil {
		return 0, nil, errors.New("the Ticket's agent is on another machine, and this machine has no hub to reach it through")
	}
	path := DispatchStartPath
	uplinkPath := DispatchUplinkStartPath
	if call == DispatchCallRead {
		path, uplinkPath = DispatchReadPath, DispatchUplinkReadPath
	}
	target := "http://hub" + uplinkPath
	if home != DispatchHomeHub {
		if home == s.dispatchHome() {
			return 0, nil, errors.New("the token names this machine, which has no such Ticket")
		}
		target = "http://hub" + WorkerRelayPrefix + url.PathEscape(home) + path
	}
	ctx, cancel := context.WithTimeout(ctx, dispatchForwardTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		return 0, nil, fmt.Errorf("reach the Ticket's machine through the hub: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	return response.StatusCode, data, err
}
