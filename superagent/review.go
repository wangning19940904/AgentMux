package superagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/wangning19940904/AgentMux/core"
)

// A review is the brain's verdict on a Ticket after its worker finished a
// turn: accepted, sent back for rework with the gaps, or waiting for a
// person's decision. The host records every review on the Ticket and bounds
// rework, so a Ticket that keeps falling short reaches a person instead of
// going round forever.

const (
	// maxReworkRounds is how many times a Ticket may be sent back. The next
	// rework is refused and the people on the thread are told it needs a
	// decision.
	maxReworkRounds    = 2
	maxReviewChecks    = 12
	maxEvidenceRunes   = 1000
	maxReviewNoteRunes = 2000
)

// checkReview refuses a review that says nothing AgentMux can carry out. It
// runs when the decision is read, before anything happens.
func checkReview(action routerAction) error {
	if strings.TrimSpace(action.TicketID) == "" {
		return errors.New("a review action has no ticket_id")
	}
	if len(action.Checks) > maxReviewChecks {
		return fmt.Errorf("a review has %d checks; give at most %d", len(action.Checks), maxReviewChecks)
	}
	unmet := 0
	for index, check := range action.Checks {
		criterion := strings.TrimSpace(check.Criterion)
		if criterion == "" {
			return fmt.Errorf("review check %d names no criterion", index+1)
		}
		if utf8.RuneCountInString(criterion) > maxCriterionRunes {
			return fmt.Errorf("review check %d has a criterion longer than %d characters", index+1, maxCriterionRunes)
		}
		if !check.Met {
			unmet++
		}
	}
	note := strings.TrimSpace(action.Note)
	switch reviewVerdict(action) {
	case core.SuperAgentReviewAccepted:
		if unmet > 0 {
			return errors.New(`a review that leaves a criterion unmet cannot be "accepted"; send it back with "rework" or ask with "needs_user"`)
		}
	case core.SuperAgentReviewRework:
		if unmet == 0 && note == "" {
			return errors.New(`a "rework" review needs the gaps: a check that is not met, or a note saying what to fix`)
		}
	case core.SuperAgentReviewNeedsUser:
		if note == "" {
			return errors.New(`a "needs_user" review needs a note saying what must be decided and by whom`)
		}
	default:
		return fmt.Errorf(`a review's verdict must be "accepted", "rework" or "needs_user", not %q`, action.Verdict)
	}
	return nil
}

func reviewVerdict(action routerAction) string {
	return strings.ToLower(strings.TrimSpace(action.Verdict))
}

// preflightReview checks a review against the host's records: the Ticket is
// this agent's, on the event's thread, and its worker has finished a turn.
// Each Ticket takes one review per decision.
func (s *Service) preflightReview(ctx context.Context, agent core.SuperAgent, trigger core.SuperAgentTrigger, action routerAction, reviewed map[string]bool) error {
	id := strings.TrimSpace(action.TicketID)
	ticket, err := s.agentTicket(ctx, agent.ID, id)
	if err != nil {
		return err
	}
	if ticket.ThreadKey != trigger.ThreadKey {
		return fmt.Errorf("ticket %s is not on this event's thread; review a ticket only on its own thread, when its update arrives", id)
	}
	if reviewed[id] {
		return fmt.Errorf("ticket %s is reviewed twice in one answer", id)
	}
	reviewed[id] = true
	verdict := reviewVerdict(action)
	switch {
	case ticket.Status == core.SuperAgentTicketReady:
	case ticket.Status == core.SuperAgentTicketFailed && verdict == core.SuperAgentReviewNeedsUser:
	default:
		return fmt.Errorf("ticket %s is %s; review it once its worker has finished a turn", id, ticket.Status)
	}
	return nil
}

// reviewOutcome is what a review did besides its record.
type reviewOutcome struct {
	// notice is for the people on the thread, in AgentMux's words.
	notice string
	// refused says why a rework was not sent.
	refused string
	rework  bool
}

// review records a checked review and carries it out. "accepted" leaves the
// Ticket ready. "rework" sends the gaps to the same worker conversation and
// counts a round, unless the rounds are used up. "needs_user" tells the
// people on the thread what must be decided.
func (s *Service) review(ctx context.Context, trigger core.SuperAgentTrigger, ticketID string, action routerAction) (reviewOutcome, error) {
	record := core.SuperAgentTicketReview{
		Verdict: reviewVerdict(action), Note: truncateRunes(strings.TrimSpace(action.Note), maxReviewNoteRunes),
		TriggerID: trigger.ID, At: s.now().UTC(),
	}
	for _, check := range action.Checks {
		record.Checks = append(record.Checks, core.SuperAgentReviewCheck{
			Criterion: strings.TrimSpace(check.Criterion), Met: check.Met,
			Evidence: truncateRunes(strings.TrimSpace(check.Evidence), maxEvidenceRunes),
		})
	}
	var outcome reviewOutcome
	err := s.mutateTicket(ctx, ticketID, func(ticket *core.SuperAgentTicket) error {
		outcome = reviewOutcome{}
		if ticket.ThreadKey != trigger.ThreadKey {
			return fmt.Errorf("ticket %s is not on this event's thread", ticket.ID)
		}
		switch record.Verdict {
		case core.SuperAgentReviewAccepted, core.SuperAgentReviewRework:
			if ticket.Status != core.SuperAgentTicketReady {
				return conflictError(fmt.Sprintf("ticket %s is %s and can no longer be reviewed", ticket.ID, ticket.Status))
			}
		}
		switch record.Verdict {
		case core.SuperAgentReviewRework:
			if ticket.ReworkRounds >= maxReworkRounds {
				record.Refused = fmt.Sprintf("AgentMux sends a ticket back at most %d times", maxReworkRounds)
				outcome.refused = record.Refused
				outcome.notice = fmt.Sprintf("“%s” was sent back %d times and still falls short, so it needs a decision from you.%s", ticket.Title, ticket.ReworkRounds, gapsText(record, " Still open: "))
				break
			}
			ticket.ReworkRounds++
			record.Round = ticket.ReworkRounds
			ticket.Inputs = append(ticket.Inputs, reworkInput(record))
			ticket.Status = core.SuperAgentTicketQueued
			ticket.Attempts = 0
			ticket.Error = ""
			outcome.rework = true
		case core.SuperAgentReviewNeedsUser:
			outcome.notice = fmt.Sprintf("“%s” needs a decision from you: %s", ticket.Title, record.Note)
		}
		ticket.Reviews = append(ticket.Reviews, record)
		return nil
	})
	return outcome, err
}

// reworkInput tells the worker what its last turn left undone.
func reworkInput(record core.SuperAgentTicketReview) string {
	var text strings.Builder
	fmt.Fprintf(&text, "REWORK (round %d of %d): the router checked your last report and sent this ticket back. Close these gaps, then report again with evidence for each criterion.\n", record.Round, maxReworkRounds)
	text.WriteString(gapsText(record, "\n"))
	return text.String()
}

// gapsText lists a review's unmet checks and its note.
func gapsText(record core.SuperAgentTicketReview, lead string) string {
	var lines []string
	for _, check := range record.Checks {
		if check.Met {
			continue
		}
		line := "- Not met: " + check.Criterion
		if check.Evidence != "" {
			line += " (" + check.Evidence + ")"
		}
		lines = append(lines, line)
	}
	if record.Note != "" {
		lines = append(lines, "- "+record.Note)
	}
	if len(lines) == 0 {
		return ""
	}
	return lead + strings.Join(lines, "\n")
}

// reviewSummary is a Ticket's latest review as the brain is shown it.
func reviewSummary(ticket core.SuperAgentTicket) string {
	if len(ticket.Reviews) == 0 {
		return ""
	}
	latest := ticket.Reviews[len(ticket.Reviews)-1]
	summary := latest.Verdict
	switch {
	case latest.Refused != "":
		summary = "rework refused: " + latest.Refused + "; it needs a person's decision"
	case latest.Verdict == core.SuperAgentReviewRework:
		summary = fmt.Sprintf("rework round %d of %d", latest.Round, maxReworkRounds)
	}
	return summary
}
