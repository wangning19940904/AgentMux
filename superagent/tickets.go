package superagent

import (
	"context"
	"slices"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
)

const (
	defaultTicketListLimit = 100
	maxTicketListLimit     = 500
	ticketSummaryOutput    = 2000
)

// TicketSummary is a Ticket as a list across agents and machines shows it:
// who owns it, where its worker runs, how it stands and how its last turn
// ended. Reply addresses, linked threads and queued inputs stay with the
// Ticket itself.
type TicketSummary struct {
	core.SuperAgentTicket
	SuperAgentName string `json:"super_agent_name"`
	// MachineName is where the worker runs. Empty means the agent's machine.
	MachineName   string `json:"machine_name,omitempty"`
	PendingInputs int    `json:"pending_inputs,omitempty"`
}

// Tickets lists the newest Tickets of every agent on this machine.
func (s *Service) Tickets(ctx context.Context, limit int) ([]TicketSummary, error) {
	if limit <= 0 {
		limit = defaultTicketListLimit
	}
	limit = min(limit, maxTicketListLimit)
	agents, err := s.store.ListSuperAgents(ctx)
	if err != nil {
		return nil, err
	}
	fleet := s.machineFleet()
	summaries := []TicketSummary{}
	for _, agent := range agents {
		tickets, err := s.store.ListSuperAgentTickets(ctx, agent.ID, limit)
		if err != nil {
			return nil, err
		}
		for _, ticket := range tickets {
			summary := TicketSummary{SuperAgentTicket: ticket, SuperAgentName: agent.Name, PendingInputs: len(ticket.Inputs)}
			summary.Inputs, summary.Reply, summary.Links = nil, nil, nil
			summary.Output = truncateRunes(summary.Output, ticketSummaryOutput)
			if hostID := ticket.Worker.HostID; hostID != "" {
				summary.MachineName = hostID
				if fleet != nil {
					if name, known := fleet.MachineName(hostID); known {
						summary.MachineName = name
					}
				}
			}
			summaries = append(summaries, summary)
		}
	}
	slices.SortStableFunc(summaries, func(a, b TicketSummary) int {
		if order := b.CreatedAt.Compare(a.CreatedAt); order != 0 {
			return order
		}
		return strings.Compare(a.ID, b.ID)
	})
	if len(summaries) > limit {
		summaries = summaries[:limit]
	}
	return summaries, nil
}
