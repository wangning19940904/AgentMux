package superagent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

const issueTarget = `{"repo":"o/r","number":1}`

// withDelivery gives the harness's agent delivery rules.
func (h *harness) withDelivery(rules core.SuperAgentDelivery) {
	h.t.Helper()
	agent := h.agent
	agent.Delivery = rules
	if err := normalizeDelivery(&agent); err != nil {
		h.t.Fatal(err)
	}
	if err := h.store.UpsertSuperAgent(context.Background(), agent); err != nil {
		h.t.Fatal(err)
	}
	h.agent = agent
}

// unprompted sends one message the agent sends on its own.
func (h *harness) unprompted(target, text string, class deliveryClass) string {
	h.t.Helper()
	class.unprompted = true
	note, err := h.service.deliver(context.Background(), h.agent, "gh", json.RawMessage(target), text, class)
	if err != nil {
		h.t.Fatal(err)
	}
	return note
}

func (h *harness) held() []HeldMessage {
	h.t.Helper()
	state, err := h.service.loadDelivery(context.Background(), h.agent.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	return state.Held
}

func (h *harness) release() {
	h.t.Helper()
	h.service.releaseHeld(context.Background())
}

func TestQuietHoursCrossMidnightInTheirTimeZone(t *testing.T) {
	window := newDeliveryWindow(core.SuperAgentDelivery{QuietStart: "22:00", QuietEnd: "08:00", TimeZone: "Asia/Shanghai"})
	for _, test := range []struct {
		utc   string
		quiet bool
	}{
		{"2026-11-03T13:59:00Z", false}, // 21:59 in Shanghai
		{"2026-11-03T14:00:00Z", true},  // 22:00
		{"2026-11-03T16:00:00Z", true},  // midnight
		{"2026-11-03T23:59:00Z", true},  // 07:59
		{"2026-11-04T00:00:00Z", false}, // 08:00
		{"2026-11-04T04:00:00Z", false}, // noon
	} {
		at, _ := time.Parse(time.RFC3339, test.utc)
		if got := window.quiet(at); got != test.quiet {
			t.Errorf("quiet(%s) = %v", test.utc, got)
		}
	}
	// 22:30 in Shanghai waits until 08:00 there, which is midnight UTC.
	at, _ := time.Parse(time.RFC3339, "2026-11-03T14:30:00Z")
	if opens := window.opens(at, heldQuietHours); !opens.Equal(time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("opens = %s", opens.UTC())
	}
	// A cap reached in the afternoon waits for the next day out of quiet
	// hours: 08:00, not midnight.
	at, _ = time.Parse(time.RFC3339, "2026-11-03T07:00:00Z")
	if opens := window.opens(at, heldDailyCap); !opens.Equal(time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("cap opens = %s", opens.UTC())
	}

	// A window within one day, in a zone with daylight saving.
	window = newDeliveryWindow(core.SuperAgentDelivery{QuietStart: "12:00", QuietEnd: "13:30", TimeZone: "America/New_York"})
	for _, test := range []struct {
		utc   string
		quiet bool
	}{
		{"2026-07-01T16:00:00Z", true},  // 12:00 EDT
		{"2026-07-01T17:29:00Z", true},  // 13:29 EDT
		{"2026-07-01T17:30:00Z", false}, // 13:30 EDT
		{"2026-12-01T16:00:00Z", false}, // 11:00 EST
		{"2026-12-01T17:00:00Z", true},  // 12:00 EST
	} {
		at, _ := time.Parse(time.RFC3339, test.utc)
		if got := window.quiet(at); got != test.quiet {
			t.Errorf("quiet(%s) = %v", test.utc, got)
		}
	}
	if newDeliveryWindow(core.SuperAgentDelivery{}).quiet(at) {
		t.Fatal("no quiet hours were set")
	}
}

func TestDeliveryRulesAreChecked(t *testing.T) {
	for _, rules := range []core.SuperAgentDelivery{
		{QuietStart: "22:00"},
		{QuietStart: "25:00", QuietEnd: "08:00"},
		{QuietStart: "22:00", QuietEnd: "22:00"},
		{QuietStart: "22:00", QuietEnd: "8"},
		{TimeZone: "Mars/Olympus"},
		{DailyCap: -1},
		{QuietChatMinutes: 61},
		{Held: "drop"},
	} {
		agent := core.SuperAgent{Delivery: rules}
		if err := normalizeDelivery(&agent); err == nil {
			t.Errorf("%+v was accepted", rules)
		}
	}
	agent := core.SuperAgent{Delivery: core.SuperAgentDelivery{QuietStart: " 7:05", QuietEnd: "23:00", Held: "Queue"}}
	if err := normalizeDelivery(&agent); err != nil || agent.Delivery.QuietStart != "07:05" || agent.Delivery.Held != "" {
		t.Fatalf("delivery = %+v, err = %v", agent.Delivery, err)
	}
	if (core.SuperAgentDelivery{}).Active() {
		t.Fatal("the zero rules are active")
	}
}

func TestDirectRepliesAreNeverHeldAndReportsWaitForTheMorning(t *testing.T) {
	h := newHarness(t)
	// The harness starts at midnight UTC: inside quiet hours, the cap is one,
	// and the chat was just busy.
	h.withDelivery(core.SuperAgentDelivery{QuietStart: "22:00", QuietEnd: "08:00", TimeZone: "UTC", DailyCap: 1, QuietChatMinutes: 5})
	h.ingest("alice", "github:o/r#1", "please fix the build", true)
	h.settle()

	// The answer to alice goes at once; the report the brain sends when the
	// worker finishes is unprompted and waits.
	if got := h.replies.sent(); !slices.Equal(got, []string{"on it"}) {
		t.Fatalf("replies = %v", got)
	}
	held := h.held()
	if len(held) != 1 || held[0].Text != "done" || held[0].Reason != heldQuietHours || !held[0].Until.Equal(h.clock.Add(8*time.Hour)) {
		t.Fatalf("held = %+v", held)
	}
	var update core.SuperAgentTrigger
	for _, trigger := range h.store.triggers {
		if trigger.Kind == core.SuperAgentTriggerTicketUpdate {
			update = trigger
		}
	}
	if !strings.Contains(update.Reason, "reply held: quiet hours") || update.Disposition != core.SuperAgentDispositionAnswered {
		t.Fatalf("ticket update = %+v", update)
	}
	activity, err := h.service.Activity(context.Background(), h.agent.ID)
	if err != nil || activity.Delivery == nil || len(activity.Delivery.Held) != 1 {
		t.Fatalf("activity delivery = %+v, err = %v", activity.Delivery, err)
	}

	// More direct replies still go at once, past the cap and in quiet hours.
	h.router.answer = func(core.InvocationRequest) (string, error) { return "you're welcome", nil }
	h.ingest("alice", "github:o/r#1", "thanks", true)
	h.settle()
	if got := h.replies.sent(); len(got) != 2 {
		t.Fatalf("replies = %v", got)
	}

	h.advance(7 * time.Hour)
	h.release()
	if len(h.replies.sent()) != 2 {
		t.Fatal("a held message went during quiet hours")
	}
	h.advance(time.Hour)
	h.release()
	if got := h.replies.sent(); got[len(got)-1] != "done" || len(h.held()) != 0 {
		t.Fatalf("replies = %v, held = %+v", got, h.held())
	}
}

func TestDailyCapHoldsPerRecipientUntilTheNextDay(t *testing.T) {
	h := newHarness(t)
	h.withDelivery(core.SuperAgentDelivery{DailyCap: 2, TimeZone: "Asia/Shanghai"})
	for _, text := range []string{"one", "two", "three", "four", "five"} {
		h.unprompted(issueTarget, text, deliveryClass{})
	}
	other := `{"repo":"o/r","number":2}`
	h.unprompted(other, "elsewhere", deliveryClass{})
	if got := h.replies.sent(); !slices.Equal(got, []string{"one", "two", "elsewhere"}) {
		t.Fatalf("sent = %v", got)
	}
	held := h.held()
	if len(held) != 3 || held[0].Reason != heldDailyCap {
		t.Fatalf("held = %+v", held)
	}
	// The harness starts at 08:00 in Shanghai; the next day there starts at
	// 16:00 UTC.
	if want := time.Date(2026, 11, 3, 16, 0, 0, 0, time.UTC); !held[0].Until.Equal(want) {
		t.Fatalf("until = %s, want %s", held[0].Until.UTC(), want)
	}
	h.advance(15 * time.Hour)
	h.release()
	if len(h.replies.sent()) != 3 {
		t.Fatal("held messages went before the day changed")
	}
	// On the new day the cap allows two: the first held message on its own,
	// then the other two folded into one.
	h.advance(time.Hour)
	h.release()
	got := h.replies.sent()
	if len(got) != 5 || got[3] != "three" || !strings.HasPrefix(got[4], "2 messages I held back") || !strings.Contains(got[4], "four") || !strings.Contains(got[4], "five") {
		t.Fatalf("sent = %q", got)
	}
	if len(h.held()) != 0 {
		t.Fatalf("held = %+v", h.held())
	}
	if note := h.unprompted(issueTarget, "six", deliveryClass{}); note == "" {
		t.Fatal("the release did not count toward the new day's cap")
	}
}

func TestQuietChatDelayWaitsForTheChatToGoQuiet(t *testing.T) {
	h := newHarness(t)
	h.withDelivery(core.SuperAgentDelivery{QuietChatMinutes: 3})
	// Someone comments on the issue; the comment addresses nobody and is
	// dropped, but the chat is busy.
	h.ingest("bob", "github:o/r#1", "looking at it now", false)
	if note := h.unprompted(issueTarget, "the build is green", deliveryClass{}); !strings.Contains(note, "quiet") {
		t.Fatalf("note = %q", note)
	}
	// A message behind it waits its turn.
	h.unprompted(issueTarget, "and the docs too", deliveryClass{})
	h.advance(2 * time.Minute)
	h.release()
	if len(h.replies.sent()) != 0 {
		t.Fatal("sent while the chat was busy")
	}
	h.advance(time.Minute)
	h.release()
	if got := h.replies.sent(); !slices.Equal(got, []string{"the build is green", "and the docs too"}) {
		t.Fatalf("sent = %v", got)
	}

	// A message in a channel chat counts too.
	chat := `{"channel_id":"ch1","chat_id":"oc_1"}`
	if _, err := h.service.InterceptMessage(context.Background(), &core.Message{ChannelID: "ch1", ChatID: "oc_1", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	note, err := h.service.deliver(context.Background(), h.agent, channelSourceID("ch1"), json.RawMessage(chat), "report", deliveryClass{unprompted: true})
	if err != nil || note == "" {
		t.Fatalf("note = %q, err = %v", note, err)
	}
	// A direct reply is never delayed.
	if err := h.service.sendReply(context.Background(), h.agent, channelSourceID("ch1"), json.RawMessage(chat), "answer"); err != nil {
		t.Fatal(err)
	}
	if got := h.replies.sent(); got[len(got)-1] != "answer" {
		t.Fatalf("sent = %v", got)
	}
}

func TestDigestFoldsHeldMessagesWhenQuietHoursEnd(t *testing.T) {
	h := newHarness(t)
	h.withDelivery(core.SuperAgentDelivery{QuietStart: "23:00", QuietEnd: "07:30", TimeZone: "UTC", Held: core.SuperAgentHeldDigest})
	h.unprompted(issueTarget, "first", deliveryClass{})
	h.advance(time.Hour)
	h.unprompted(issueTarget, "second", deliveryClass{})
	other := `{"repo":"o/r","number":2}`
	h.unprompted(other, "alone", deliveryClass{})
	h.advance(6*time.Hour + 29*time.Minute)
	h.release()
	if len(h.replies.sent()) != 0 {
		t.Fatal("sent during quiet hours")
	}
	h.advance(time.Minute)
	h.release()
	digests := h.replies.sentTo(issueTarget)
	if len(digests) != 1 || !strings.Contains(digests[0], "[11-03 00:00] first") || !strings.Contains(digests[0], "[11-03 01:00] second") {
		t.Fatalf("digest = %q", digests)
	}
	if alone := h.replies.sentTo(other); !slices.Equal(alone, []string{"alone"}) {
		t.Fatalf("other = %q", alone)
	}
}

func TestOneItemInOneStateGoesOncePerDay(t *testing.T) {
	h := newHarness(t)
	h.withDelivery(core.SuperAgentDelivery{OncePerDay: true, TimeZone: "UTC"})
	stalled := deliveryClass{item: "ticket:tkt_1", state: "supervision"}
	h.unprompted(issueTarget, "tkt_1 looks stalled", stalled)
	if note := h.unprompted(issueTarget, "tkt_1 still looks stalled", stalled); !strings.HasPrefix(note, "dropped") {
		t.Fatalf("note = %q", note)
	}
	// Another state of the item, and another item, still go; so do reports,
	// which are never repeats.
	h.unprompted(issueTarget, "tkt_1 follow-up", deliveryClass{item: "ticket:tkt_1", state: "follow_up"})
	h.unprompted(issueTarget, "report", deliveryClass{})
	h.unprompted(issueTarget, "report", deliveryClass{})
	if got := h.replies.sent(); !slices.Equal(got, []string{"tkt_1 looks stalled", "tkt_1 follow-up", "report", "report"}) {
		t.Fatalf("sent = %v", got)
	}
	activity, _ := h.service.Activity(context.Background(), h.agent.ID)
	if activity.Delivery == nil || len(activity.Delivery.Dropped) != 1 || activity.Delivery.SentToday["github:o/r#1"] != 4 {
		t.Fatalf("delivery = %+v", activity.Delivery)
	}
	h.advance(24 * time.Hour)
	if note := h.unprompted(issueTarget, "tkt_1 is still stalled", stalled); note != "" {
		t.Fatalf("note = %q", note)
	}
}

func TestOnlyTheNewestHeldMessageAboutAnItemIsKept(t *testing.T) {
	h := newHarness(t)
	h.withDelivery(core.SuperAgentDelivery{OncePerDay: true, QuietStart: "00:00", QuietEnd: "06:00", TimeZone: "UTC"})
	stalled := deliveryClass{item: "ticket:tkt_1", state: "supervision"}
	h.unprompted(issueTarget, "stalled for 1h", stalled)
	h.unprompted(issueTarget, "stalled for 2h", stalled)
	if held := h.held(); len(held) != 1 || held[0].Text != "stalled for 2h" {
		t.Fatalf("held = %+v", held)
	}
	h.advance(6 * time.Hour)
	h.release()
	if note := h.unprompted(issueTarget, "stalled for 8h", stalled); !strings.HasPrefix(note, "dropped") {
		t.Fatalf("note = %q", note)
	}
	if got := h.replies.sent(); !slices.Equal(got, []string{"stalled for 2h"}) {
		t.Fatalf("sent = %v", got)
	}
}

func TestHeldMessagesSurviveARestartAndGoWhenRulesAreTurnedOff(t *testing.T) {
	h := newHarness(t)
	h.withDelivery(core.SuperAgentDelivery{QuietStart: "00:00", QuietEnd: "06:00", TimeZone: "UTC"})
	h.unprompted(issueTarget, "overnight", deliveryClass{})

	restarted := New(Options{Store: h.store, Replies: h.replies, Now: func() time.Time { return *h.clock }, StateDir: t.TempDir()})
	restarted.findHeld(context.Background())
	h.withDelivery(core.SuperAgentDelivery{})
	restarted.releaseHeld(context.Background())
	if got := h.replies.sent(); !slices.Equal(got, []string{"overnight"}) {
		t.Fatalf("sent = %v", got)
	}
	if len(h.held()) != 0 {
		t.Fatal("still held")
	}
}

func TestAMessageThatGoesNowhereIsNeitherHeldNorCounted(t *testing.T) {
	h := newHarness(t)
	h.withDelivery(core.SuperAgentDelivery{QuietStart: "00:00", QuietEnd: "06:00", TimeZone: "UTC"})
	// A document Source without a chat to tell records results only.
	doc := core.SuperAgentSource{ID: "doc", Kind: core.SuperAgentSourceFeishuDoc, Enabled: true}
	h.agent.Sources = append(h.agent.Sources, doc)
	note, err := h.service.deliver(context.Background(), h.agent, "doc", json.RawMessage(`{"channel_id":"ch1"}`), "edited", deliveryClass{unprompted: true})
	if err != nil || note != "" || len(h.held()) != 0 {
		t.Fatalf("note = %q, err = %v, held = %+v", note, err, h.held())
	}
}

func TestTriggersAreClassified(t *testing.T) {
	for _, test := range []struct {
		trigger    core.SuperAgentTrigger
		unprompted bool
		item       string
	}{
		{core.SuperAgentTrigger{Addressed: true, SourceKind: core.SuperAgentSourceFeishuMessage}, false, ""},
		{core.SuperAgentTrigger{Addressed: true, SourceKind: core.SuperAgentSourceEmail}, false, ""},
		{core.SuperAgentTrigger{Addressed: true, SourceKind: core.SuperAgentSourceChannel}, false, ""},
		{core.SuperAgentTrigger{Addressed: true, Kind: core.SuperAgentTriggerPlan}, false, ""},
		{core.SuperAgentTrigger{Addressed: true, Kind: core.SuperAgentTriggerTicketUpdate, TicketID: "t"}, true, ""},
		{core.SuperAgentTrigger{Addressed: true, Kind: core.SuperAgentTriggerTimer, ThreadKey: "x"}, true, "thread:x"},
		{core.SuperAgentTrigger{Addressed: true, Kind: core.SuperAgentTriggerSupervision, TicketID: "t"}, true, "ticket:t"},
		{core.SuperAgentTrigger{Addressed: true, SourceKind: core.SuperAgentSourceFeishuDoc, ThreadKey: "d"}, true, "thread:d"},
		{core.SuperAgentTrigger{SourceKind: core.SuperAgentSourceGitHub, ThreadKey: "g"}, true, "thread:g"},
	} {
		class := triggerDelivery(test.trigger)
		if class.unprompted != test.unprompted || class.item != test.item {
			t.Errorf("%+v: %+v", test.trigger, class)
		}
	}
}
