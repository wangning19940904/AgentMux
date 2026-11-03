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
	passInterval        = 2 * time.Second
	maxRouterEvals      = 4
	maxRouterAttempts   = 3
	maxWorkerAttempts   = 3
	retryDelay          = 15 * time.Second
	routerTimeout       = 5 * time.Minute
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
	GetSuperAgentSourceState(context.Context, string) (string, error)
	SetSuperAgentSourceState(context.Context, string, string, string) error
	GetAgentInstance(context.Context, string) (*core.AgentInstance, error)
	UpsertAgentInstance(context.Context, *core.AgentInstance) error
	DeleteAgentInstance(context.Context, string) error
	ListAgentInstances(context.Context) ([]core.AgentInstance, error)
	ListChannels(context.Context) ([]core.Channel, error)
}

// Options holds the service's collaborators.
type Options struct {
	Store   Store
	Invoker core.Invoker
	Workers WorkerRunner
	Replies ReplySender
	Mail    MailClient
	Docs    DocumentSubscriber
	Log     *slog.Logger
	Now     func() time.Time
	// ChannelHasAgent reports whether a channel has its own Agent to take the
	// messages a Super Agent does not claim.
	ChannelHasAgent func(context.Context, string) bool
	// RuntimeAvailable reports whether an agent framework can run on this machine.
	RuntimeAvailable func(string) bool
	// StateDir holds directories the service creates. Empty means ~/.agentmux.
	StateDir string
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

	mu             sync.Mutex
	running        map[string]context.CancelFunc
	stopping       map[string]bool
	evaluating     map[string]bool
	routerAttempts map[string]int
	notBefore      map[string]time.Time
	health         map[string]SourceHealth

	wg     sync.WaitGroup
	cancel context.CancelFunc
	ctx    context.Context
	once   sync.Once

	claims          claimIndex
	mail            MailClient
	docs            DocumentSubscriber
	documents       map[string][]documentRef
	channelHasAgent func(context.Context, string) bool

	runtimeAvailable func(string) bool
	state            string
}

// SourceHealth is the latest intake outcome of one Source.
type SourceHealth struct {
	LastEventAt time.Time `json:"last_event_at,omitzero"`
	LastError   string    `json:"last_error,omitempty"`
	LastErrorAt time.Time `json:"last_error_at,omitzero"`
}

// Activity is the Console's view of one Super Agent.
type Activity struct {
	Tickets  []core.SuperAgentTicket       `json:"tickets"`
	Triggers []core.SuperAgentTrigger      `json:"triggers"`
	Workers  []core.SuperAgentWorkerStatus `json:"workers"`
	Sources  map[string]SourceHealth       `json:"sources"`
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
		routerAttempts: map[string]int{}, notBefore: map[string]time.Time{}, health: map[string]SourceHealth{},
		mail: options.Mail, docs: options.Docs, documents: map[string][]documentRef{}, channelHasAgent: options.ChannelHasAgent,
		runtimeAvailable: options.RuntimeAvailable, state: options.StateDir,
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
		if err := s.store.UpsertSuperAgentTicket(ctx, ticket); err != nil {
			cancel()
			return fmt.Errorf("recover super agent ticket %s: %w", ticket.ID, err)
		}
	}
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

// Pass evaluates pending Triggers and starts queued Tickets once.
func (s *Service) Pass(ctx context.Context) {
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
		busy := s.evaluating[key] || len(s.evaluating) >= maxRouterEvals || s.now().Before(s.notBefore[trigger.ID])
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
	tickets, err := s.store.ListSuperAgentThreadTickets(ctx, agent.ID, trigger.ThreadKey)
	if err != nil {
		s.log.Error("load thread tickets", "thread", trigger.ThreadKey, "err", err)
		return
	}
	decision, err := s.route(ctx, *agent, trigger, tickets)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		s.routerFailed(ctx, *agent, trigger, tickets, err)
		return
	}
	s.mu.Lock()
	delete(s.routerAttempts, trigger.ID)
	delete(s.notBefore, trigger.ID)
	s.mu.Unlock()
	if agent.Mode == core.SuperAgentModeObserve {
		s.finishTrigger(ctx, trigger, core.SuperAgentDispositionObserved, decision.summary(), "")
		return
	}
	if err := s.apply(ctx, *agent, trigger, tickets, decision); err != nil {
		s.log.Error("apply router decision", "trigger", trigger.ID, "err", err)
		s.finishTrigger(ctx, trigger, core.SuperAgentDispositionFailed, err.Error(), "")
		s.sendReply(ctx, *agent, trigger.SourceID, trigger.Reply, "I could not process this request because my router chose something I cannot carry out. Please try again, or ask the operator to check this Super Agent.")
	}
}

// routerFailed retries a transient router failure. Once retries run out, a
// message on a thread with a live Ticket still reaches that Ticket, so a router
// outage never strands a conversation that is already authorized.
func (s *Service) routerFailed(ctx context.Context, agent core.SuperAgent, trigger core.SuperAgentTrigger, tickets []core.SuperAgentTicket, cause error) {
	s.mu.Lock()
	s.routerAttempts[trigger.ID]++
	attempts := s.routerAttempts[trigger.ID]
	if attempts < maxRouterAttempts {
		s.notBefore[trigger.ID] = s.now().Add(retryDelay)
		s.mu.Unlock()
		s.log.Warn("super agent router failed; retrying", "trigger", trigger.ID, "attempt", attempts, "err", cause)
		return
	}
	delete(s.routerAttempts, trigger.ID)
	delete(s.notBefore, trigger.ID)
	s.mu.Unlock()
	if live := latestLiveTicket(tickets); live != nil && agent.Mode == core.SuperAgentModeAct {
		if err := s.attach(ctx, live.ID, trigger); err == nil {
			s.finishTrigger(ctx, trigger, core.SuperAgentDispositionFollowUp, "router unavailable; delivered to the thread's ticket", live.ID)
			return
		}
	}
	s.finishTrigger(ctx, trigger, core.SuperAgentDispositionFailed, "router failed: "+cause.Error(), "")
	if agent.Mode == core.SuperAgentModeAct {
		s.sendReply(ctx, agent, trigger.SourceID, trigger.Reply, "I could not process this request because my router is unavailable. Please try again later.")
	}
}

func latestLiveTicket(tickets []core.SuperAgentTicket) *core.SuperAgentTicket {
	for index := len(tickets) - 1; index >= 0; index-- {
		if tickets[index].Status != core.SuperAgentTicketStopped {
			return &tickets[index]
		}
	}
	return nil
}

// apply executes one validated decision. Everything a decision names is checked
// against the host's own records: a worker must be on this Super Agent's roster
// and a Ticket must already belong to the Trigger's thread.
func (s *Service) apply(ctx context.Context, agent core.SuperAgent, trigger core.SuperAgentTrigger, tickets []core.SuperAgentTicket, decision routerDecision) error {
	switch decision.Decision {
	case decisionIgnore:
		s.finishTrigger(ctx, trigger, core.SuperAgentDispositionIgnored, decision.Reason, "")
		s.sendReply(ctx, agent, trigger.SourceID, trigger.Reply, decision.Reply)
		return nil
	case decisionAccept:
		return s.accept(ctx, agent, trigger, decision)
	case decisionAttach, decisionStop:
		ticket := threadTicket(tickets, decision.TicketID)
		if ticket == nil {
			return fmt.Errorf("router named ticket %q, which is not on this thread", decision.TicketID)
		}
		if decision.Decision == decisionStop {
			if err := s.StopTicket(ctx, ticket.ID); err != nil && !errors.Is(err, ErrConflict) {
				return err
			}
			s.finishTrigger(ctx, trigger, core.SuperAgentDispositionStopped, decision.Reason, ticket.ID)
		} else {
			if err := s.attach(ctx, ticket.ID, trigger); err != nil {
				return err
			}
			s.finishTrigger(ctx, trigger, core.SuperAgentDispositionFollowUp, decision.Reason, ticket.ID)
		}
		s.sendReply(ctx, agent, trigger.SourceID, trigger.Reply, decision.Reply)
		return nil
	default:
		return fmt.Errorf("unknown router decision %q", decision.Decision)
	}
}

func threadTicket(tickets []core.SuperAgentTicket, id string) *core.SuperAgentTicket {
	for index := range tickets {
		if tickets[index].ID == id {
			return &tickets[index]
		}
	}
	return nil
}

func (s *Service) accept(ctx context.Context, agent core.SuperAgent, trigger core.SuperAgentTrigger, decision routerDecision) error {
	worker, err := s.assignedWorker(ctx, agent, decision.WorkerID)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	id := "tkt_" + uuid.NewString()
	title := strings.TrimSpace(decision.Title)
	if title == "" {
		title = firstNonEmpty(trigger.Title, truncateRunes(trigger.Text, 80))
	}
	ticket := core.SuperAgentTicket{
		ID: id, SuperAgentID: agent.ID, SourceID: trigger.SourceID, SourceKind: trigger.SourceKind,
		ThreadKey: trigger.ThreadKey, TriggerID: trigger.ID, Title: title, Actor: trigger.Actor,
		Worker: worker, ConversationID: "superagent-" + id, Status: core.SuperAgentTicketQueued,
		Inputs: []string{workerAssignment(trigger, decision)}, Reply: trigger.Reply,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.UpsertSuperAgentTicket(ctx, ticket); err != nil {
		return err
	}
	s.finishTrigger(ctx, trigger, core.SuperAgentDispositionAccepted, decision.Reason, id)
	s.sendReply(ctx, agent, trigger.SourceID, trigger.Reply, decision.Reply)
	return nil
}

func (s *Service) assignedWorker(ctx context.Context, agent core.SuperAgent, workerID string) (core.SuperAgentWorker, error) {
	if runtimeID, ok := strings.CutPrefix(workerID, autoWorkerID+":"); ok {
		return s.launchAutoWorker(ctx, agent, runtimeID)
	}
	index := slices.IndexFunc(agent.Workers, func(worker core.SuperAgentWorker) bool { return worker.ID == workerID })
	if index < 0 {
		return core.SuperAgentWorker{}, fmt.Errorf("router chose worker %q, which is not on the roster", workerID)
	}
	return agent.Workers[index], nil
}

// attach delivers a Trigger to an existing Ticket on its thread. A running
// Ticket runs again after its current turn with the new input; an ended Ticket
// resumes on the same conversation.
func (s *Service) attach(ctx context.Context, ticketID string, trigger core.SuperAgentTrigger) error {
	return s.mutateTicket(ctx, ticketID, func(ticket *core.SuperAgentTicket) error {
		if ticket.Status == core.SuperAgentTicketStopped {
			return fmt.Errorf("%w: ticket %s was stopped", ErrConflict, ticket.ID)
		}
		ticket.Inputs = append(ticket.Inputs, workerFollowUp(trigger))
		ticket.Reply = trigger.Reply
		if ticket.Status != core.SuperAgentTicketRunning {
			ticket.Status = core.SuperAgentTicketQueued
			ticket.Attempts = 0
			ticket.Error = ""
		}
		return nil
	})
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
	if err := s.store.UpsertSuperAgentTicket(ctx, *ticket); err != nil {
		return err
	}
	s.notify()
	return nil
}

func (s *Service) startQueued(ctx context.Context) error {
	queued, err := s.store.ListSuperAgentTicketsByStatus(ctx, core.SuperAgentTicketQueued)
	if err != nil {
		return err
	}
	for _, ticket := range queued {
		agent, err := s.store.GetSuperAgent(ctx, ticket.SuperAgentID)
		if err != nil {
			return err
		}
		if agent == nil || !agent.Enabled {
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
		s.wg.Add(1)
		go func(agent core.SuperAgent, ticketID string) {
			defer s.wg.Done()
			defer func() {
				s.mu.Lock()
				delete(s.running, agent.ID+"|"+ticketID)
				delete(s.stopping, ticketID)
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
	answer, runErr := s.workers.Run(runCtx, ticket.Worker, ticket.ConversationID, input)
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
	finished := time.Time{}
	mutateErr := s.mutateTicket(ctx, ticketID, func(current *core.SuperAgentTicket) error {
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
	if finished.IsZero() || strings.TrimSpace(reply) == "" {
		return
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
	if err := s.store.UpsertSuperAgentTicket(ctx, *ticket); err != nil {
		return err
	}
	if cancel := s.running[ticket.SuperAgentID+"|"+id]; cancel != nil {
		s.stopping[id] = true
		cancel()
	}
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
	activity := &Activity{Tickets: tickets, Triggers: triggers, Workers: s.workerStatuses(ctx, *agent), Sources: map[string]SourceHealth{}}
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
