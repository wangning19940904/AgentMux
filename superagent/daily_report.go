package superagent

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/framework/health"
)

// A daily report is the brain's short account of a day: what its work came
// to, what it learned, and what needs a person. AgentMux gathers the facts
// from its own records once a day, at the agent's report time, and stores
// them as an event on the agent's report thread. The brain writes the report
// from them like any other answer, and the answer goes to the report's
// place. If the brain cannot write it, the facts are sent as they are.
//
// When the nightly memory pass (docs/architecture/agent-memory-loop.md)
// exists, the rules and lessons it learned are one more fact here, and the
// report time should come after it.

const (
	reportSettingKey = "super_agent_daily_report:"
	defaultReportAt  = "09:00"
	// reportScanEvery is how often the agents' report times are checked.
	reportScanEvery = time.Minute
	// A report that is due while the daemon is down is sent late, but not
	// once it is this late: the next day's report covers the time instead.
	maxReportLateness = 6 * time.Hour
	// A report never looks back further than this, however long ago the
	// previous one was.
	maxReportWindow = 7 * 24 * time.Hour
	// reportFactsMarker starts the facts in a report event. If the brain
	// cannot write the report, what follows it is sent instead.
	reportFactsMarker = "FACTS FROM AGENTMUX"
	maxReportItems    = 10
	maxReportLine     = 240
	reportTriggerList = 1000
)

// reportState is what AgentMux remembers about an agent's reports: the last
// scheduled time it handled, and the end of the time the last report covered.
type reportState struct {
	LastDue time.Time `json:"last_due,omitzero"`
	Through time.Time `json:"through,omitzero"`
}

// normalizeDailyReport checks an agent's report settings. A report that is
// not enabled keeps its settings, so turning it back on restores them.
func normalizeDailyReport(agent *core.SuperAgent) error {
	report := agent.DailyReport
	if report == nil {
		return nil
	}
	report.At = strings.TrimSpace(report.At)
	report.Timezone = strings.TrimSpace(report.Timezone)
	report.To = strings.TrimSpace(report.To)
	if report.At != "" {
		if _, _, err := parseReportAt(report.At); err != nil {
			return err
		}
	}
	if report.Timezone != "" {
		if _, err := time.LoadLocation(report.Timezone); err != nil {
			return fmt.Errorf("daily_report.timezone %q is not a time zone such as Asia/Shanghai", report.Timezone)
		}
	}
	if report.To != "" && !validReportPlace(report.To) {
		return fmt.Errorf("daily_report.to %q must be chat:<id>, person:<login>, agent:<id> or github:<owner/repo>#<number>", report.To)
	}
	if !report.Enabled && report.At == "" && report.Timezone == "" && report.To == "" && !report.Always {
		agent.DailyReport = nil
	}
	return nil
}

func validReportPlace(to string) bool {
	for _, prefix := range []string{"chat:", core.MailPartyPerson + ":", core.MailPartyAgent + ":", "github:"} {
		if rest, ok := strings.CutPrefix(to, prefix); ok && strings.TrimSpace(rest) != "" {
			return true
		}
	}
	return false
}

// parseReportAt reads "HH:MM".
func parseReportAt(at string) (int, int, error) {
	hours, minutes, found := strings.Cut(at, ":")
	h, errH := strconv.Atoi(hours)
	m, errM := strconv.Atoi(minutes)
	if !found || errH != nil || errM != nil || h < 0 || h > 23 || m < 0 || m > 59 || len(minutes) != 2 {
		return 0, 0, fmt.Errorf("daily_report.at %q must be a time of day such as 09:00", at)
	}
	return h, m, nil
}

// reportZone is the zone an agent's report time is read in.
func (s *Service) reportZone(report core.SuperAgentDailyReport) *time.Location {
	if report.Timezone != "" {
		if zone, err := time.LoadLocation(report.Timezone); err == nil {
			return zone
		}
	}
	return s.now().Location()
}

// lastReportDue is the latest scheduled report time at or before now.
func lastReportDue(now time.Time, at string, zone *time.Location) time.Time {
	if at == "" {
		at = defaultReportAt
	}
	h, m, err := parseReportAt(at)
	if err != nil {
		return time.Time{}
	}
	local := now.In(zone)
	due := time.Date(local.Year(), local.Month(), local.Day(), h, m, 0, 0, zone)
	if due.After(now) {
		due = time.Date(local.Year(), local.Month(), local.Day()-1, h, m, 0, 0, zone)
	}
	return due
}

// fireDailyReports stores the report event of every agent whose report time
// has come since the last one it handled.
func (s *Service) fireDailyReports(ctx context.Context) error {
	now := s.now()
	if now.Sub(s.reportsScanned) < reportScanEvery {
		return nil
	}
	s.reportsScanned = now
	agents, err := s.store.ListSuperAgents(ctx)
	if err != nil {
		return err
	}
	for _, agent := range agents {
		if !agent.Enabled || agent.DailyReport == nil || !agent.DailyReport.Enabled {
			continue
		}
		if err := s.fireDailyReport(ctx, agent, now); err != nil {
			s.log.Warn("send daily report", "super_agent", agent.ID, "err", err)
		}
	}
	return nil
}

func (s *Service) fireDailyReport(ctx context.Context, agent core.SuperAgent, now time.Time) error {
	s.reportsMu.Lock()
	defer s.reportsMu.Unlock()
	report := *agent.DailyReport
	due := lastReportDue(now, report.At, s.reportZone(report))
	if due.IsZero() {
		return nil
	}
	var state reportState
	if err := s.loadSetting(ctx, reportSettingKey+agent.ID, &state); err != nil {
		return err
	}
	if !due.After(state.LastDue) {
		return nil
	}
	if state.LastDue.IsZero() && !due.After(agent.UpdatedAt) {
		// Turned on after today's report time: the first report is the next one.
		state.LastDue = due
		return s.saveSetting(ctx, reportSettingKey+agent.ID, state)
	}
	if now.Sub(due) > maxReportLateness {
		// Missed while the daemon was down; the next report covers the time.
		state.LastDue = due
		return s.saveSetting(ctx, reportSettingKey+agent.ID, state)
	}
	from := reportFrom(state, due)
	facts := s.collectDailyFacts(ctx, agent, from, due)
	if facts.quiet() && !report.Always {
		state.LastDue = due
		return s.saveSetting(ctx, reportSettingKey+agent.ID, state)
	}
	if err := s.insertDailyReport(ctx, agent, report, facts, fmt.Sprintf("daily_report:%d", due.Unix())); err != nil {
		return err
	}
	state.LastDue, state.Through = due, due
	return s.saveSetting(ctx, reportSettingKey+agent.ID, state)
}

// RunDailyReport writes an agent's report now, covering the time since its
// last report. It is sent even when there is little to say, since a person
// asked for it, and the next scheduled report starts where it ends.
func (s *Service) RunDailyReport(ctx context.Context, agentID string) error {
	agent, err := s.store.GetSuperAgent(ctx, strings.TrimSpace(agentID))
	if err != nil {
		return err
	}
	if agent == nil {
		return ErrNotFound
	}
	if !agent.Enabled {
		return conflictError("the agent is disabled")
	}
	report := core.SuperAgentDailyReport{}
	if agent.DailyReport != nil {
		report = *agent.DailyReport
	}
	s.reportsMu.Lock()
	defer s.reportsMu.Unlock()
	now := s.now()
	var state reportState
	if err := s.loadSetting(ctx, reportSettingKey+agent.ID, &state); err != nil {
		return err
	}
	facts := s.collectDailyFacts(ctx, *agent, reportFrom(state, now), now)
	if err := s.insertDailyReport(ctx, *agent, report, facts, fmt.Sprintf("daily_report:now:%d", now.UnixNano())); err != nil {
		return err
	}
	state.Through = now
	return s.saveSetting(ctx, reportSettingKey+agent.ID, state)
}

// reportFrom is where a report ending at end starts: where the last one
// ended, or a day back for the first, and never more than a week back.
func reportFrom(state reportState, end time.Time) time.Time {
	from := state.Through
	if from.IsZero() || !from.Before(end) {
		from = end.Add(-24 * time.Hour)
	}
	if end.Sub(from) > maxReportWindow {
		from = end.Add(-maxReportWindow)
	}
	return from
}

func (s *Service) insertDailyReport(ctx context.Context, agent core.SuperAgent, report core.SuperAgentDailyReport, facts dailyFacts, dedupeKey string) error {
	var target destination
	if report.To != "" {
		resolved, err := s.resolveDestination(ctx, agent, report.To, nil)
		if err != nil {
			// The report is still written and kept in the activity, and it
			// says why it could not be sent.
			facts.needsPerson = append(facts.needsPerson, fmt.Sprintf("This report could not be sent to %s: %s. Fix the daily report's place in the agent's settings.", report.To, oneLine(err.Error(), maxReportLine)))
		} else {
			target = resolved
		}
	}
	now := s.now().UTC()
	trigger := core.SuperAgentTrigger{
		ID: "sat_" + uuid.NewString(), SuperAgentID: agent.ID, SourceID: target.sourceID,
		DedupeKey: dedupeKey, ThreadKey: core.SuperAgentDailyReportThread,
		Actor: "agentmux", ActorName: "AgentMux", Addressed: true, Kind: core.SuperAgentTriggerDailyReport,
		Title: "Daily report " + facts.to.In(facts.zone).Format("2006-01-02"), Text: truncateRunes(facts.render(), maxTriggerRunes),
		Reply: target.reply, Disposition: core.SuperAgentDispositionPending, ReceivedAt: now, UpdatedAt: now,
	}
	if source := s.agentSource(agent, target.sourceID); source != nil {
		trigger.SourceKind = source.Kind
	}
	if _, err := s.store.InsertSuperAgentTrigger(ctx, trigger); err != nil {
		return err
	}
	s.notify()
	return nil
}

func (s *Service) agentSource(agent core.SuperAgent, sourceID string) *core.SuperAgentSource {
	for index := range agent.Sources {
		if sourceID != "" && agent.Sources[index].ID == sourceID {
			return &agent.Sources[index]
		}
	}
	return nil
}

// relayDailyFacts sends the facts of a report the brain could not write.
func (s *Service) relayDailyFacts(ctx context.Context, agent core.SuperAgent, trigger core.SuperAgentTrigger) {
	if len(trigger.Reply) == 0 {
		return
	}
	_, facts, found := strings.Cut(trigger.Text, reportFactsMarker)
	if !found {
		return
	}
	text := "I could not write today's report, so here is what AgentMux recorded:\n" + strings.TrimSpace(facts)
	if err := s.sendReply(ctx, agent, trigger.SourceID, trigger.Reply, text); err != nil {
		s.log.Warn("send daily report facts", "super_agent", agent.ID, "err", err)
	}
}

// dailyFacts is what AgentMux knows about an agent's day from its own records.
type dailyFacts struct {
	agentName string
	from, to  time.Time
	zone      *time.Location

	done, failed, stopped, open []string
	events                      map[string]int
	failedEvents                []string
	rework, blocking            []string
	memory                      []string
	needsPerson                 []string
	followUps                   []string
	frameworks                  []string
}

func (f dailyFacts) quiet() bool {
	people := 0
	for _, count := range f.events {
		people += count
	}
	return len(f.done)+len(f.failed)+len(f.stopped)+len(f.open)+people+len(f.failedEvents)+
		len(f.rework)+len(f.blocking)+len(f.memory)+len(f.needsPerson) == 0
}

func (s *Service) collectDailyFacts(ctx context.Context, agent core.SuperAgent, from, to time.Time) dailyFacts {
	zone := to.Location()
	if agent.DailyReport != nil {
		zone = s.reportZone(*agent.DailyReport)
	}
	facts := dailyFacts{agentName: agent.Name, from: from, to: to, zone: zone, events: map[string]int{}}
	in := func(at time.Time) bool { return !at.Before(from) && at.Before(to) }
	when := func(at time.Time) string { return at.In(zone).Format("01-02 15:04") }

	tickets, err := s.store.ListSuperAgentTickets(ctx, agent.ID, ticketListLimit)
	if err != nil {
		s.log.Warn("daily report: list tickets", "super_agent", agent.ID, "err", err)
	}
	for _, ticket := range newestFirst(tickets) {
		name := fmt.Sprintf("%s %q", ticket.ID, oneLine(ticket.Title, 120))
		switch ticket.Status {
		case core.SuperAgentTicketReady:
			if in(ticket.FinishedAt) {
				line := name + " done"
				if ticket.ReworkRounds > 0 {
					line += fmt.Sprintf(" after %d rework round(s)", ticket.ReworkRounds)
				}
				if output := oneLine(ticket.Output, 160); output != "" {
					line += ": " + output
				}
				facts.done = append(facts.done, line)
			}
		case core.SuperAgentTicketFailed:
			if in(ticket.FinishedAt) || in(ticket.UpdatedAt) {
				facts.failed = append(facts.failed, name+" failed: "+oneLine(ticket.Error, 160))
				facts.needsPerson = append(facts.needsPerson, name+" failed and nobody has retried it.")
			}
		case core.SuperAgentTicketStopped:
			if in(ticket.FinishedAt) || in(ticket.UpdatedAt) {
				facts.stopped = append(facts.stopped, name+" stopped")
			}
		case core.SuperAgentTicketQueued, core.SuperAgentTicketRunning:
			line := fmt.Sprintf("%s %s since %s", name, ticket.Status, when(ticket.CreatedAt))
			for _, signal := range ticket.Signals {
				if signal.ClearedAt.IsZero() {
					line += fmt.Sprintf("; %s since %s: %s", signal.Kind, when(signal.Since), oneLine(signal.Detail, 120))
					facts.needsPerson = append(facts.needsPerson, fmt.Sprintf("%s is %s: %s", name, signal.Kind, oneLine(signal.Detail, 160)))
				}
			}
			facts.open = append(facts.open, line)
		}
		for _, review := range ticket.Reviews {
			if !in(review.At) {
				continue
			}
			switch review.Verdict {
			case core.SuperAgentReviewRework:
				facts.rework = append(facts.rework, fmt.Sprintf("%s sent back (round %d): %s", name, review.Round, oneLine(review.Note, 200)))
			case core.SuperAgentReviewNeedsUser:
				if ticket.Status == core.SuperAgentTicketReady && lastVerdict(ticket) == core.SuperAgentReviewNeedsUser {
					facts.needsPerson = append(facts.needsPerson, fmt.Sprintf("%s needs a person's decision: %s", name, oneLine(review.Note, 200)))
				}
			}
		}
		if review := ticket.AutoReview; review != nil && in(review.At) && review.Blocking > 0 {
			facts.blocking = append(facts.blocking, fmt.Sprintf("%s: the independent review found %d blocking issue(s)", name, review.Blocking))
		}
		if ticket.ReplyError != "" && in(ticket.UpdatedAt) {
			facts.needsPerson = append(facts.needsPerson, fmt.Sprintf("The reply about %s could not be delivered: %s", name, oneLine(ticket.ReplyError, 160)))
		}
	}

	triggers, err := s.store.ListSuperAgentTriggers(ctx, agent.ID, reportTriggerList)
	if err != nil {
		s.log.Warn("daily report: list events", "super_agent", agent.ID, "err", err)
	}
	for _, trigger := range triggers {
		if !in(trigger.ReceivedAt) || trigger.Kind == core.SuperAgentTriggerDailyReport {
			continue
		}
		if trigger.Actor != "agentmux" {
			facts.events[trigger.Disposition]++
		}
		if trigger.Disposition == core.SuperAgentDispositionFailed {
			who := trigger.ActorName
			if who == "" {
				who = trigger.Actor
			}
			facts.failedEvents = append(facts.failedEvents, fmt.Sprintf("%s from %s at %s: %s", firstNonEmpty(trigger.Title, trigger.Kind, "message"), who, when(trigger.ReceivedAt), oneLine(trigger.Reason, 160)))
		}
	}

	facts.memory = s.memoryChanges(agent.ID, in, when)

	s.plansMu.Lock()
	plans, err := s.loadPlans(ctx, agent.ID)
	s.plansMu.Unlock()
	if err != nil {
		s.log.Warn("daily report: load plans", "super_agent", agent.ID, "err", err)
	}
	for _, plan := range plans {
		if plan.Status == core.SuperAgentPlanPending {
			facts.needsPerson = append(facts.needsPerson, fmt.Sprintf("Plan %q (%s tier) from %s waits for approval in the Console since %s.", oneLine(plan.Title, 120), plan.Tier, firstNonEmpty(plan.RequesterName, plan.Requester, "someone"), when(plan.CreatedAt)))
		}
	}
	for _, ask := range s.loadAsks(agent.ID) {
		facts.needsPerson = append(facts.needsPerson, fmt.Sprintf("Asked %s at %s and still waiting for an answer: %s", ask.Place, when(ask.At), oneLine(ask.Text, 160)))
	}

	timers, err := s.store.ListActiveSuperAgentTimers(ctx, agent.ID)
	if err != nil {
		s.log.Warn("daily report: list timers", "super_agent", agent.ID, "err", err)
	}
	for _, timer := range timers {
		if timer.DueAt.Before(to.Add(24 * time.Hour)) {
			facts.followUps = append(facts.followUps, fmt.Sprintf("%s: %s", when(timer.DueAt), oneLine(timer.Note, 160)))
		}
	}

	facts.frameworks = s.reportHealth(ctx, agent)
	return facts
}

func lastVerdict(ticket core.SuperAgentTicket) string {
	if len(ticket.Reviews) == 0 {
		return ""
	}
	return ticket.Reviews[len(ticket.Reviews)-1].Verdict
}

// memoryChanges are the files in the brain's memory folder it changed in the
// report's time, each with its first line.
func (s *Service) memoryChanges(agentID string, in func(time.Time) bool, when func(time.Time) string) []string {
	home, err := s.homeDir(agentID)
	if err != nil {
		return nil
	}
	dir := filepath.Join(home, memoryDirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var changes []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !in(info.ModTime()) {
			continue
		}
		line := fmt.Sprintf("memory/%s changed %s", entry.Name(), when(info.ModTime()))
		if data, err := os.ReadFile(filepath.Join(dir, entry.Name())); err == nil {
			for _, text := range strings.Split(string(data), "\n") {
				if text = strings.TrimSpace(strings.TrimLeft(text, "#-* ")); text != "" {
					line += ": " + oneLine(text, 160)
					break
				}
			}
		}
		changes = append(changes, line)
	}
	slices.Sort(changes)
	return changes
}

// reportHealth names the frameworks of the brain and its roster that are
// failing or at a usage limit on this machine.
func (s *Service) reportHealth(ctx context.Context, agent core.SuperAgent) []string {
	var lines []string
	if brain, err := s.store.GetAgentInstance(ctx, agent.RouterAgentID); err == nil && brain != nil {
		if record := s.runtimeHealth(ctx, "", brain.RuntimeID); unhealthy(record) {
			lines = append(lines, fmt.Sprintf("the brain's framework %s is %s: %s", brain.RuntimeID, record.State, oneLine(record.Reason, 160)))
		}
	}
	for _, entry := range s.rosterHealth(ctx, agent, nil) {
		if entry.State != string(health.StateFailing) && entry.State != string(health.StateUsageLimit) {
			continue
		}
		where := "this machine"
		if entry.HostID != "" {
			where = entry.HostID
		}
		line := fmt.Sprintf("%s on %s is %s", entry.Runtime, where, entry.State)
		if entry.ResetsAt != nil {
			line += " until " + entry.ResetsAt.Format(time.RFC3339)
		}
		if entry.Reason != "" {
			line += ": " + entry.Reason
		}
		lines = append(lines, line)
	}
	return lines
}

// render is the report event: what to write, then the facts.
func (f dailyFacts) render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "It is time for your daily report, covering %s to %s (%s).\n\n",
		f.from.In(f.zone).Format("2006-01-02 15:04"), f.to.In(f.zone).Format("2006-01-02 15:04"), f.zone)
	b.WriteString(`Write a brief report for the people who receive it, in the language they use with you, with these four short sections:
1. Tasks: the status and outcome of the day's work. Say what came of it, not how it was done.
2. Rules and lessons: what you learned today: corrections people made, why work was sent back, what you wrote to memory and why it matters.
3. Needs a person: what someone must decide, approve, answer, follow up on or help with. Say who, if you know, and what they should do.
4. Anything else important that you noticed.
Use the facts below, what was said on your threads, and your memory. Write "Nothing" for an empty section. Keep it under 25 lines. Take no actions for this event: no Tickets, posts, follow-ups or reviews. Your answer is the report.

`)
	b.WriteString(reportFactsMarker + "\n")
	section := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n%s (%d):\n", title, len(items))
		for index, item := range items {
			if index == maxReportItems {
				fmt.Fprintf(&b, "- … and %d more\n", len(items)-maxReportItems)
				break
			}
			fmt.Fprintf(&b, "- %s\n", oneLine(item, maxReportLine))
		}
	}
	section("Tickets done", f.done)
	section("Tickets failed", f.failed)
	section("Tickets stopped", f.stopped)
	section("Tickets still open", f.open)
	if len(f.events) > 0 {
		var parts []string
		total := 0
		for _, disposition := range slices.Sorted(maps.Keys(f.events)) {
			parts = append(parts, fmt.Sprintf("%s %d", disposition, f.events[disposition]))
			total += f.events[disposition]
		}
		fmt.Fprintf(&b, "\nMessages and events from people: %d (%s)\n", total, strings.Join(parts, ", "))
	}
	section("Events that failed", f.failedEvents)
	section("Work sent back for rework", f.rework)
	section("Independent reviews with blocking findings", f.blocking)
	section("Memory you wrote", f.memory)
	section("Needs a person", f.needsPerson)
	section("Follow-ups due in the next 24 hours", f.followUps)
	section("Frameworks with problems", f.frameworks)
	if f.quiet() {
		b.WriteString("\nNothing was recorded in this time.\n")
	}
	return b.String()
}
