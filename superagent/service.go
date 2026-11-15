// Package superagent runs persistent routers. Sources produce Triggers, a
// deterministic gate decides who may originate work, a router Agent decides
// what each admitted Trigger means, and the host applies that decision to local
// or remote workers. Its concepts follow DeepSeek's dsh-superagent prototype:
// the host, never the model or the event text, enforces allowed lists and which
// workers and Tickets a decision may touch.
package superagent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wangning19940904/AgentMux/core"
)

const (
	passInterval      = 2 * time.Second
	maxRouterEvals    = 4
	maxRouterAttempts = 3
	maxWorkerAttempts = 3
	retryDelay        = 15 * time.Second
	// A router at high reasoning effort can think for minutes without
	// streaming anything, so the idle limit catches a dead stream, not a slow
	// model.
	routerTimeout       = 10 * time.Minute
	routerIdleTimeout   = 5 * time.Minute
	workerTimeout       = 6 * time.Hour
	pendingBatchSize    = 100
	defaultConcurrency  = 2
	maxConcurrencyLimit = 32
	maxTriggerRunes     = 60_000
)

var (
	ErrNotFound = errors.New("super agent not found")
	ErrConflict = errors.New("ticket is not in a state that allows this")
)

// conflictError is an ErrConflict that says what the conflict is.
type conflictError string

func (e conflictError) Error() string        { return string(e) }
func (e conflictError) Is(target error) bool { return target == ErrConflict }

// Store is the persistence surface of the service, implemented by store.Store.
type Store interface {
	ListSuperAgents(context.Context) ([]core.SuperAgent, error)
	GetSuperAgent(context.Context, string) (*core.SuperAgent, error)
	UpsertSuperAgent(context.Context, core.SuperAgent) error
	DeleteSuperAgent(context.Context, string) error
	InsertSuperAgentTrigger(context.Context, core.SuperAgentTrigger) (bool, error)
	UpdateSuperAgentTrigger(context.Context, core.SuperAgentTrigger) error
	ListPendingSuperAgentTriggers(context.Context, int) ([]core.SuperAgentTrigger, error)
	ListSuperAgentTriggers(context.Context, string, int) ([]core.SuperAgentTrigger, error)
	UpsertSuperAgentTicket(context.Context, core.SuperAgentTicket) error
	GetSuperAgentTicket(context.Context, string) (*core.SuperAgentTicket, error)
	ListSuperAgentTickets(context.Context, string, int) ([]core.SuperAgentTicket, error)
	ListSuperAgentThreadTickets(context.Context, string, string) ([]core.SuperAgentTicket, error)
	ListSuperAgentTicketsByStatus(context.Context, ...string) ([]core.SuperAgentTicket, error)
	UpsertSuperAgentTimer(context.Context, core.SuperAgentTimer) error
	GetSuperAgentTimer(context.Context, string) (*core.SuperAgentTimer, error)
	ListDueSuperAgentTimers(context.Context, time.Time, int) ([]core.SuperAgentTimer, error)
	ListActiveSuperAgentTimers(context.Context, string) ([]core.SuperAgentTimer, error)
	ListFinishedSuperAgentTimers(context.Context, string, time.Time, int) ([]core.SuperAgentTimer, error)
	CountSuperAgentTimersFired(context.Context, string, time.Time) (int, error)
	GetSuperAgentSourceState(context.Context, string) (string, error)
	SetSuperAgentSourceState(context.Context, string, string, string) error
	GetAgentInstance(context.Context, string) (*core.AgentInstance, error)
	UpsertAgentInstance(context.Context, *core.AgentInstance) error
	DeleteAgentInstance(context.Context, string) error
	ListAgentInstances(context.Context) ([]core.AgentInstance, error)
	ListChannels(context.Context) ([]core.Channel, error)
	// Plans and remembered approvals are kept as settings (plans.go).
	GetSetting(ctx context.Context, key string) (string, bool, error)
	SetSetting(ctx context.Context, key, value string) error
}

// Options holds the service's collaborators.
type Options struct {
	Store   Store
	Invoker core.Invoker
	Workers WorkerRunner
	Replies ReplySender
	Mail    MailClient
	GitHub  GitHubInbox
	Docs    DocumentSubscriber
	// Directory names the Feishu groups a Super Agent listens in and the
	// people there, so its router can address them. Scopes checks that a
	// Feishu bot receives the messages a Source expects.
	Directory Directory
	Scopes    FeishuScopeChecker
	Log       *slog.Logger
	Now       func() time.Time
	// ChannelHasAgent reports whether a channel has its own Agent to take the
	// messages a Super Agent does not claim.
	ChannelHasAgent func(context.Context, string) bool
	// RuntimeAvailable reports whether an agent framework can run on this machine.
	RuntimeAvailable func(string) bool
	// DetectTools finds what workers on this machine can use besides code,
	// such as a browser; DetectWorkerTools looks at the real machine.
	DetectTools func(runtimes []string) WorkerTools
	// MCP lists the MCP servers a router agent may be given.
	MCP core.MCPRegistry
	// StateDir holds directories the service creates. Empty means ~/.agentmux.
	StateDir string
	// Health is this machine's framework health; nil leaves every framework
	// here unknown, which holds nothing back.
	Health LocalHealth
	// MachineUIDs returns a machine's UID by host id ("" is this machine),
	// or "" when unknown. Workspace claims use it so one computer reached
	// several ways counts once; nil keys claims by host id.
	MachineUIDs func(hostID string) string
}

// Service owns intake, routing and execution for every Super Agent.
type Service struct {
	store   Store
	invoker core.Invoker
	workers WorkerRunner
	replies ReplySender
	log     *slog.Logger
	now     func() time.Time
	wake    chan struct{}

	mu     sync.Mutex
	asksMu sync.Mutex
	// plansMu makes each change to an agent's plans and trust one step.
	plansMu sync.Mutex
	// timersMu makes each change to a timer one step. It is taken after mu,
	// never before it.
	timersMu       sync.Mutex
	running        map[string]context.CancelFunc
	stopping       map[string]bool
	evaluating     map[string]bool
	evaluations    uint64 // finished router evaluations
	routerAttempts map[string]int
	routerFeedback map[string]string
	notBefore      map[string]time.Time
	health         map[string]SourceHealth
	places         map[string]feishuPlaces
	scopeChecks    map[string]scopeState
	activities     map[string]activity
	// turnStarts says when each running turn began, and suspects when each
	// supervision condition was first seen holding; see supervision.go.
	turnStarts map[string]time.Time
	suspects   map[string]time.Time

	wg     sync.WaitGroup
	cancel context.CancelFunc
	ctx    context.Context
	once   sync.Once

	claims          claimIndex
	mail            MailClient
	github          GitHubInbox
	docs            DocumentSubscriber
	documents       map[string][]documentRef
	channelHasAgent func(context.Context, string) bool

	runtimeAvailable func(string) bool
	// detectTools finds what this machine's workers can use besides code.
	detectTools func(runtimes []string) WorkerTools
	mcp         core.MCPRegistry
	state       string
	directory   Directory
	scopes      FeishuScopeChecker
	mailbox     Mailbox
	// healthRecords and placement decide where new Tickets run; see
	// placement.go.
	healthRecords LocalHealth
	placement     placementCache
	machineUIDs   func(hostID string) string
}

// SourceHealth is the latest intake outcome of one Source. A warning is a
// standing problem that loses some events without failing any.
type SourceHealth struct {
	LastEventAt time.Time `json:"last_event_at,omitzero"`
	LastError   string    `json:"last_error,omitempty"`
	LastErrorAt time.Time `json:"last_error_at,omitzero"`
	Warning     string    `json:"warning,omitempty"`
}

// Activity is the Console's view of one Super Agent.
type Activity struct {
	Tickets  []core.SuperAgentTicket       `json:"tickets"`
	Triggers []core.SuperAgentTrigger      `json:"triggers"`
	Workers  []core.SuperAgentWorkerStatus `json:"workers"`
	// Machines are the other machines the agent may launch workers on.
	Machines []core.SuperAgentMachineStatus `json:"machines"`
	Sources  map[string]SourceHealth        `json:"sources"`
	// Timers are the wake-ups the agent set for itself: those still set, then
	// those that fired or were cancelled lately.
	Timers       []core.SuperAgentTimer `json:"timers"`
	TimerWarning string                 `json:"timer_warning,omitempty"`
	// RuntimeHealth is the health of each framework on the roster, per
	// machine.
	RuntimeHealth []core.SuperAgentRuntimeHealth `json:"runtime_health,omitempty"`
	// WorkspaceClaims are the directories Tickets hold now on the machines
	// this agent's workers use.
	WorkspaceClaims []WorkspaceClaim `json:"workspace_claims,omitempty"`
	// CostTiers are the roster entries' cost tiers by roster id; Plans the
	// agent's plans, newest first; Trust its remembered approvals.
	CostTiers map[string]string      `json:"cost_tiers,omitempty"`
	Plans     []core.SuperAgentPlan  `json:"plans,omitempty"`
	Trust     []core.SuperAgentTrust `json:"trust,omitempty"`
}

func New(options Options) *Service {
	log := options.Log
	if log == nil {
		log = slog.Default()
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		store: options.Store, invoker: options.Invoker, workers: options.Workers, replies: options.Replies,
		log: log, now: now, wake: make(chan struct{}, 1),
		running: map[string]context.CancelFunc{}, stopping: map[string]bool{}, evaluating: map[string]bool{},
		routerAttempts: map[string]int{}, routerFeedback: map[string]string{}, notBefore: map[string]time.Time{}, health: map[string]SourceHealth{},
		places: map[string]feishuPlaces{}, scopeChecks: map[string]scopeState{}, activities: map[string]activity{},
		turnStarts: map[string]time.Time{}, suspects: map[string]time.Time{},
		mail: options.Mail, github: options.GitHub, docs: options.Docs, documents: map[string][]documentRef{}, channelHasAgent: options.ChannelHasAgent,
		runtimeAvailable: options.RuntimeAvailable, detectTools: options.DetectTools, mcp: options.MCP, state: options.StateDir,
		directory: options.Directory, scopes: options.Scopes, healthRecords: options.Health,
		machineUIDs: options.MachineUIDs,
	}
}

// Start recovers interrupted work and begins routing. A Ticket that was running
// when the daemon stopped resumes on the same conversation with a recovery note.
func (s *Service) Start(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	s.ctx, s.cancel = ctx, cancel
	interrupted, err := s.store.ListSuperAgentTicketsByStatus(ctx, core.SuperAgentTicketRunning)
	if err != nil {
		cancel()
		return fmt.Errorf("recover super agent tickets: %w", err)
	}
	for _, ticket := range interrupted {
		ticket.Status = core.SuperAgentTicketQueued
		ticket.Inputs = append([]string{recoveryNote}, ticket.Inputs...)
		ticket.UpdatedAt = s.now().UTC()
		closeSignals(&ticket, ticket.UpdatedAt)
		if err := s.store.UpsertSuperAgentTicket(ctx, ticket); err != nil {
			cancel()
			return fmt.Errorf("recover super agent ticket %s: %w", ticket.ID, err)
		}
		s.recordTicket(ctx, ticket)
	}
	if created, err := s.UnifyAgents(ctx); err != nil {
		// Chats do not depend on the agent records: the channel engine still
		// reaches every brain. The next start tries again.
		s.log.Error("give agents their records", "err", err, "created", created)
	} else if created > 0 {
		s.log.Info("gave agents their records", "created", created)
	}
	s.adoptRouters(ctx)
	if err := s.refreshClaims(ctx); err != nil {
		cancel()
		return fmt.Errorf("load super agent sources: %w", err)
	}
	s.startPollers(ctx)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(passInterval)
		defer ticker.Stop()
		for {
			s.Pass(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-s.wake:
			}
		}
	}()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runSupervision(ctx)
	}()
	return nil
}

// Stop cancels running turns and waits for them. Their Tickets stay running in
// the store, so the next Start resumes them.
func (s *Service) Stop() {
	s.once.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		s.wg.Wait()
	})
}

func (s *Service) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Pass fires due timers, evaluates pending Triggers and starts queued
// Tickets once.
func (s *Service) Pass(ctx context.Context) {
	if err := s.fireDueTimers(ctx); err != nil && ctx.Err() == nil {
		s.log.Error("super agent timer pass failed", "err", err)
	}
	if err := s.evaluatePending(ctx); err != nil && ctx.Err() == nil {
		s.log.Error("super agent evaluation pass failed", "err", err)
	}
	if err := s.startQueued(ctx); err != nil && ctx.Err() == nil {
		s.log.Error("super agent execution pass failed", "err", err)
	}
}

// Ingest applies the gate to one event from a Source and stores it once. An
// event that addresses nobody and belongs to no Ticket's thread is dropped
// unrecorded: it is ordinary traffic in a channel, not a request.
func (s *Service) Ingest(ctx context.Context, agent core.SuperAgent, source core.SuperAgentSource, trigger core.SuperAgentTrigger) (bool, error) {
	if !agent.Enabled || !source.Enabled {
		return false, nil
	}
	now := s.now().UTC()
	s.recordEvent(source.ID, now)
	allowed := actorAllowed(source, trigger.Actor)
	if !trigger.Addressed {
		if !allowed {
			return false, nil
		}
		tickets, err := s.store.ListSuperAgentThreadTickets(ctx, agent.ID, trigger.ThreadKey)
		if err != nil {
			return false, err
		}
		if len(tickets) == 0 {
			return false, nil
		}
	}
	trigger.ID = "sat_" + uuid.NewString()
	trigger.SuperAgentID, trigger.SourceID, trigger.SourceKind = agent.ID, source.ID, source.Kind
	trigger.Text = truncateRunes(trigger.Text, maxTriggerRunes)
	trigger.ReceivedAt, trigger.UpdatedAt = now, now
	trigger.Disposition = core.SuperAgentDispositionPending
	if !allowed {
		trigger.Disposition = core.SuperAgentDispositionFiltered
		trigger.Reason = "sender is not on this source's allowed list"
	}
	inserted, err := s.store.InsertSuperAgentTrigger(ctx, trigger)
	if err != nil {
		return false, err
	}
	if inserted && allowed {
		s.notify()
	}
	return inserted, nil
}

// actorAllowed is fail closed: an empty list admits nobody. "*" must be written
// explicitly by the operator to open a Source to everyone who can reach it.
func actorAllowed(source core.SuperAgentSource, actor string) bool {
	actor = strings.TrimSpace(actor)
	for _, candidate := range source.AllowedActors {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return actor != ""
		}
		if actor != "" && strings.EqualFold(candidate, actor) {
			return true
		}
	}
	return false
}

func (s *Service) evaluatePending(ctx context.Context) error {
	// An evaluation that finishes after the list is read leaves its Trigger in
	// the list, still pending. Nothing starts from a list that old: the
	// finished evaluation wakes another pass.
	s.mu.Lock()
	finished := s.evaluations
	s.mu.Unlock()
	pending, err := s.store.ListPendingSuperAgentTriggers(ctx, pendingBatchSize)
	if err != nil {
		return err
	}
	// Triggers on one thread are evaluated in order, one at a time, so a
	// follow-up always sees the Ticket its predecessor created.
	blocked := map[string]bool{}
	for _, trigger := range pending {
		key := trigger.SuperAgentID + "|" + trigger.ThreadKey
		if blocked[key] {
			continue
		}
		blocked[key] = true
		s.mu.Lock()
		busy := s.evaluating[key] || s.evaluations != finished || len(s.evaluating) >= maxRouterEvals || s.now().Before(s.notBefore[trigger.ID])
		if !busy {
			s.evaluating[key] = true
		}
		s.mu.Unlock()
		if busy {
			continue
		}
		s.wg.Add(1)
		go func(trigger core.SuperAgentTrigger) {
			defer s.wg.Done()
			defer func() {
				s.mu.Lock()
				delete(s.evaluating, key)
				s.evaluations++
				s.mu.Unlock()
				s.notify()
			}()
			s.evaluate(ctx, trigger)
		}(trigger)
	}
	return nil
}

func (s *Service) evaluate(ctx context.Context, trigger core.SuperAgentTrigger) {
	agent, err := s.store.GetSuperAgent(ctx, trigger.SuperAgentID)
	if err != nil {
		s.log.Error("load super agent", "id", trigger.SuperAgentID, "err", err)
		return
	}
	if agent == nil {
		s.finishTrigger(ctx, trigger, core.SuperAgentDispositionFailed, "super agent was deleted", "")
		return
	}
	if !agent.Enabled {
		s.finishTrigger(ctx, trigger, core.SuperAgentDispositionFiltered, "super agent is disabled", "")
		return
	}
	*agent = s.withWorkerRuntimes(ctx, *agent)
	tickets, err := s.store.ListSuperAgentTickets(ctx, agent.ID, ticketListLimit)
	if err != nil {
		s.log.Error("load super agent tickets", "id", agent.ID, "err", err)
		return
	}
	view := s.routerView(ctx, *agent, trigger, tickets)
	s.mu.Lock()
	view.Feedback = s.routerFeedback[trigger.ID]
	s.mu.Unlock()
	decision, err := s.route(ctx, *agent, trigger, view)
	if err == nil {
		// Every action is checked before any is carried out, so a mistake
		// in one never leaves the others half done.
		if err = s.preflight(ctx, *agent, trigger, decision); err != nil {
			err = decisionError{err}
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		s.routerFailed(ctx, *agent, trigger, threadTickets(tickets, trigger.ThreadKey), err)
		return
	}
	s.mu.Lock()
	delete(s.routerAttempts, trigger.ID)
	delete(s.routerFeedback, trigger.ID)
	delete(s.notBefore, trigger.ID)
	s.mu.Unlock()
	s.apply(ctx, *agent, trigger, decision)
}

func threadTickets(tickets []core.SuperAgentTicket, threadKey string) []core.SuperAgentTicket {
	var out []core.SuperAgentTicket
	for _, ticket := range tickets {
		if ticket.ThreadKey == threadKey {
			out = append(out, ticket)
		}
	}
	slices.SortStableFunc(out, func(a, b core.SuperAgentTicket) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return out
}

// routerFailed retries a router failure, telling the router on the next try
// what went wrong, since nothing it asked for happened. Once retries run out,
// a message on a thread with a live Ticket still reaches that Ticket, and a
// finished turn is still reported in the worker's own words, so a router
// outage never strands a conversation that is already authorized.
func (s *Service) routerFailed(ctx context.Context, agent core.SuperAgent, trigger core.SuperAgentTrigger, tickets []core.SuperAgentTicket, cause error) {
	s.mu.Lock()
	s.routerAttempts[trigger.ID]++
	attempts := s.routerAttempts[trigger.ID]
	if attempts < maxRouterAttempts {
		s.notBefore[trigger.ID] = s.now().Add(retryDelay)
		s.routerFeedback[trigger.ID] = cause.Error()
		s.mu.Unlock()
		s.log.Warn("super agent router failed; retrying", "trigger", trigger.ID, "attempt", attempts, "err", cause)
		trigger.Reason = fmt.Sprintf("router attempt %d failed: %v", attempts, cause)
		trigger.UpdatedAt = s.now().UTC()
		if err := s.store.UpdateSuperAgentTrigger(ctx, trigger); err != nil && ctx.Err() == nil {
			s.log.Error("update super agent trigger", "id", trigger.ID, "err", err)
		}
		return
	}
	delete(s.routerAttempts, trigger.ID)
	delete(s.routerFeedback, trigger.ID)
	delete(s.notBefore, trigger.ID)
	s.mu.Unlock()
	if trigger.Kind == core.SuperAgentTriggerTimer || trigger.Kind == core.SuperAgentTriggerSupervision || trigger.Kind == core.SuperAgentTriggerPlan {
		// Nobody is waiting for an answer to a follow-up or a supervision
		// signal, and either is for the router, not for a worker or a person.
		s.finishTrigger(ctx, trigger, core.SuperAgentDispositionFailed, "router failed: "+cause.Error(), trigger.TicketID)
		return
	}
	if trigger.Kind == core.SuperAgentTriggerTicketUpdate {
		s.finishTrigger(ctx, trigger, core.SuperAgentDispositionFailed, "router failed: "+cause.Error()+"; sent the worker's report as it was", trigger.TicketID)
		s.relayReport(ctx, agent, trigger)
		return
	}
	if live := latestLiveTicket(tickets); live != nil {
		if err := s.attach(ctx, agent, live.ID, trigger, ""); err == nil {
			s.finishTrigger(ctx, trigger, core.SuperAgentDispositionFollowUp, "router unavailable; delivered to the thread's ticket", live.ID)
			return
		}
	}
	s.finishTrigger(ctx, trigger, core.SuperAgentDispositionFailed, "router failed: "+cause.Error(), "")
	message := "I could not process this request because my router is unavailable. Please try again later."
	if errors.As(cause, new(decisionError)) {
		message = "I could not process this request because my router chose something I cannot carry out. Please try again, or ask the operator to check this Super Agent."
	}
	s.sendReply(ctx, agent, trigger.SourceID, trigger.Reply, message)
}

func latestLiveTicket(tickets []core.SuperAgentTicket) *core.SuperAgentTicket {
	for index := len(tickets) - 1; index >= 0; index-- {
		if tickets[index].Status != core.SuperAgentTicketStopped {
			return &tickets[index]
		}
	}
	return nil
}

// preflight checks every action against the host's own records before any is
// carried out: a worker must be on the roster or an allowed auto worker, a
// Ticket must belong to this Super Agent, and a post must go somewhere this
// Super Agent already talks. A ref names a Ticket accepted earlier in the
// same decision.
func (s *Service) preflight(ctx context.Context, agent core.SuperAgent, trigger core.SuperAgentTrigger, decision routerDecision) error {
	refs := map[string]string{}
	reviewed := map[string]bool{}
	// Directories held now, read when an accept first needs them.
	claims := &claimCheck{s: s, agent: agent}
	// The thread's and the agent's follow-ups, read when an action first
	// needs them.
	var timers *timerLedger
	ledger := func() (*timerLedger, error) {
		if timers != nil {
			return timers, nil
		}
		var err error
		timers, err = s.timerLedger(ctx, agent.ID, trigger.ThreadKey)
		return timers, err
	}
	resolve := func(id string) (string, error) {
		id = strings.TrimSpace(id)
		if _, ok := refs[id]; ok {
			return "", nil
		}
		ticket, err := s.agentTicket(ctx, agent.ID, id)
		if err != nil {
			return "", err
		}
		return ticket.Status, nil
	}
	for index, action := range decision.Actions {
		switch action.Action {
		case actionAccept:
			if err := s.checkWorker(agent, action.WorkerID); err != nil {
				return err
			}
			if err := checkCriteria(action.DoneWhen, action.ExpectMinutes); err != nil {
				return err
			}
			// Where it runs is decided here, so a place that cannot take it
			// is refused before anything is carried out. decision.Actions
			// shares its array with the caller's, which apply then reads.
			placed, err := s.place(ctx, agent, action, func(place workerPlace) string {
				return claims.conflict(ctx, action, place)
			})
			if err != nil {
				return err
			}
			decision.Actions[index].placed = &placed
			claims.claim(ctx, action, placed.place)
			if !action.approved {
				requester, _ := s.requester(ctx, agent.ID, trigger)
				if problem := s.approvalProblem(ctx, agent, requester, s.placeTier(ctx, agent, placed.place)); problem != "" {
					return errors.New(problem)
				}
			}
			for _, id := range append(slices.Clone(action.DependsOn), action.BaseTicket) {
				if strings.TrimSpace(id) == "" {
					continue
				}
				status, err := resolve(id)
				if err != nil {
					return err
				}
				if status == core.SuperAgentTicketStopped {
					return fmt.Errorf("ticket %s was stopped, so nothing can depend on it", id)
				}
			}
			if ref := strings.TrimSpace(action.Ref); ref != "" {
				refs[ref] = ref
			}
		case actionAttach, actionStop, actionLink:
			status, err := resolve(action.TicketID)
			if err != nil {
				return err
			}
			if action.Action == actionAttach && status == core.SuperAgentTicketStopped {
				return fmt.Errorf("ticket %s was stopped and cannot take input; accept new work instead", action.TicketID)
			}
		case actionPost:
			if _, ok := refs[strings.TrimSpace(action.To)]; ok {
				continue
			}
			if _, err := s.resolveDestination(ctx, agent, action.To, nil); err != nil {
				return err
			}
		case actionFollowUp:
			if _, err := followUpDue(action, s.now()); err != nil {
				return err
			}
			if id := strings.TrimSpace(action.TicketID); id != "" {
				status, err := resolve(id)
				if err != nil {
					return err
				}
				if status == core.SuperAgentTicketStopped {
					return fmt.Errorf("ticket %s was stopped, so there is nothing to follow up on", id)
				}
			}
			counts, err := ledger()
			if err != nil {
				return err
			}
			if err := counts.add(); err != nil {
				return err
			}
		case actionCancelTimer:
			timer, err := s.threadTimer(ctx, agent.ID, trigger.ThreadKey, action.TimerID)
			if err != nil {
				return err
			}
			counts, err := ledger()
			if err != nil {
				return err
			}
			counts.cancel(timer.ID)
		case actionReview:
			if err := s.preflightReview(ctx, agent, trigger, action, reviewed); err != nil {
				return err
			}
		case actionPlan:
			items, tier, err := s.preflightPlan(ctx, agent, action, claims, resolve)
			if err != nil {
				return err
			}
			decision.Actions[index].planItems, decision.Actions[index].planTier = items, tier
		}
	}
	return nil
}

// checkWorker is assignedWorker without launching anything.
func (s *Service) checkWorker(agent core.SuperAgent, workerID string) error {
	if runtimeID, pathID, ok := parseAutoWorkerID(workerID); ok {
		if pathID != "" {
			return checkMachineWorker(agent, workerID, runtimeID, pathID)
		}
		if !agent.AutoWorkers {
			return errors.New("this Super Agent cannot launch workers; choose one from the roster")
		}
		if !slices.Contains(agent.WorkerRuntimes, runtimeID) {
			return fmt.Errorf("worker %q is not on the roster", workerID)
		}
		return nil
	}
	if !slices.ContainsFunc(agent.Workers, func(worker core.SuperAgentWorker) bool { return worker.ID == workerID }) {
		return fmt.Errorf("worker %q is not on the roster", workerID)
	}
	return nil
}

// apply carries out a checked decision in order, sends the reply to the
// event's thread, then posts elsewhere. The Trigger records the most
// significant thing that happened. A failure past preflight is rare, such as a Ticket stopped in the
// meantime; it is recorded and the rest still runs.
func (s *Service) apply(ctx context.Context, agent core.SuperAgent, trigger core.SuperAgentTrigger, decision routerDecision) {
	refs := map[string]string{}
	resolve := func(id string) string {
		id = strings.TrimSpace(id)
		if ticketID, ok := refs[id]; ok {
			return ticketID
		}
		return id
	}
	var accepted, attached, stopped, posted, reviewed, planned bool
	var ticketIDs, problems, reasons, notices []string
	if decision.Reason != "" {
		reasons = append(reasons, decision.Reason)
	}
	reply := strings.TrimSpace(decision.Reply)
	replied := false
	sendReply := func() {
		if replied {
			return
		}
		replied = true
		if err := s.sendReply(ctx, agent, trigger.SourceID, trigger.Reply, reply); err != nil && trigger.Kind == core.SuperAgentTriggerTicketUpdate {
			_ = s.mutateTicket(ctx, trigger.TicketID, func(ticket *core.SuperAgentTicket) error {
				ticket.ReplyError = err.Error()
				return nil
			})
		}
	}
	for _, action := range decision.Actions {
		if action.Reason != "" {
			reasons = append(reasons, action.Reason)
		}
		var err error
		switch action.Action {
		case actionAccept:
			var dependsOn []string
			for _, id := range action.DependsOn {
				if id = resolve(id); id != "" {
					dependsOn = append(dependsOn, id)
				}
			}
			action.DependsOn = dependsOn
			action.BaseTicket = resolve(action.BaseTicket)
			var id string
			if id, err = s.accept(ctx, agent, trigger, action); err == nil {
				accepted = true
				ticketIDs = append(ticketIDs, id)
				if action.placed != nil && action.placed.moved != nil {
					moved := action.placed.moved
					reasons = append(reasons, fmt.Sprintf("moved %s from %s to %s (%s): %s", id, moved.RequestedWorkerID, moved.WorkerID, moved.Machine, moved.Reason))
				}
				if ref := strings.TrimSpace(action.Ref); ref != "" {
					refs[ref] = id
				}
			}
		case actionAttach:
			id := resolve(action.TicketID)
			if err = s.attach(ctx, agent, id, trigger, action.Instructions); err == nil {
				attached = true
				ticketIDs = append(ticketIDs, id)
			}
		case actionLink:
			id := resolve(action.TicketID)
			if err = s.link(ctx, agent, id, trigger); err == nil {
				ticketIDs = append(ticketIDs, id)
			}
		case actionStop:
			id := resolve(action.TicketID)
			if err = s.StopTicket(ctx, id); err == nil || errors.Is(err, ErrConflict) {
				err = nil
				stopped = true
				ticketIDs = append(ticketIDs, id)
			}
		case actionPost:
			sendReply()
			var target destination
			if target, err = s.resolveDestination(ctx, agent, resolve(action.To), refs); err == nil {
				if err = s.sendReply(ctx, agent, target.sourceID, target.reply, action.Text); err == nil {
					posted = true
					s.notePost(agent.ID, trigger.ThreadKey, trigger.SourceID, trigger.Reply, target, action.Text)
				}
			}
		case actionFollowUp:
			var timer core.SuperAgentTimer
			if timer, err = s.setFollowUp(ctx, agent, trigger, action, resolve(action.TicketID)); err == nil {
				reasons = append(reasons, fmt.Sprintf("set follow-up %s for %s", timer.ID, timer.DueAt.Format(time.RFC3339)))
			}
		case actionCancelTimer:
			// preflight checked that the timer is this thread's. One that
			// fired in the meantime is left alone.
			var cancelled bool
			if cancelled, err = s.cancelTimer(ctx, strings.TrimSpace(action.TimerID)); cancelled {
				reasons = append(reasons, "cancelled follow-up "+strings.TrimSpace(action.TimerID))
			}
		case actionPlan:
			var plan core.SuperAgentPlan
			var notice string
			if plan, notice, err = s.proposePlan(ctx, agent, trigger, action); err == nil {
				planned = true
				notices = append(notices, notice)
				if plan.Status == core.SuperAgentPlanApproved {
					accepted = true
					ticketIDs = append(ticketIDs, plan.TicketIDs...)
					reasons = append(reasons, "started plan "+plan.ID+" (remembered approval)")
				} else {
					reasons = append(reasons, "proposed plan "+plan.ID+" for approval")
				}
			}
		case actionReview:
			id := strings.TrimSpace(action.TicketID)
			var outcome reviewOutcome
			if outcome, err = s.review(ctx, trigger, id, action); err == nil {
				reviewed = true
				attached = attached || outcome.rework
				ticketIDs = append(ticketIDs, id)
				if outcome.notice != "" {
					notices = append(notices, outcome.notice)
				}
				if outcome.refused != "" {
					reasons = append(reasons, "refused a rework of "+id+": "+outcome.refused)
				} else {
					reasons = append(reasons, "reviewed "+id+": "+reviewVerdict(action))
				}
			}
		}
		if err != nil {
			s.log.Error("apply router action", "trigger", trigger.ID, "action", action.Action, "err", err)
			problems = append(problems, action.Action+": "+err.Error())
		}
	}
	sendReply()
	// What AgentMux itself must tell the thread, such as a ticket that needs
	// a decision, goes after the brain's reply, in a message of its own.
	for _, notice := range notices {
		if err := s.sendReply(ctx, agent, trigger.SourceID, trigger.Reply, notice); err != nil {
			problems = append(problems, "notice: "+err.Error())
		}
	}
	disposition := core.SuperAgentDispositionIgnored
	switch {
	case accepted:
		disposition = core.SuperAgentDispositionAccepted
	case attached:
		disposition = core.SuperAgentDispositionFollowUp
	case stopped:
		disposition = core.SuperAgentDispositionStopped
	case reply != "" || posted || reviewed || planned:
		disposition = core.SuperAgentDispositionAnswered
	case len(problems) > 0:
		disposition = core.SuperAgentDispositionFailed
	}
	ticketID := trigger.TicketID
	if len(ticketIDs) > 0 {
		ticketID = ticketIDs[0]
	}
	reason := strings.Join(reasons, "; ")
	if len(problems) > 0 {
		reason = strings.TrimPrefix(reason+"; failed: "+strings.Join(problems, "; "), "; ")
	}
	s.finishTrigger(ctx, trigger, disposition, reason, ticketID)
}

func (s *Service) accept(ctx context.Context, agent core.SuperAgent, trigger core.SuperAgentTrigger, action routerAction) (string, error) {
	var moved *core.SuperAgentTicketPlacement
	if action.placed != nil && action.placed.moved != nil {
		moved = action.placed.moved
		action.WorkerID = moved.WorkerID
	}
	worker, workspaceMode, err := s.launchAssigned(ctx, agent, action.WorkerID)
	if err != nil {
		return "", err
	}
	var workspace *core.SuperAgentTicketWorkspace
	if place, err := s.placeOf(ctx, agent, action.WorkerID); err == nil {
		if found, ok := s.workspaceOf(ctx, agent, place); ok {
			if workspaceMode != "" {
				found.Isolated = strings.EqualFold(workspaceMode, "worktree")
			}
			workspace = &found
		}
	}
	base := strings.TrimSpace(action.BaseTicket)
	if base == "" && len(action.DependsOn) == 1 {
		base = action.DependsOn[0]
	}
	background := s.ticketBackground(ctx, agent.ID, action.DependsOn, base)
	if line := toolNotesLine(s.workerToolNotes(ctx, agent, action.WorkerID)); line != "" {
		background = append(background, line)
	}
	now := s.now().UTC()
	id := "tkt_" + uuid.NewString()
	title := strings.TrimSpace(action.Title)
	if title == "" {
		title = firstNonEmpty(trigger.Title, truncateRunes(trigger.Text, 80))
	}
	ticket := core.SuperAgentTicket{
		ID: id, SuperAgentID: agent.ID, SourceID: trigger.SourceID, SourceKind: trigger.SourceKind,
		ThreadKey: trigger.ThreadKey, TriggerID: trigger.ID, Title: title, Actor: trigger.Actor,
		Worker: worker, ConversationID: "superagent-" + id, Status: core.SuperAgentTicketQueued,
		Inputs:    []string{workerAssignment(agent, trigger, action, background)},
		Reply:     trigger.Reply,
		DependsOn: action.DependsOn, BaseTicket: base,
		Instructions: strings.TrimSpace(action.Instructions),
		DoneWhen:     cleanCriteria(action.DoneWhen), ExpectMinutes: action.ExpectMinutes,
		Placement: moved, Workspace: workspace,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.openTicketFolder(agent.ID, id); err != nil {
		return "", err
	}
	if err := s.store.UpsertSuperAgentTicket(ctx, ticket); err != nil {
		return "", err
	}
	s.recordTicket(ctx, ticket)
	s.notify()
	return id, nil
}

func (s *Service) assignedWorker(ctx context.Context, agent core.SuperAgent, workerID string) (core.SuperAgentWorker, error) {
	worker, _, err := s.launchAssigned(ctx, agent, workerID)
	return worker, err
}

// launchAssigned is assignedWorker that also says, for a worker launched on
// another machine, how that machine isolates its conversations.
func (s *Service) launchAssigned(ctx context.Context, agent core.SuperAgent, workerID string) (core.SuperAgentWorker, string, error) {
	if runtimeID, pathID, ok := parseAutoWorkerID(workerID); ok {
		if pathID != "" {
			return s.launchMachineWorkerMode(ctx, agent, runtimeID, pathID)
		}
		worker, err := s.launchAutoWorker(ctx, agent, runtimeID)
		return worker, "", err
	}
	worker, err := s.rosterWorker(agent, workerID)
	return worker, "", err
}

func (s *Service) rosterWorker(agent core.SuperAgent, workerID string) (core.SuperAgentWorker, error) {
	index := slices.IndexFunc(agent.Workers, func(worker core.SuperAgentWorker) bool { return worker.ID == workerID })
	if index < 0 {
		return core.SuperAgentWorker{}, fmt.Errorf("router chose worker %q, which is not on the roster", workerID)
	}
	return agent.Workers[index], nil
}

// attach delivers a Trigger to one of the Super Agent's Tickets, on any
// thread. A running Ticket gets it in its current turn when its worker can
// take input mid-turn, and otherwise runs again after that turn with it; an
// ended Ticket resumes on the same conversation. A message from the Ticket's
// own thread moves its reply there; one from another thread links that thread,
// so the router can tell it about the Ticket later.
func (s *Service) attach(ctx context.Context, agent core.SuperAgent, ticketID string, trigger core.SuperAgentTrigger, note string) error {
	current, err := s.agentTicket(ctx, agent.ID, ticketID)
	if err != nil {
		return err
	}
	where := ""
	if current.ThreadKey != trigger.ThreadKey && trigger.Kind == "" {
		where = describeThread(trigger.ThreadKey, trigger.DedupeKey, nil)
	}
	input := workerFollowUp(trigger, note, where)
	steered := false
	if current.Status == core.SuperAgentTicketRunning {
		if err := s.steer(ctx, *current, input); err == nil {
			steered = true
			s.appendTicketLog(*current, ticketInputsFile, "(delivered during the running turn)\n\n"+input)
		} else if !errors.Is(err, core.ErrSteerUnsupported) {
			s.log.Debug("super agent could not steer a running ticket; it will get the message after its turn", "ticket", ticketID, "err", err)
		}
	}
	return s.mutateTicket(ctx, ticketID, func(ticket *core.SuperAgentTicket) error {
		if ticket.Status == core.SuperAgentTicketStopped {
			return fmt.Errorf("%w: ticket %s was stopped", ErrConflict, ticket.ID)
		}
		if trigger.Kind == "" && ticket.ThreadKey == trigger.ThreadKey {
			ticket.Reply = trigger.Reply
		} else {
			addLink(ticket, trigger)
		}
		if steered && ticket.Status == core.SuperAgentTicketRunning {
			return nil
		}
		ticket.Inputs = append(ticket.Inputs, input)
		if ticket.Status != core.SuperAgentTicketRunning {
			ticket.Status = core.SuperAgentTicketQueued
			ticket.Attempts = 0
			ticket.Error = ""
		}
		return nil
	})
}

// link records that a thread other than the Ticket's own cares about it,
// without sending its worker anything.
func (s *Service) link(ctx context.Context, agent core.SuperAgent, ticketID string, trigger core.SuperAgentTrigger) error {
	if _, err := s.agentTicket(ctx, agent.ID, ticketID); err != nil {
		return err
	}
	return s.mutateTicket(ctx, ticketID, func(ticket *core.SuperAgentTicket) error {
		addLink(ticket, trigger)
		return nil
	})
}

// addLink links an event's thread to a Ticket, keeping the latest reply
// address. A ticket update, or a message on the Ticket's own thread, adds no
// link.
func addLink(ticket *core.SuperAgentTicket, trigger core.SuperAgentTrigger) {
	if trigger.Kind != "" || ticket.ThreadKey == trigger.ThreadKey || len(trigger.Reply) == 0 {
		return
	}
	link := core.SuperAgentTicketLink{ThreadKey: trigger.ThreadKey, SourceID: trigger.SourceID, Reply: trigger.Reply}
	if index := slices.IndexFunc(ticket.Links, func(existing core.SuperAgentTicketLink) bool { return existing.ThreadKey == trigger.ThreadKey }); index >= 0 {
		ticket.Links[index] = link
		return
	}
	ticket.Links = append(ticket.Links, link)
}

func (s *Service) finishTrigger(ctx context.Context, trigger core.SuperAgentTrigger, disposition, reason, ticketID string) {
	trigger.Disposition, trigger.Reason, trigger.TicketID = disposition, reason, ticketID
	trigger.UpdatedAt = s.now().UTC()
	if err := s.store.UpdateSuperAgentTrigger(ctx, trigger); err != nil && ctx.Err() == nil {
		s.log.Error("update super agent trigger", "id", trigger.ID, "err", err)
	}
	s.notify()
}

func (s *Service) mutateTicket(ctx context.Context, id string, mutate func(*core.SuperAgentTicket) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ticket, err := s.store.GetSuperAgentTicket(ctx, id)
	if err != nil {
		return err
	}
	if ticket == nil {
		return ErrNotFound
	}
	if err := mutate(ticket); err != nil {
		return err
	}
	ticket.UpdatedAt = s.now().UTC()
	closeSignals(ticket, ticket.UpdatedAt)
	releaseWorkspace(ticket)
	if err := s.store.UpsertSuperAgentTicket(ctx, *ticket); err != nil {
		return err
	}
	s.recordTicket(ctx, *ticket)
	s.notify()
	return nil
}

func (s *Service) startQueued(ctx context.Context) error {
	queued, err := s.store.ListSuperAgentTicketsByStatus(ctx, core.SuperAgentTicketQueued)
	if err != nil {
		return err
	}
	// A Ticket whose directory another one holds waits for it. Claims taken
	// in this pass count too, before their Tickets record them.
	var holders map[string]core.SuperAgentTicket
	if slices.ContainsFunc(queued, needsWorkspace) {
		if holders, err = s.workspaceHolders(ctx); err != nil {
			return err
		}
	}
	for _, ticket := range queued {
		if needsWorkspace(ticket) && ticket.Workspace.HeldSince.IsZero() {
			key := s.workspaceKey(*ticket.Workspace)
			if holder, held := holders[key]; held && holder.ID != ticket.ID {
				continue
			}
		}
		agent, err := s.store.GetSuperAgent(ctx, ticket.SuperAgentID)
		if err != nil {
			return err
		}
		if agent == nil || !agent.Enabled || len(s.unmetDependencies(ctx, ticket)) > 0 {
			continue
		}
		s.mu.Lock()
		active := 0
		for runningID := range s.running {
			if strings.HasPrefix(runningID, agent.ID+"|") {
				active++
			}
		}
		_, alreadyRunning := s.running[agent.ID+"|"+ticket.ID]
		blocked := alreadyRunning || active >= max(agent.MaxConcurrency, 1) || s.now().Before(s.notBefore[ticket.ID])
		if blocked {
			s.mu.Unlock()
			continue
		}
		runCtx, cancel := context.WithTimeout(ctx, workerTimeout)
		s.running[agent.ID+"|"+ticket.ID] = cancel
		s.mu.Unlock()
		if needsWorkspace(ticket) {
			holders[s.workspaceKey(*ticket.Workspace)] = ticket
		}
		s.wg.Add(1)
		go func(agent core.SuperAgent, ticketID string) {
			defer s.wg.Done()
			defer func() {
				s.mu.Lock()
				delete(s.running, agent.ID+"|"+ticketID)
				delete(s.stopping, ticketID)
				delete(s.turnStarts, ticketID)
				s.mu.Unlock()
				cancel()
				s.notify()
			}()
			s.runTicket(runCtx, ctx, agent, ticketID)
		}(*agent, ticket.ID)
	}
	return nil
}

// runTicket executes one turn. runCtx ends with a stop or the turn's deadline;
// ctx ends with the daemon, in which case the Ticket stays running in the store
// and is recovered on the next Start.
func (s *Service) runTicket(runCtx, ctx context.Context, agent core.SuperAgent, ticketID string) {
	var delivered int
	var input string
	var ticket core.SuperAgentTicket
	err := s.mutateTicket(ctx, ticketID, func(current *core.SuperAgentTicket) error {
		if current.Status != core.SuperAgentTicketQueued {
			return ErrConflict
		}
		current.Status = core.SuperAgentTicketRunning
		current.Attempts++
		if current.StartedAt.IsZero() {
			current.StartedAt = s.now().UTC()
		}
		if needsWorkspace(*current) && current.Workspace.HeldSince.IsZero() {
			current.Workspace.HeldSince = s.now().UTC()
		}
		s.turnStarts[ticketID] = s.now().UTC()
		delivered = len(current.Inputs)
		input = strings.Join(current.Inputs, "\n\n---\n\n")
		ticket = *current
		return nil
	})
	if err != nil {
		if !errors.Is(err, ErrConflict) && ctx.Err() == nil {
			s.log.Error("start super agent ticket", "ticket", ticketID, "err", err)
		}
		return
	}
	placement, err := s.placeTicket(ctx, ticket)
	if err == nil {
		err = linkTicketWork(s.existingTicketDir(ticket), placement)
	}
	if err != nil {
		s.log.Warn("place super agent ticket", "ticket", ticketID, "err", err)
	}
	s.appendTicketLog(ticket, ticketInputsFile, input)
	answer, runErr := s.workers.Run(runCtx, ticket.Worker, ticket.ConversationID, input, RunOptions{
		Dir: placement.ConversationDir, BaseRef: s.baseCommit(ctx, ticket), Progress: s.progressRecorder(ticket),
	})
	if ctx.Err() != nil {
		return
	}
	s.mu.Lock()
	stopped := s.stopping[ticketID]
	s.mu.Unlock()
	if stopped {
		return
	}
	var reply string
	var replyTo = ticket.Reply
	var saved core.SuperAgentTicket
	finished := time.Time{}
	mutateErr := s.mutateTicket(ctx, ticketID, func(current *core.SuperAgentTicket) error {
		defer func() { saved = *current }()
		if current.Status != core.SuperAgentTicketRunning {
			return nil // Stopped while this turn was finishing.
		}
		replyTo = current.Reply
		now := s.now().UTC()
		if runErr != nil {
			current.Error = runErr.Error()
			if IsRetryable(runErr) && current.Attempts < maxWorkerAttempts {
				current.Status = core.SuperAgentTicketQueued
				s.notBefore[ticketID] = now.Add(retryDelay)
				return nil
			}
			current.Status = core.SuperAgentTicketFailed
			current.FinishedAt = now
			finished = now
			reply = fmt.Sprintf("I could not finish “%s”: %s", current.Title, runErr.Error())
			return nil
		}
		current.Inputs = slices.Clone(current.Inputs[min(delivered, len(current.Inputs)):])
		current.Output, current.Error = answer, ""
		current.Attempts = 0
		if len(current.Inputs) > 0 {
			// Follow-ups arrived during this turn. The worker reads them before
			// anyone is told the work is finished.
			current.Status = core.SuperAgentTicketQueued
			return nil
		}
		current.Status = core.SuperAgentTicketReady
		current.FinishedAt = now
		finished = now
		reply = answer
		return nil
	})
	if mutateErr != nil {
		s.log.Error("finish super agent ticket", "ticket", ticketID, "err", mutateErr)
		return
	}
	if runErr != nil {
		s.noteRunFailure(ctx, agent, ticket.Worker, runErr)
	}
	if runErr == nil {
		s.appendTicketLog(ticket, ticketResultFile, answer)
	}
	if finished.IsZero() || strings.TrimSpace(reply) == "" {
		return
	}
	// The router tells people what the turn did and moves dependent work
	// along. If it cannot be woken, people still hear the worker.
	failed := runErr != nil
	report := answer
	if failed {
		report = runErr.Error()
	}
	// An independent reviewer looks at the work before the brain hears of it.
	review := ""
	if !failed && reviewable(ctx, agent, saved, placement) {
		review = s.independentReview(runCtx, agent, saved, placement.ConversationDir)
		s.mu.Lock()
		stopped := s.stopping[ticketID]
		s.mu.Unlock()
		if stopped || ctx.Err() != nil {
			return
		}
	}
	if wakeErr := s.wakeRouter(ctx, agent, saved, replyTo, report, failed, review); wakeErr == nil {
		return
	} else if ctx.Err() == nil {
		s.log.Error("wake super agent router", "ticket", ticketID, "err", wakeErr)
	}
	if replyErr := s.sendReply(ctx, agent, ticket.SourceID, replyTo, reply); replyErr != nil {
		_ = s.mutateTicket(ctx, ticketID, func(current *core.SuperAgentTicket) error {
			current.ReplyError = replyErr.Error()
			return nil
		})
	}
}

// StopTicket is the only way work is withdrawn. A stopped Ticket never resumes;
// cancelling its context closes the local turn or the remote stream, and the
// remote daemon cancels the invocation when its client disconnects.
func (s *Service) StopTicket(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ticket, err := s.store.GetSuperAgentTicket(ctx, id)
	if err != nil {
		return err
	}
	if ticket == nil {
		return ErrNotFound
	}
	if ticket.Status == core.SuperAgentTicketStopped {
		return ErrConflict
	}
	now := s.now().UTC()
	ticket.Status = core.SuperAgentTicketStopped
	ticket.Inputs = nil
	ticket.FinishedAt, ticket.UpdatedAt = now, now
	closeSignals(ticket, now)
	releaseWorkspace(ticket)
	if err := s.store.UpsertSuperAgentTicket(ctx, *ticket); err != nil {
		return err
	}
	s.recordTicket(ctx, *ticket)
	if cancel := s.running[ticket.SuperAgentID+"|"+id]; cancel != nil {
		s.stopping[id] = true
		cancel()
	}
	s.cancelTicketTimers(ctx, *ticket)
	return nil
}

// RetryTicket requeues a failed Ticket with its undelivered input.
func (s *Service) RetryTicket(ctx context.Context, id string) error {
	return s.mutateTicket(ctx, id, func(ticket *core.SuperAgentTicket) error {
		if ticket.Status != core.SuperAgentTicketFailed {
			return ErrConflict
		}
		ticket.Status = core.SuperAgentTicketQueued
		ticket.Attempts = 0
		ticket.Error = ""
		ticket.FinishedAt = time.Time{}
		return nil
	})
}

// TriggerManually submits a Console request. The Console is already
// authenticated as an administrator, so it passes the gate as an explicit ask.
func (s *Service) TriggerManually(ctx context.Context, id, text string) (*core.SuperAgentTrigger, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("text is required")
	}
	agent, err := s.store.GetSuperAgent(ctx, id)
	if err != nil {
		return nil, err
	}
	if agent == nil {
		return nil, ErrNotFound
	}
	if !agent.Enabled {
		return nil, errors.New("super agent is disabled")
	}
	now := s.now().UTC()
	triggerID := "sat_" + uuid.NewString()
	trigger := core.SuperAgentTrigger{
		ID: triggerID, SuperAgentID: agent.ID, SourceID: "console", SourceKind: "console",
		DedupeKey: triggerID, ThreadKey: "console:" + triggerID, Actor: "console", Addressed: true,
		Text: truncateRunes(text, maxTriggerRunes), Disposition: core.SuperAgentDispositionPending,
		ReceivedAt: now, UpdatedAt: now,
	}
	if _, err := s.store.InsertSuperAgentTrigger(ctx, trigger); err != nil {
		return nil, err
	}
	s.notify()
	return &trigger, nil
}

// Activity returns recent Tickets, Triggers, worker reachability and Source health.
func (s *Service) Activity(ctx context.Context, id string) (*Activity, error) {
	agent, err := s.store.GetSuperAgent(ctx, id)
	if err != nil {
		return nil, err
	}
	if agent == nil {
		return nil, ErrNotFound
	}
	tickets, err := s.store.ListSuperAgentTickets(ctx, id, 50)
	if err != nil {
		return nil, err
	}
	triggers, err := s.store.ListSuperAgentTriggers(ctx, id, 50)
	if err != nil {
		return nil, err
	}
	timers, timerWarning, err := s.activityTimers(ctx, id)
	if err != nil {
		return nil, err
	}
	workers := s.workerStatuses(ctx, *agent)
	activity := &Activity{
		Tickets: tickets, Triggers: triggers, Workers: workers, Machines: s.machineStatuses(ctx, *agent),
		Sources: map[string]SourceHealth{}, Timers: timers, TimerWarning: timerWarning,
	}
	activity.RuntimeHealth = s.rosterHealth(ctx, s.withWorkerRuntimes(ctx, *agent), workers)
	activity.WorkspaceClaims = s.workspaceClaims(ctx, *agent)
	activity.CostTiers = s.rosterTiers(ctx, s.withWorkerRuntimes(ctx, *agent), workers)
	if activity.Plans, err = s.Plans(ctx, id); err != nil {
		return nil, err
	}
	if activity.Trust, err = s.Trust(ctx, id); err != nil {
		return nil, err
	}
	s.mu.Lock()
	for _, source := range agent.Sources {
		activity.Sources[source.ID] = s.health[source.ID]
	}
	s.mu.Unlock()
	return activity, nil
}

func (s *Service) workerStatuses(ctx context.Context, agent core.SuperAgent) []core.SuperAgentWorkerStatus {
	statuses := make([]core.SuperAgentWorkerStatus, len(agent.Workers))
	var wg sync.WaitGroup
	for index, worker := range agent.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statusCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			statuses[index] = s.workers.Status(statusCtx, worker)
			statuses[index].WorkerID = worker.ID
		}()
	}
	wg.Wait()
	return statuses
}

func (s *Service) recordEvent(sourceID string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	health := s.health[sourceID]
	health.LastEventAt = at
	s.health[sourceID] = health
}

func (s *Service) recordSourceWarning(sourceID, warning string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	health := s.health[sourceID]
	health.Warning = warning
	s.health[sourceID] = health
}

func (s *Service) recordSourceError(sourceID string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	health := s.health[sourceID]
	if err == nil {
		health.LastError, health.LastErrorAt = "", time.Time{}
	} else {
		health.LastError, health.LastErrorAt = err.Error(), s.now().UTC()
	}
	s.health[sourceID] = health
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "\n[truncated]"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
