package superagent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

const reportTarget = `{"repo":"o/r","number":1}`

func isReportEvent(prompt string) bool {
	return strings.Contains(prompt, `"kind": "`+core.SuperAgentTriggerDailyReport+`"`)
}

// reportRouter accepts ordinary requests and answers report events with
// onReport.
func (h *harness) reportRouter(onReport func(prompt string) (string, error)) {
	h.router.answer = func(req core.InvocationRequest) (string, error) {
		if isReportEvent(req.Input) {
			return onReport(req.Input)
		}
		return acceptAnswer("local"), nil
	}
}

func (h *harness) enableReport(report core.SuperAgentDailyReport) {
	h.t.Helper()
	agent := h.agent
	agent.DailyReport = &report
	saved, err := h.service.Save(context.Background(), agent)
	if err != nil {
		h.t.Fatal(err)
	}
	h.agent = saved
}

func (h *harness) reportEvents() []core.SuperAgentTrigger {
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	var out []core.SuperAgentTrigger
	for _, trigger := range h.store.triggers {
		if trigger.Kind == core.SuperAgentTriggerDailyReport {
			out = append(out, trigger)
		}
	}
	return out
}

func (h *harness) reportPrompts() []string {
	h.router.mu.Lock()
	defer h.router.mu.Unlock()
	var out []string
	for _, prompt := range h.router.prompts {
		if isReportEvent(prompt) {
			out = append(out, prompt)
		}
	}
	return out
}

func TestDailyReportIsWrittenFromTheDayAndSent(t *testing.T) {
	h := newHarness(t)
	h.reportRouter(func(string) (string, error) { return "Today: fixed the build.", nil })
	h.enableReport(core.SuperAgentDailyReport{Enabled: true, At: "09:00", Timezone: "UTC", To: "github:o/r#1"})
	h.ingest("alice", "github:o/r#1", "please fix the build", true)
	h.settle()
	if len(h.reportEvents()) != 0 {
		t.Fatal("a report was sent before its time")
	}

	h.advance(9*time.Hour + 30*time.Second)
	h.settle()
	events := h.reportEvents()
	if len(events) != 1 {
		t.Fatalf("report events = %+v", events)
	}
	event := events[0]
	if event.ThreadKey != core.SuperAgentDailyReportThread || event.SourceID != "gh" || string(event.Reply) != reportTarget || event.Title != "Daily report 2026-11-03" {
		t.Fatalf("report event = %+v", event)
	}
	prompts := h.reportPrompts()
	if len(prompts) != 1 {
		t.Fatalf("report prompts = %d", len(prompts))
	}
	for _, want := range []string{"Rules and lessons", "Needs a person", "Tickets done (1)", `\"Fix it\" done`, "Messages and events from people: 1 (accepted 1)"} {
		if !strings.Contains(prompts[0], want) {
			t.Fatalf("the report event lacks %q:\n%s", want, prompts[0])
		}
	}
	if sent := h.replies.sentTo(reportTarget); len(sent) == 0 || sent[len(sent)-1] != "Today: fixed the build." {
		t.Fatalf("sent = %q", sent)
	}

	// Once a day: later passes the same day send nothing more.
	h.advance(2 * time.Hour)
	h.settle()
	if len(h.reportEvents()) != 1 {
		t.Fatal("the report was sent twice in one day")
	}
}

func TestDailyReportSkipsQuietDaysUnlessAlways(t *testing.T) {
	h := newHarness(t)
	h.reportRouter(func(string) (string, error) { return "Nothing happened.", nil })
	h.enableReport(core.SuperAgentDailyReport{Enabled: true, At: "09:00", Timezone: "UTC", To: "github:o/r#1"})
	h.advance(9*time.Hour + time.Minute)
	h.settle()
	if len(h.reportEvents()) != 0 {
		t.Fatal("a quiet day was reported")
	}

	h.enableReport(core.SuperAgentDailyReport{Enabled: true, At: "09:00", Timezone: "UTC", To: "github:o/r#1", Always: true})
	h.advance(24 * time.Hour)
	h.settle()
	events := h.reportEvents()
	if len(events) != 1 || !strings.Contains(h.reportPrompts()[0], "Nothing was recorded in this time.") {
		t.Fatalf("report events = %+v", events)
	}
}

func TestDailyReportTurnedOnLateWaitsForTheNextDay(t *testing.T) {
	h := newHarness(t)
	h.reportRouter(func(string) (string, error) { return "report", nil })
	h.ingest("alice", "github:o/r#1", "please fix the build", true)
	h.settle()
	h.advance(10 * time.Hour)
	h.enableReport(core.SuperAgentDailyReport{Enabled: true, At: "09:00", Timezone: "UTC", Always: true})
	h.settle()
	if len(h.reportEvents()) != 0 {
		t.Fatal("turning the report on after its time sent today's at once")
	}
	h.advance(23 * time.Hour)
	h.settle()
	if len(h.reportEvents()) != 1 {
		t.Fatal("the next day's report was not sent")
	}
}

func TestDailyReportMissedByHoursIsSkipped(t *testing.T) {
	h := newHarness(t)
	h.reportRouter(func(string) (string, error) { return "report", nil })
	h.enableReport(core.SuperAgentDailyReport{Enabled: true, At: "09:00", Timezone: "UTC", Always: true})
	// The daemon was down from before 09:00 until 16:00.
	h.advance(16 * time.Hour)
	h.settle()
	if len(h.reportEvents()) != 0 {
		t.Fatal("a report seven hours late was sent")
	}
	h.advance(17*time.Hour + time.Minute)
	h.settle()
	events := h.reportEvents()
	if len(events) != 1 || !strings.Contains(h.reportPrompts()[0], "covering 2026-11-03 09:00 to 2026-11-04 09:00") {
		t.Fatalf("report events = %+v prompts = %q", events, h.reportPrompts())
	}
}

func TestDailyReportFactsAreSentWhenTheBrainFails(t *testing.T) {
	h := newHarness(t)
	h.reportRouter(func(string) (string, error) { return "", errors.New("model unavailable") })
	h.enableReport(core.SuperAgentDailyReport{Enabled: true, At: "09:00", Timezone: "UTC", To: "github:o/r#1"})
	h.ingest("alice", "github:o/r#1", "please fix the build", true)
	h.settle()
	h.advance(9*time.Hour + time.Minute)
	h.exhaustRetries()

	events := h.reportEvents()
	if len(events) != 1 || events[0].Disposition != core.SuperAgentDispositionFailed {
		t.Fatalf("report events = %+v", events)
	}
	sent := h.replies.sentTo(reportTarget)
	last := sent[len(sent)-1]
	if !strings.HasPrefix(last, "I could not write today's report") || !strings.Contains(last, `"Fix it" done`) || strings.Contains(last, "Take no actions") {
		t.Fatalf("fallback = %q", last)
	}
}

func TestDailyReportTakesNoActions(t *testing.T) {
	h := newHarness(t)
	calls := 0
	h.reportRouter(func(string) (string, error) {
		calls++
		if calls == 1 {
			return followUpAnswer("Report", `"in":"1h"`, "chase Li"), nil
		}
		return "Report without actions", nil
	})
	h.enableReport(core.SuperAgentDailyReport{Enabled: true, At: "09:00", Timezone: "UTC", To: "github:o/r#1", Always: true})
	h.advance(9*time.Hour + time.Minute)
	h.settle()
	h.advance(retryDelay + time.Second)
	h.settle()

	if active := h.timers(core.SuperAgentTimerActive); len(active) != 0 {
		t.Fatalf("a report event set a follow-up: %+v", active)
	}
	if sent := h.replies.sentTo(reportTarget); len(sent) == 0 || sent[len(sent)-1] != "Report without actions" {
		t.Fatalf("sent = %q", sent)
	}
}

func TestRunDailyReportNowCoversSinceTheLastReport(t *testing.T) {
	h := newHarness(t)
	h.reportRouter(func(string) (string, error) { return "report", nil })
	h.enableReport(core.SuperAgentDailyReport{Enabled: true, At: "09:00", Timezone: "UTC"})
	h.advance(5 * time.Hour)
	if err := h.service.RunDailyReport(context.Background(), h.agent.ID); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if len(h.reportEvents()) != 1 {
		t.Fatal("run now sent no report on a quiet day")
	}
	h.ingest("alice", "github:o/r#1", "please fix the build", true)
	h.settle()
	h.advance(4*time.Hour + time.Minute)
	h.settle()
	prompts := h.reportPrompts()
	if len(prompts) != 2 || !strings.Contains(prompts[1], "covering 2026-11-03 05:00 to 2026-11-03 09:00") {
		t.Fatalf("prompts = %q", prompts)
	}
	if err := h.service.RunDailyReport(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("run for a missing agent: %v", err)
	}
}

func TestNormalizeDailyReport(t *testing.T) {
	for _, test := range []struct {
		report core.SuperAgentDailyReport
		err    string
		kept   bool
	}{
		{report: core.SuperAgentDailyReport{Enabled: true, At: "08:30", Timezone: "Asia/Shanghai", To: "chat:oc_1"}, kept: true},
		{report: core.SuperAgentDailyReport{Enabled: true, To: "person:li"}, kept: true},
		{report: core.SuperAgentDailyReport{}, kept: false},
		{report: core.SuperAgentDailyReport{Enabled: true, At: "9:00pm"}, err: "daily_report.at"},
		{report: core.SuperAgentDailyReport{Enabled: true, At: "24:00"}, err: "daily_report.at"},
		{report: core.SuperAgentDailyReport{Enabled: true, Timezone: "Mars/Olympus"}, err: "daily_report.timezone"},
		{report: core.SuperAgentDailyReport{Enabled: true, To: "#general"}, err: "daily_report.to"},
	} {
		report := test.report
		agent := core.SuperAgent{DailyReport: &report}
		err := normalizeDailyReport(&agent)
		if test.err != "" {
			if err == nil || !strings.Contains(err.Error(), test.err) {
				t.Fatalf("%+v: err = %v", test.report, err)
			}
			continue
		}
		if err != nil || (agent.DailyReport != nil) != test.kept {
			t.Fatalf("%+v: err = %v, kept = %v", test.report, err, agent.DailyReport != nil)
		}
	}
}

func TestLastReportDue(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skip(err)
	}
	// 00:30 UTC is 08:30 in Shanghai: today's 09:00 has not come, so the
	// latest is yesterday's.
	now := time.Date(2026, 11, 3, 0, 30, 0, 0, time.UTC)
	if due := lastReportDue(now, "09:00", shanghai); !due.Equal(time.Date(2026, 11, 2, 9, 0, 0, 0, shanghai)) {
		t.Fatalf("due = %s", due)
	}
	if due := lastReportDue(now.Add(time.Hour), "09:00", shanghai); !due.Equal(time.Date(2026, 11, 3, 9, 0, 0, 0, shanghai)) {
		t.Fatalf("due = %s", due)
	}
	if due := lastReportDue(now, "", time.UTC); !due.Equal(time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("default due = %s", due)
	}
}
