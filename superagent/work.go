package superagent

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// Work is what every agent on this machine is doing, for a page that watches
// all agents at once: the events still with a router, the Tickets in hand
// and lately finished, and the follow-ups the routers set for themselves.
// Each list carries the agent's name, so the page needs nothing else.
type Work struct {
	Routing []RoutingSummary `json:"routing"`
	Tickets []TicketSummary  `json:"tickets"`
	Timers  []TimerSummary   `json:"timers"`
}

// RoutingSummary is an event a router has yet to decide on. Routing is true
// while the router is reading it; otherwise it waits its turn on its thread.
type RoutingSummary struct {
	ID             string    `json:"id"`
	SuperAgentID   string    `json:"super_agent_id"`
	SuperAgentName string    `json:"super_agent_name"`
	SourceKind     string    `json:"source_kind"`
	Kind           string    `json:"kind,omitempty"`
	ThreadKey      string    `json:"thread_key"`
	Actor          string    `json:"actor,omitempty"`
	ActorName      string    `json:"actor_name,omitempty"`
	Title          string    `json:"title,omitempty"`
	Text           string    `json:"text"`
	TicketID       string    `json:"ticket_id,omitempty"`
	Routing        bool      `json:"routing"`
	ReceivedAt     time.Time `json:"received_at"`
}

// TimerSummary is a follow-up as a list across agents shows it. Where the
// router replies stays with the timer.
type TimerSummary struct {
	core.SuperAgentTimer
	SuperAgentName string `json:"super_agent_name"`
	TicketTitle    string `json:"ticket_title,omitempty"`
}

const (
	// finishedWorkWindow is how long finished Tickets and follow-ups stay on
	// the page, and finishedWorkLimit how many of each per agent.
	finishedWorkWindow = 24 * time.Hour
	finishedWorkLimit  = 30
	maxRoutingShown    = 200
	routingTextShown   = 1000
	// requestLookback is how many of an agent's newest events are searched
	// for the request each Ticket came from.
	requestLookback = 500
)

// Work lists what the agents on this machine are doing now.
func (s *Service) Work(ctx context.Context) (*Work, error) {
	agents, err := s.store.ListSuperAgents(ctx)
	if err != nil {
		return nil, err
	}
	work := &Work{Routing: []RoutingSummary{}, Tickets: []TicketSummary{}, Timers: []TimerSummary{}}
	if len(agents) == 0 {
		return work, nil
	}
	names := make(map[string]string, len(agents))
	for _, agent := range agents {
		names[agent.ID] = agent.Name
	}
	if work.Routing, err = s.routingSummaries(ctx, names); err != nil {
		return nil, err
	}

	// Every Ticket in hand, however old, then each agent's lately finished.
	active, err := s.store.ListSuperAgentTicketsByStatus(ctx, core.SuperAgentTicketQueued, core.SuperAgentTicketRunning)
	if err != nil {
		return nil, err
	}
	since := s.now().UTC().Add(-finishedWorkWindow)
	tickets := map[string]core.SuperAgentTicket{}
	for _, ticket := range active {
		if _, known := names[ticket.SuperAgentID]; known {
			tickets[ticket.ID] = ticket
		}
	}
	for _, agent := range agents {
		recent, err := s.store.ListSuperAgentTickets(ctx, agent.ID, finishedWorkLimit)
		if err != nil {
			return nil, err
		}
		for _, ticket := range recent {
			if ticket.SuperAgentID == agent.ID && (ticketActive(ticket) || ticketTouched(ticket).After(since)) {
				tickets[ticket.ID] = ticket
			}
		}
	}
	requests, err := s.ticketRequests(ctx, agents, tickets)
	if err != nil {
		return nil, err
	}
	fleet := s.machineFleet()
	for _, ticket := range tickets {
		summary := summarizeTicket(ticket, names[ticket.SuperAgentID], fleet)
		if request, found := requests[ticket.TriggerID]; found {
			summary.RequesterName = request.ActorName
			summary.Request = truncateRunes(request.Text, routingTextShown)
		}
		work.Tickets = append(work.Tickets, summary)
	}
	slices.SortStableFunc(work.Tickets, func(a, b TicketSummary) int {
		if a, b := ticketActive(a.SuperAgentTicket), ticketActive(b.SuperAgentTicket); a != b {
			if a {
				return -1
			}
			return 1
		}
		if order := b.CreatedAt.Compare(a.CreatedAt); order != 0 {
			return order
		}
		return strings.Compare(a.ID, b.ID)
	})

	for _, agent := range agents {
		set, err := s.store.ListActiveSuperAgentTimers(ctx, agent.ID)
		if err != nil {
			return nil, err
		}
		finished, err := s.store.ListFinishedSuperAgentTimers(ctx, agent.ID, since, finishedWorkLimit)
		if err != nil {
			return nil, err
		}
		for _, timer := range append(set, finished...) {
			timer.Reply = nil
			summary := TimerSummary{SuperAgentTimer: timer, SuperAgentName: agent.Name}
			if ticket, found := tickets[timer.TicketID]; found {
				summary.TicketTitle = ticket.Title
			}
			work.Timers = append(work.Timers, summary)
		}
	}
	slices.SortStableFunc(work.Timers, func(a, b TimerSummary) int {
		if a, b := a.Status == core.SuperAgentTimerActive, b.Status == core.SuperAgentTimerActive; a != b {
			if a {
				return -1
			}
			return 1
		}
		if a.Status == core.SuperAgentTimerActive {
			return a.DueAt.Compare(b.DueAt)
		}
		return b.UpdatedAt.Compare(a.UpdatedAt)
	})
	return work, nil
}

// routingSummaries are the events waiting for a router, oldest first. The
// oldest on a thread is the one being read while that thread is evaluated.
func (s *Service) routingSummaries(ctx context.Context, names map[string]string) ([]RoutingSummary, error) {
	pending, err := s.store.ListPendingSuperAgentTriggers(ctx, maxRoutingShown)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	evaluating := make(map[string]bool, len(s.evaluating))
	for key, busy := range s.evaluating {
		evaluating[key] = busy
	}
	s.mu.Unlock()
	seen := map[string]bool{}
	out := []RoutingSummary{}
	for _, trigger := range pending {
		name, known := names[trigger.SuperAgentID]
		if !known {
			continue
		}
		key := trigger.SuperAgentID + "|" + trigger.ThreadKey
		out = append(out, RoutingSummary{
			ID: trigger.ID, SuperAgentID: trigger.SuperAgentID, SuperAgentName: name,
			SourceKind: trigger.SourceKind, Kind: trigger.Kind, ThreadKey: trigger.ThreadKey,
			Actor: trigger.Actor, ActorName: trigger.ActorName, Title: trigger.Title,
			Text: truncateRunes(trigger.Text, routingTextShown), TicketID: trigger.TicketID,
			Routing: !seen[key] && evaluating[key], ReceivedAt: trigger.ReceivedAt,
		})
		seen[key] = true
	}
	return out, nil
}

// ticketRequests finds the event each Ticket came from among its agent's
// newest events, by event id.
func (s *Service) ticketRequests(ctx context.Context, agents []core.SuperAgent, tickets map[string]core.SuperAgentTicket) (map[string]core.SuperAgentTrigger, error) {
	wanted := map[string]map[string]bool{}
	for _, ticket := range tickets {
		if ticket.TriggerID == "" {
			continue
		}
		if wanted[ticket.SuperAgentID] == nil {
			wanted[ticket.SuperAgentID] = map[string]bool{}
		}
		wanted[ticket.SuperAgentID][ticket.TriggerID] = true
	}
	found := map[string]core.SuperAgentTrigger{}
	for _, agent := range agents {
		ids := wanted[agent.ID]
		if len(ids) == 0 {
			continue
		}
		triggers, err := s.store.ListSuperAgentTriggers(ctx, agent.ID, requestLookback)
		if err != nil {
			return nil, err
		}
		for _, trigger := range triggers {
			if ids[trigger.ID] {
				found[trigger.ID] = trigger
			}
		}
	}
	return found, nil
}

func ticketActive(ticket core.SuperAgentTicket) bool {
	return ticket.Status == core.SuperAgentTicketQueued || ticket.Status == core.SuperAgentTicketRunning
}

// ticketTouched is when a Ticket last changed, for keeping finished ones on
// the page a while.
func ticketTouched(ticket core.SuperAgentTicket) time.Time {
	latest := ticket.CreatedAt
	for _, at := range []time.Time{ticket.FinishedAt, ticket.UpdatedAt} {
		if at.After(latest) {
			latest = at
		}
	}
	return latest
}
