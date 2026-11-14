package superagent

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wangning19940904/AgentMux/core"
)

// A timer lets an agent act because time passed. The router sets a follow-up
// for itself with an action; when it is due, AgentMux stores a Trigger on the
// thread it was set on, and the router's turn runs like any other. Nothing
// starts from a timer directly, so a follow-up is ordered, retried and
// recorded like every other event.

// A follow-up is set by a turn an allowed sender started, so it needs no gate
// of its own. These limits keep a confused or crafted turn from setting up a
// loop that spends tokens with nobody watching.
const (
	maxThreadFollowUps = 5
	maxAgentFollowUps  = 50
	minFollowUpDelay   = time.Minute
	maxFollowUpDelay   = 90 * 24 * time.Hour
	maxTimerNoteRunes  = 2000
	// A timer event may set another follow-up, which is how "check every hour
	// until it merges" works. This budget is what bounds such a chain.
	maxTimerEventsPerDay = 200
	timerBudgetWindow    = 24 * time.Hour
	// dueTimerBatch is far above what the per-agent limit lets pile up, so
	// the timers of a disabled agent, which wait, never crowd out the rest.
	dueTimerBatch = 1000
	// lateTimerGrace is how late a timer may fire before its event says so.
	lateTimerGrace      = 2 * time.Minute
	finishedTimerWindow = 72 * time.Hour
	maxFinishedTimers   = 50
	maxTimerNoteShown   = 400
)

// timerView is one active timer as the router is shown it.
type timerView struct {
	ID       string `json:"timer_id"`
	Due      string `json:"due"`
	Note     string `json:"note"`
	TicketID string `json:"ticket_id,omitempty"`
}

var leadingDays = regexp.MustCompile(`^(\d{1,4})d`)

// parseDelay reads "30m", "18h", "3d" or a mix such as "1d12h".
func parseDelay(text string) (time.Duration, error) {
	text = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(text), " ", ""))
	var total time.Duration
	if match := leadingDays.FindStringSubmatch(text); match != nil {
		days, err := strconv.Atoi(match[1])
		if err != nil {
			return 0, err
		}
		total = time.Duration(days) * 24 * time.Hour
		text = text[len(match[0]):]
	}
	if text != "" {
		rest, err := time.ParseDuration(text)
		if err != nil {
			return 0, err
		}
		total += rest
	}
	if total <= 0 {
		return 0, errors.New("not a delay")
	}
	return total, nil
}

// parseFollowUpTime reads a time with an offset, or one without in the
// daemon's zone, which is the zone the router is shown the time in.
func parseFollowUpTime(text string, zone *time.Location) (time.Time, error) {
	if at, err := time.Parse(time.RFC3339, text); err == nil {
		return at, nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if at, err := time.ParseInLocation(layout, text, zone); err == nil {
			return at, nil
		}
	}
	return time.Time{}, errors.New("not a time")
}

// followUpDue is when a follow_up action asks to be woken, checked against
// the shortest and longest delay.
func followUpDue(action routerAction, now time.Time) (time.Time, error) {
	in, at := strings.TrimSpace(action.In), strings.TrimSpace(action.At)
	var due time.Time
	switch {
	case (in == "") == (at == ""):
		return time.Time{}, errors.New(`a follow_up action needs exactly one of "in" and "at"`)
	case in != "":
		delay, err := parseDelay(in)
		if err != nil {
			return time.Time{}, fmt.Errorf(`a follow_up action's "in" is %q; write minutes, hours or days, such as "30m", "18h" or "3d"`, in)
		}
		due = now.Add(delay)
	default:
		parsed, err := parseFollowUpTime(at, now.Location())
		if err != nil {
			return time.Time{}, fmt.Errorf(`a follow_up action's "at" is %q; write a time such as %s`, at, now.Add(time.Hour).Format(time.RFC3339))
		}
		due = parsed
	}
	switch delay := due.Sub(now); {
	case delay < minFollowUpDelay:
		return time.Time{}, fmt.Errorf("a follow-up must be at least a minute from now, and it is now %s", now.Format(time.RFC3339))
	case delay > maxFollowUpDelay:
		return time.Time{}, fmt.Errorf("a follow-up can be at most %d days from now", int(maxFollowUpDelay.Hours()/24))
	}
	return due.UTC().Truncate(time.Second), nil
}

// timerLedger counts a thread's and an agent's active follow-ups while the
// actions of one decision are checked in order, so "cancel one, set another"
// in the same block is judged on what would be left.
type timerLedger struct {
	onThread  map[string]bool
	thread    int
	agent     int
	cancelled map[string]bool
}

func (s *Service) timerLedger(ctx context.Context, agentID, threadKey string) (*timerLedger, error) {
	active, err := s.store.ListActiveSuperAgentTimers(ctx, agentID)
	if err != nil {
		return nil, err
	}
	ledger := &timerLedger{onThread: map[string]bool{}, agent: len(active), cancelled: map[string]bool{}}
	for _, timer := range active {
		if timer.ThreadKey == threadKey {
			ledger.onThread[timer.ID] = true
			ledger.thread++
		}
	}
	return ledger, nil
}

func (l *timerLedger) add() error {
	if l.thread >= maxThreadFollowUps {
		return fmt.Errorf("this thread already has %d follow-ups set, the most allowed; cancel one you no longer need first", maxThreadFollowUps)
	}
	if l.agent >= maxAgentFollowUps {
		return fmt.Errorf("this Super Agent already has %d follow-ups set, the most allowed", maxAgentFollowUps)
	}
	l.thread++
	l.agent++
	return nil
}

func (l *timerLedger) cancel(id string) {
	if l.onThread[id] && !l.cancelled[id] {
		l.cancelled[id] = true
		l.thread--
		l.agent--
	}
}

// threadTimer is a timer the router may cancel from an event: one of this
// agent's, set on the event's thread. A timer that already fired or was
// cancelled is returned too, since cancelling it again changes nothing.
func (s *Service) threadTimer(ctx context.Context, agentID, threadKey, id string) (*core.SuperAgentTimer, error) {
	timer, err := s.store.GetSuperAgentTimer(ctx, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	if timer == nil || timer.SuperAgentID != agentID || timer.ThreadKey != threadKey {
		return nil, fmt.Errorf("there is no timer %s on this thread; TIMERS ON THIS THREAD lists the ones you can cancel", id)
	}
	return timer, nil
}

// setFollowUp stores a follow-up for the thread of the event that asked for
// it. The limits are checked again here, under the lock, since several
// events of one agent can be evaluated at once.
func (s *Service) setFollowUp(ctx context.Context, agent core.SuperAgent, trigger core.SuperAgentTrigger, action routerAction, ticketID string) (core.SuperAgentTimer, error) {
	s.timersMu.Lock()
	defer s.timersMu.Unlock()
	now := s.now()
	due, err := followUpDue(action, now)
	if err != nil {
		return core.SuperAgentTimer{}, err
	}
	ledger, err := s.timerLedger(ctx, agent.ID, trigger.ThreadKey)
	if err != nil {
		return core.SuperAgentTimer{}, err
	}
	if err := ledger.add(); err != nil {
		return core.SuperAgentTimer{}, err
	}
	timer := core.SuperAgentTimer{
		ID: "stm_" + uuid.NewString(), SuperAgentID: agent.ID, Kind: core.SuperAgentTimerFollowUp, Status: core.SuperAgentTimerActive,
		ThreadKey: trigger.ThreadKey, SourceID: trigger.SourceID, SourceKind: trigger.SourceKind, Reply: trigger.Reply,
		TicketID: strings.TrimSpace(ticketID), Note: truncateRunes(strings.TrimSpace(action.Note), maxTimerNoteRunes),
		DueAt: due, SetBy: trigger.ID, CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	if err := s.store.UpsertSuperAgentTimer(ctx, timer); err != nil {
		return core.SuperAgentTimer{}, err
	}
	return timer, nil
}

// cancelTimer withdraws an active timer. One that already fired or was
// cancelled is left as it is.
func (s *Service) cancelTimer(ctx context.Context, id string) (bool, error) {
	s.timersMu.Lock()
	defer s.timersMu.Unlock()
	timer, err := s.store.GetSuperAgentTimer(ctx, id)
	if err != nil {
		return false, err
	}
	if timer == nil {
		return false, ErrNotFound
	}
	if timer.Status != core.SuperAgentTimerActive {
		return false, nil
	}
	timer.Status = core.SuperAgentTimerCancelled
	timer.UpdatedAt = s.now().UTC()
	return true, s.store.UpsertSuperAgentTimer(ctx, *timer)
}

// CancelTimer is the Console's way to withdraw a timer an agent set.
func (s *Service) CancelTimer(ctx context.Context, id string) error {
	cancelled, err := s.cancelTimer(ctx, strings.TrimSpace(id))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("timer not found: %w", ErrNotFound)
		}
		return err
	}
	if !cancelled {
		return conflictError("the timer already fired or was cancelled")
	}
	return nil
}

// cancelTicketTimers withdraws the follow-ups about a Ticket that was
// stopped: there is nothing left to check on.
func (s *Service) cancelTicketTimers(ctx context.Context, ticket core.SuperAgentTicket) {
	active, err := s.store.ListActiveSuperAgentTimers(ctx, ticket.SuperAgentID)
	if err != nil {
		s.log.Warn("list timers of a stopped ticket", "ticket", ticket.ID, "err", err)
		return
	}
	for _, timer := range active {
		if timer.TicketID != ticket.ID {
			continue
		}
		if _, err := s.cancelTimer(ctx, timer.ID); err != nil {
			s.log.Warn("cancel timer of a stopped ticket", "timer", timer.ID, "err", err)
		}
	}
}

// fireDueTimers turns each due timer into a Trigger. The timers of a
// disabled agent, and of one that has spent its daily budget, stay due and
// fire late.
func (s *Service) fireDueTimers(ctx context.Context) error {
	now := s.now()
	due, err := s.store.ListDueSuperAgentTimers(ctx, now, dueTimerBatch)
	if err != nil || len(due) == 0 {
		return err
	}
	agents := map[string]*core.SuperAgent{}
	budget := map[string]int{}
	for _, timer := range due {
		agent, seen := agents[timer.SuperAgentID]
		if !seen {
			if agent, err = s.store.GetSuperAgent(ctx, timer.SuperAgentID); err != nil {
				return err
			}
			agents[timer.SuperAgentID] = agent
			if agent != nil && agent.Enabled {
				fired, err := s.store.CountSuperAgentTimersFired(ctx, agent.ID, now.Add(-timerBudgetWindow))
				if err != nil {
					return err
				}
				budget[agent.ID] = maxTimerEventsPerDay - fired
			}
		}
		if agent == nil {
			// Deleting an agent deletes its timers; this one was set while
			// that happened.
			if _, err := s.cancelTimer(ctx, timer.ID); err != nil {
				return err
			}
			continue
		}
		if !agent.Enabled || budget[agent.ID] <= 0 {
			continue
		}
		fired, err := s.fireTimer(ctx, timer.ID, now)
		if err != nil {
			return err
		}
		if fired {
			budget[agent.ID]--
		}
	}
	return nil
}

// fireTimer stores a timer's Trigger, then marks the timer done. The dedupe
// key is the timer and its due time, so a daemon that stops between the two
// writes stores no second Trigger when it tries again.
func (s *Service) fireTimer(ctx context.Context, id string, now time.Time) (bool, error) {
	s.timersMu.Lock()
	defer s.timersMu.Unlock()
	timer, err := s.store.GetSuperAgentTimer(ctx, id)
	if err != nil {
		return false, err
	}
	if timer == nil || timer.Status != core.SuperAgentTimerActive {
		return false, nil // Cancelled since the list was read.
	}
	var text strings.Builder
	late := ""
	if now.Sub(timer.DueAt) > lateTimerGrace {
		late = ", " + strings.TrimSuffix(ago(now, timer.DueAt), " ago") + " late"
	}
	fmt.Fprintf(&text, "A follow-up you set %s is due (due %s%s).\nYour note: %s\n",
		ago(now, timer.CreatedAt), timer.DueAt.In(now.Location()).Format(time.RFC3339), late, timer.Note)
	if timer.TicketID != "" {
		if ticket, err := s.store.GetSuperAgentTicket(ctx, timer.TicketID); err == nil && ticket != nil {
			fmt.Fprintf(&text, "Ticket %s %q is %s.\n", ticket.ID, ticket.Title, ticket.Status)
		}
	}
	at := now.UTC()
	trigger := core.SuperAgentTrigger{
		ID: "sat_" + uuid.NewString(), SuperAgentID: timer.SuperAgentID, SourceID: timer.SourceID, SourceKind: timer.SourceKind,
		DedupeKey: fmt.Sprintf("timer:%s:%d", timer.ID, timer.DueAt.Unix()), ThreadKey: timer.ThreadKey,
		Actor: "agentmux", ActorName: "AgentMux", Addressed: true, Kind: core.SuperAgentTriggerTimer,
		TicketID: timer.TicketID, Title: "Follow-up due", Text: text.String(), Reply: timer.Reply,
		Disposition: core.SuperAgentDispositionPending, ReceivedAt: at, UpdatedAt: at,
	}
	if _, err := s.store.InsertSuperAgentTrigger(ctx, trigger); err != nil {
		return false, err
	}
	timer.Status = core.SuperAgentTimerDone
	timer.FiredAt, timer.UpdatedAt = at, at
	if err := s.store.UpsertSuperAgentTimer(ctx, *timer); err != nil {
		return false, err
	}
	return true, nil
}

// timerViews are a thread's active timers as the router is shown them.
func (s *Service) timerViews(ctx context.Context, agentID, threadKey string) []timerView {
	active, err := s.store.ListActiveSuperAgentTimers(ctx, agentID)
	if err != nil {
		s.log.Warn("list super agent timers", "agent", agentID, "err", err)
		return nil
	}
	zone := s.now().Location()
	var views []timerView
	for _, timer := range active {
		if timer.ThreadKey != threadKey {
			continue
		}
		views = append(views, timerView{
			ID: timer.ID, Due: timer.DueAt.In(zone).Format(time.RFC3339),
			Note: oneLine(timer.Note, maxTimerNoteShown), TicketID: timer.TicketID,
		})
	}
	return views
}

// activityTimers are an agent's timers for the Console: those still set, then
// those that fired or were cancelled lately. The warning says when the daily
// budget is holding due timers back.
func (s *Service) activityTimers(ctx context.Context, agentID string) ([]core.SuperAgentTimer, string, error) {
	now := s.now()
	timers, err := s.store.ListActiveSuperAgentTimers(ctx, agentID)
	if err != nil {
		return nil, "", err
	}
	waiting := 0
	for _, timer := range timers {
		if !timer.DueAt.After(now) {
			waiting++
		}
	}
	warning := ""
	if waiting > 0 {
		fired, err := s.store.CountSuperAgentTimersFired(ctx, agentID, now.Add(-timerBudgetWindow))
		if err != nil {
			return nil, "", err
		}
		if fired >= maxTimerEventsPerDay {
			warning = fmt.Sprintf("This agent's timers fired %d times in the last 24 hours, the most allowed. %d more are due and wait.", fired, waiting)
		}
	}
	finished, err := s.store.ListFinishedSuperAgentTimers(ctx, agentID, now.Add(-finishedTimerWindow), maxFinishedTimers)
	if err != nil {
		return nil, "", err
	}
	return append(timers, finished...), warning, nil
}
