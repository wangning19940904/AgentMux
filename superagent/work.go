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
// and lately finished, the follow-ups the routers set for themselves, and
// the events that tie them together. Each list carries the agent's name, so
// the page needs nothing else.
type Work struct {
	Routing []RoutingSummary `json:"routing"`
	Tickets []TicketSummary  `json:"tickets"`
	Timers  []TimerSummary   `json:"timers"`
	Events  []WorkEvent      `json:"events"`
}

// RoutingSummary is an event a router has yet to decide on. Routing is true
// while the router is reading it; otherwise it waits its turn on its thread.
// TicketID is the Ticket an event from AgentMux is about.
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
// router replies stays with the timer. FromTicketID is the Ticket the router
// was handling when it set a follow-up that names none.
type TimerSummary struct {
	core.SuperAgentTimer
	SuperAgentName string `json:"super_agent_name"`
	TicketTitle    string `json:"ticket_title,omitempty"`
	FromTicketID   string `json:"from_ticket_id,omitempty"`
}

// WorkEvent is an event a router delivered to a Ticket that was already
// there: a later message for its worker, or the result of another Ticket,
// which FromTicketID names.
type WorkEvent struct {
	ID           string    `json:"id"`
	SuperAgentID string    `json:"super_agent_id"`
	SourceKind   string    `json:"source_kind"`
	Kind         string    `json:"kind,omitempty"`
	ThreadKey    string    `json:"thread_key"`
	ActorName    string    `json:"actor_name,omitempty"`
	Text         string    `json:"text"`
	TicketID     string    `json:"ticket_id"`
	FromTicketID string    `json:"from_ticket_id,omitempty"`
	ReceivedAt   time.Time `json:"received_at"`
}

const (
	// finishedWorkWindow is how long finished Tickets and follow-ups stay on
	// the page, and finishedWorkLimit how many of each per agent.
	finishedWorkWindow = 24 * time.Hour
	finishedWorkLimit  = 30
	maxRoutingShown    = 200
	routingTextShown   = 1000
	// eventLookback is how many of an agent's newest events are searched for
	// the request each Ticket came from and for what was delivered to it.
	eventLookback = 500
	// maxLinkedTickets is how many older Tickets join the page because shown
	// work came from them or waits on them; maxWorkEvents and workEventText
	// bound the delivered events.
	maxLinkedTickets = 40
	maxWorkEvents    = 200
	workEventText    = 400
)

// Work lists what the agents on this machine are doing now.
func (s *Service) Work(ctx context.Context) (*Work, error) {
	agents, err := s.store.ListSuperAgents(ctx)
	if err != nil {
		return nil, err
	}
	work := &Work{Routing: []RoutingSummary{}, Tickets: []TicketSummary{}, Timers: []TimerSummary{}, Events: []WorkEvent{}}
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

	timers := map[string]core.SuperAgentTimer{}
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
			timers[timer.ID] = timer
			work.Timers = append(work.Timers, TimerSummary{SuperAgentTimer: timer, SuperAgentName: agent.Name})
		}
	}

	// The newest events of each agent with something to show say where its
	// Tickets and follow-ups came from.
	busy := map[string]bool{}
	for _, ticket := range tickets {
		busy[ticket.SuperAgentID] = true
	}
	for _, timer := range timers {
		busy[timer.SuperAgentID] = true
	}
	events := map[string]core.SuperAgentTrigger{}
	for _, agent := range agents {
		if !busy[agent.ID] {
			continue
		}
		triggers, err := s.store.ListSuperAgentTriggers(ctx, agent.ID, eventLookback)
		if err != nil {
			return nil, err
		}
		for _, trigger := range triggers {
			events[trigger.ID] = trigger
		}
	}
	// origin is the Ticket and the follow-up a Ticket was started from, when
	// an event from AgentMux started it.
	origin := func(ticket core.SuperAgentTicket) (kind, ticketID, timerID string) {
		trigger, found := events[ticket.TriggerID]
		if !found || trigger.Kind == "" {
			return "", "", ""
		}
		ticketID, timerID = eventSubject(trigger)
		if ticketID == "" && timerID != "" {
			timer, known := timers[timerID]
			if !known {
				if stored, err := s.store.GetSuperAgentTimer(ctx, timerID); err == nil && stored != nil {
					timer = *stored
				}
				timers[timerID] = timer
			}
			ticketID = timer.TicketID
		}
		if ticketID == ticket.ID {
			ticketID = ""
		}
		return trigger.Kind, ticketID, timerID
	}
	for index, timer := range work.Timers {
		if timer.TicketID != "" {
			continue
		}
		if trigger, found := events[timer.SetBy]; found {
			subject, _ := eventSubject(trigger)
			work.Timers[index].FromTicketID = firstNonEmpty(trigger.TicketID, subject)
		}
	}

	// Older Tickets that shown work came from, waits on or follows up join
	// the page, so each chain is whole.
	missed := map[string]bool{}
	for linked := 0; ; {
		var missing []string
		want := func(id string) {
			if _, shown := tickets[id]; id != "" && !shown && !missed[id] && !slices.Contains(missing, id) {
				missing = append(missing, id)
			}
		}
		for _, ticket := range tickets {
			_, from, _ := origin(ticket)
			want(from)
			want(ticket.BaseTicket)
			for _, id := range ticket.DependsOn {
				want(id)
			}
		}
		for _, timer := range work.Timers {
			want(timer.TicketID)
			want(timer.FromTicketID)
		}
		if len(missing) == 0 || linked >= maxLinkedTickets {
			break
		}
		slices.Sort(missing)
		for _, id := range missing {
			ticket, err := s.store.GetSuperAgentTicket(ctx, id)
			if err != nil {
				return nil, err
			}
			if _, known := names[ticketAgent(ticket)]; !known || linked >= maxLinkedTickets {
				missed[id] = true
				continue
			}
			tickets[id] = *ticket
			linked++
		}
	}

	fleet := s.machineFleet()
	for _, ticket := range tickets {
		summary := summarizeTicket(ticket, names[ticket.SuperAgentID], fleet)
		// The page polls every few seconds and shows neither the reviews nor
		// the acceptance criteria, which made up about half of what it read.
		summary.Reviews, summary.DoneWhen, summary.AutoReview = nil, nil, nil
		if request, found := events[ticket.TriggerID]; found {
			summary.RequesterName = request.ActorName
			summary.Request = truncateRunes(request.Text, routingTextShown)
		}
		summary.OriginKind, summary.OriginTicketID, summary.OriginTimerID = origin(ticket)
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

	for index, timer := range work.Timers {
		if ticket, found := tickets[timer.TicketID]; found {
			work.Timers[index].TicketTitle = ticket.Title
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

	// What a router delivered to a shown Ticket after it started. An event
	// about the Ticket itself, such as its own update sent back for rework,
	// is left to the Ticket's reviews.
	for _, trigger := range events {
		if trigger.Disposition != core.SuperAgentDispositionFollowUp {
			continue
		}
		if _, shown := tickets[trigger.TicketID]; !shown {
			continue
		}
		from, _ := eventSubject(trigger)
		if from == trigger.TicketID {
			continue
		}
		work.Events = append(work.Events, WorkEvent{
			ID: trigger.ID, SuperAgentID: trigger.SuperAgentID, SourceKind: trigger.SourceKind, Kind: trigger.Kind,
			ThreadKey: trigger.ThreadKey, ActorName: trigger.ActorName, Text: truncateRunes(trigger.Text, workEventText),
			TicketID: trigger.TicketID, FromTicketID: from, ReceivedAt: trigger.ReceivedAt,
		})
	}
	slices.SortFunc(work.Events, func(a, b WorkEvent) int {
		if order := a.ReceivedAt.Compare(b.ReceivedAt); order != 0 {
			return order
		}
		return strings.Compare(a.ID, b.ID)
	})
	if extra := len(work.Events) - maxWorkEvents; extra > 0 {
		work.Events = work.Events[extra:]
	}
	return work, nil
}

// eventSubject is the Ticket an event from AgentMux is about and the
// follow-up that came due. An event stored before it carried them names
// them in its dedupe key.
func eventSubject(trigger core.SuperAgentTrigger) (ticketID, timerID string) {
	if trigger.SubjectTicketID != "" || trigger.TimerID != "" {
		return trigger.SubjectTicketID, trigger.TimerID
	}
	parts := strings.SplitN(trigger.DedupeKey, ":", 3)
	if len(parts) < 3 {
		return "", ""
	}
	switch {
	case trigger.Kind == core.SuperAgentTriggerTicketUpdate && parts[0] == "ticket":
		return parts[1], ""
	case trigger.Kind == core.SuperAgentTriggerTimer && parts[0] == "timer":
		return "", parts[1]
	}
	return "", ""
}

// ticketAgent is the agent a Ticket belongs to, or "" for a missing Ticket.
func ticketAgent(ticket *core.SuperAgentTicket) string {
	if ticket == nil {
		return ""
	}
	return ticket.SuperAgentID
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
