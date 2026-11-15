package superagent

import (
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// Supervision watches an agent's running Tickets from the records AgentMux
// already keeps: when each turn started, the worker's latest progress, which
// turns this daemon is running and whether a remote worker's machine answers.
// It calls no model. What it notices is a signal, recorded on the Ticket once
// per stretch of time in which it holds. In act mode the brain is woken with
// it, through the same path as a ticket update.

const (
	supervisionInterval = 30 * time.Second
	// stallFloor is the shortest silence counted as a stall; a Ticket with
	// expect_minutes allows half as long again as the brain expected.
	stallFloor = 20 * time.Minute
	// orphanGrace keeps a Ticket that is between two steps, or a machine that
	// missed one status check, from counting as orphaned.
	orphanGrace = time.Minute
	// supervisionCooldown is the least time between two supervision wake-ups
	// on one thread. Signals that come up in between wait and are sent
	// together.
	supervisionCooldown = 10 * time.Minute
	maxTicketSignals    = 20
	statusCheckTimeout  = 10 * time.Second
)

// supervisionMode is the agent's mode, observe when unset.
func supervisionMode(agent core.SuperAgent) string {
	switch mode := strings.ToLower(strings.TrimSpace(agent.Supervision)); mode {
	case core.SuperAgentSupervisionAct, core.SuperAgentSupervisionOff:
		return mode
	default:
		return core.SuperAgentSupervisionObserve
	}
}

func normalizeSupervision(agent *core.SuperAgent) error {
	mode := strings.ToLower(strings.TrimSpace(agent.Supervision))
	switch mode {
	case "", core.SuperAgentSupervisionObserve:
		agent.Supervision = ""
	case core.SuperAgentSupervisionAct, core.SuperAgentSupervisionOff:
		agent.Supervision = mode
	default:
		return fmt.Errorf("supervision must be %s, %s or %s", core.SuperAgentSupervisionObserve, core.SuperAgentSupervisionAct, core.SuperAgentSupervisionOff)
	}
	return nil
}

// stallLimit is how long a running Ticket may show no progress.
func stallLimit(expectMinutes int) time.Duration {
	return max(stallFloor, time.Duration(expectMinutes)*time.Minute*3/2)
}

// runSupervision checks running Tickets until the daemon stops.
func (s *Service) runSupervision(ctx context.Context) {
	ticker := time.NewTicker(supervisionInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if err := s.supervise(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("super agent supervision pass failed", "err", err)
		}
	}
}

// supervise derives each running Ticket's signals, records the ones that
// began or ended, and wakes the brains of agents in act mode.
func (s *Service) supervise(ctx context.Context) error {
	running, err := s.store.ListSuperAgentTicketsByStatus(ctx, core.SuperAgentTicketRunning)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	agents := map[string]*core.SuperAgent{}
	watched := map[string]bool{}
	waiting := map[string][]core.SuperAgentTicket{}
	var order []string
	for _, ticket := range running {
		agent, loaded := agents[ticket.SuperAgentID]
		if !loaded {
			if agent, err = s.store.GetSuperAgent(ctx, ticket.SuperAgentID); err != nil {
				return err
			}
			agents[ticket.SuperAgentID] = agent
		}
		if agent == nil || !agent.Enabled || supervisionMode(*agent) == core.SuperAgentSupervisionOff {
			continue
		}
		watched[ticket.ID] = true
		holding, suspected := s.ticketConditions(ctx, ticket, now)
		updated, err := s.syncSignals(ctx, ticket.ID, holding, suspected, now)
		if err != nil {
			s.log.Warn("record super agent ticket signals", "ticket", ticket.ID, "err", err)
			continue
		}
		if updated == nil || supervisionMode(*agent) != core.SuperAgentSupervisionAct || len(undelivered(*updated)) == 0 {
			continue
		}
		key := agent.ID + "|" + updated.ThreadKey
		if _, ok := waiting[key]; !ok {
			order = append(order, key)
		}
		waiting[key] = append(waiting[key], *updated)
	}
	s.mu.Lock()
	for key := range s.suspects {
		if ticketID, _, _ := strings.Cut(key, "|"); !watched[ticketID] {
			delete(s.suspects, key)
		}
	}
	s.mu.Unlock()
	for _, key := range order {
		tickets := waiting[key]
		if err := s.wakeForSignals(ctx, *agents[tickets[0].SuperAgentID], tickets, now); err != nil && ctx.Err() == nil {
			s.log.Error("wake super agent router with signals", "agent", tickets[0].SuperAgentID, "err", err)
		}
	}
	return nil
}

// ticketConditions are the signals that hold for a running Ticket now, and
// the kinds only suspected: a condition first seen less than orphanGrace ago,
// such as one a daemon that just started has not watched long enough.
func (s *Service) ticketConditions(ctx context.Context, ticket core.SuperAgentTicket, now time.Time) ([]core.SuperAgentTicketSignal, []string) {
	s.mu.Lock()
	_, live := s.running[ticket.SuperAgentID+"|"+ticket.ID]
	started := s.turnStarts[ticket.ID]
	latest := s.activities[ticket.ID]
	s.mu.Unlock()
	var out []core.SuperAgentTicketSignal
	var suspected []string
	if !live {
		if since := s.suspect(ticket.ID+"|idle", now, true); now.Sub(since) >= orphanGrace {
			out = append(out, core.SuperAgentTicketSignal{
				Kind: core.SuperAgentSignalOrphaned, Since: since,
				Detail: "marked running, but nothing on this machine is running it",
			})
		} else {
			suspected = append(suspected, core.SuperAgentSignalOrphaned)
		}
		return out, suspected
	}
	s.suspect(ticket.ID+"|idle", now, false)
	if ticket.Worker.HostID != "" {
		statusCtx, cancel := context.WithTimeout(ctx, statusCheckTimeout)
		status := s.workers.Status(statusCtx, ticket.Worker)
		cancel()
		key := ticket.ID + "|unreachable|" + ticket.Worker.HostID
		if since := s.suspect(key, now, !status.Available); !status.Available && now.Sub(since) >= orphanGrace {
			detail := "its worker's machine cannot be reached"
			if status.Detail != "" {
				detail += ": " + oneLine(status.Detail, 200)
			}
			out = append(out, core.SuperAgentTicketSignal{Kind: core.SuperAgentSignalOrphaned, Since: since, Detail: detail})
		} else if !status.Available {
			suspected = append(suspected, core.SuperAgentSignalOrphaned)
		}
	}
	progress := started
	if latest.at.After(progress) {
		progress = latest.at
	}
	if progress.IsZero() {
		progress = ticket.UpdatedAt
	}
	limit := stallLimit(ticket.ExpectMinutes)
	if now.Sub(progress) >= limit {
		detail := fmt.Sprintf("no progress from its worker since %s, longer than the %s limit", progress.UTC().Format("15:04 UTC"), shortDuration(limit))
		if ticket.ExpectMinutes > 0 {
			detail += fmt.Sprintf(" (you expected about %dm)", ticket.ExpectMinutes)
		}
		if !latest.at.IsZero() && latest.text != "" {
			detail += "; its last step: " + oneLine(latest.text, 160)
		}
		out = append(out, core.SuperAgentTicketSignal{Kind: core.SuperAgentSignalStalled, Since: progress.UTC(), Detail: detail})
	}
	return out, suspected
}

// suspect returns when a condition was first seen holding without a break,
// starting it now when it is new. A condition that no longer holds is
// forgotten.
func (s *Service) suspect(key string, now time.Time, holds bool) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !holds {
		delete(s.suspects, key)
		return now
	}
	since, ok := s.suspects[key]
	if !ok {
		since = now
		s.suspects[key] = since
	}
	return since
}

// syncSignals records which of a Ticket's signals began and which ended. A
// condition is the same signal as an open one of its kind, or as a closed one
// that began at the same time, which a retry of the same turn reopens, so
// neither a restart nor a short break reports it again. A kind only suspected
// keeps its open signal open. It returns the Ticket as saved, or nil when it
// is no longer running.
func (s *Service) syncSignals(ctx context.Context, ticketID string, current []core.SuperAgentTicketSignal, suspected []string, now time.Time) (*core.SuperAgentTicket, error) {
	var saved *core.SuperAgentTicket
	err := s.quietlyMutateTicket(ctx, ticketID, func(ticket *core.SuperAgentTicket) bool {
		if ticket.Status != core.SuperAgentTicketRunning {
			return false
		}
		saved = ticket
		changed := false
		for index := range ticket.Signals {
			signal := &ticket.Signals[index]
			if signal.ClearedAt.IsZero() && !slices.Contains(suspected, signal.Kind) && !slices.ContainsFunc(current, func(c core.SuperAgentTicketSignal) bool { return c.Kind == signal.Kind }) {
				signal.ClearedAt, changed = now, true
			}
		}
		for _, condition := range current {
			if slices.ContainsFunc(ticket.Signals, func(signal core.SuperAgentTicketSignal) bool {
				return signal.Kind == condition.Kind && signal.ClearedAt.IsZero()
			}) {
				continue
			}
			if index := slices.IndexFunc(ticket.Signals, func(signal core.SuperAgentTicketSignal) bool {
				return signal.Kind == condition.Kind && signal.Since.Equal(condition.Since)
			}); index >= 0 {
				ticket.Signals[index].ClearedAt, changed = time.Time{}, true
				continue
			}
			condition.At = now
			ticket.Signals = append(ticket.Signals, condition)
			changed = true
			s.log.Info("super agent ticket signal", "ticket", ticket.ID, "kind", condition.Kind, "detail", condition.Detail)
		}
		ticket.Signals = trimSignals(ticket.Signals)
		return changed
	})
	return saved, err
}

// trimSignals keeps the latest signals, dropping the oldest closed ones first.
func trimSignals(signals []core.SuperAgentTicketSignal) []core.SuperAgentTicketSignal {
	for len(signals) > maxTicketSignals {
		index := slices.IndexFunc(signals, func(signal core.SuperAgentTicketSignal) bool { return !signal.ClearedAt.IsZero() })
		if index < 0 {
			index = 0
		}
		signals = slices.Delete(signals, index, index+1)
	}
	return signals
}

// closeSignals ends a Ticket's open signals. Every signal is about a running
// Ticket, so whatever stops it running ends them.
func closeSignals(ticket *core.SuperAgentTicket, now time.Time) {
	if ticket.Status == core.SuperAgentTicketRunning {
		return
	}
	for index := range ticket.Signals {
		if ticket.Signals[index].ClearedAt.IsZero() {
			ticket.Signals[index].ClearedAt = now
		}
	}
}

func openSignals(ticket core.SuperAgentTicket) []core.SuperAgentTicketSignal {
	var out []core.SuperAgentTicketSignal
	for _, signal := range ticket.Signals {
		if signal.ClearedAt.IsZero() {
			out = append(out, signal)
		}
	}
	return out
}

func undelivered(ticket core.SuperAgentTicket) []core.SuperAgentTicketSignal {
	return slices.DeleteFunc(openSignals(ticket), func(signal core.SuperAgentTicketSignal) bool { return !signal.WokeAt.IsZero() })
}

// wakeForSignals tells the brain, in one event on the thread, every signal
// on that thread it has not been told about, unless it was woken there for
// signals less than supervisionCooldown ago. The event's dedupe key names the
// signals, so a daemon that stops between storing it and marking them sent
// stores no second event.
func (s *Service) wakeForSignals(ctx context.Context, agent core.SuperAgent, tickets []core.SuperAgentTicket, now time.Time) error {
	thread, err := s.store.ListSuperAgentThreadTickets(ctx, agent.ID, tickets[0].ThreadKey)
	if err != nil {
		return err
	}
	var last time.Time
	for _, ticket := range thread {
		for _, signal := range ticket.Signals {
			if signal.WokeAt.After(last) {
				last = signal.WokeAt
			}
		}
	}
	if !last.IsZero() && now.Sub(last) < supervisionCooldown {
		return nil
	}
	var text strings.Builder
	text.WriteString("AgentMux noticed this from its own records; no model looked at the work:\n")
	var keys []string
	for _, ticket := range tickets {
		for _, signal := range undelivered(ticket) {
			fmt.Fprintf(&text, "- Ticket %s %q is %s: %s.\n", ticket.ID, ticket.Title, signal.Kind, signal.Detail)
			keys = append(keys, ticket.ID+"/"+signal.Kind+"/"+signal.Since.UTC().Format(time.RFC3339Nano))
		}
	}
	text.WriteString("\nDecide what to do: ask its worker how it stands with attach, stop it and accept the work again, tell the people waiting, or do nothing. You are not told about the same condition again while it lasts.\n")
	slices.Sort(keys)
	digest := sha256.Sum256([]byte(strings.Join(keys, ",")))
	first := tickets[0]
	if err := s.insertAgentMuxEvent(ctx, agent, first, core.SuperAgentTriggerSupervision, "Supervision: "+first.Title, text.String(), first.Reply, fmt.Sprintf("supervision:%x", digest[:12])); err != nil {
		return err
	}
	for _, ticket := range tickets {
		if err := s.quietlyMutateTicket(ctx, ticket.ID, func(current *core.SuperAgentTicket) bool {
			changed := false
			for index := range current.Signals {
				signal := &current.Signals[index]
				if signal.ClearedAt.IsZero() && signal.WokeAt.IsZero() {
					signal.WokeAt, changed = now, true
				}
			}
			return changed
		}); err != nil {
			s.log.Warn("mark super agent ticket signals sent", "ticket", ticket.ID, "err", err)
		}
	}
	return nil
}

// quietlyMutateTicket changes what supervision records about a Ticket without
// moving its updated time, which says when its work last changed.
func (s *Service) quietlyMutateTicket(ctx context.Context, id string, mutate func(*core.SuperAgentTicket) bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ticket, err := s.store.GetSuperAgentTicket(ctx, id)
	if err != nil || ticket == nil {
		return err
	}
	if !mutate(ticket) {
		return nil
	}
	if err := s.store.UpsertSuperAgentTicket(ctx, *ticket); err != nil {
		return err
	}
	s.recordTicket(ctx, *ticket)
	return nil
}

// signalLines are a Ticket's open signals as the brain is shown them.
func signalLines(ticket core.SuperAgentTicket) []string {
	var out []string
	for _, signal := range openSignals(ticket) {
		out = append(out, signal.Kind+": "+signal.Detail)
	}
	return out
}

func shortDuration(d time.Duration) string {
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	if d >= time.Hour {
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}
