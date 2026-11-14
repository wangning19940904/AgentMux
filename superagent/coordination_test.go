package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// ingestOn stores an event with its own reply address, as two GitHub issues
// or two Feishu threads would have.
func (h *harness) ingestOn(thread, text, reply string) {
	h.t.Helper()
	trigger := core.SuperAgentTrigger{
		DedupeKey: fmt.Sprintf("%s|%s|%d", thread, text, len(h.store.triggers)), ThreadKey: thread, Actor: "alice",
		Addressed: true, Text: text, Reply: json.RawMessage(reply),
	}
	if _, err := h.service.Ingest(context.Background(), h.agent, h.source, trigger); err != nil {
		h.t.Fatal(err)
	}
}

func actions(reply string, list ...string) func(core.InvocationRequest) (string, error) {
	return func(core.InvocationRequest) (string, error) {
		return reply + "\n<agentmux-action>[" + strings.Join(list, ",") + "]</agentmux-action>", nil
	}
}

func TestRetryTellsTheRouterWhyNothingHappened(t *testing.T) {
	h := newHarness(t)
	calls := 0
	h.router.answer = func(core.InvocationRequest) (string, error) {
		calls++
		if calls == 1 {
			return "已转交给开发。\n<agentmux-action>{\"action\":\"accept\",\"worker_id\":\"ghost\"}</agentmux-action>", nil
		}
		return acceptAnswer("local"), nil
	}
	h.ingest("alice", "github:o/r#1", "fix it", true)
	h.settle()
	if len(h.store.allTickets()) != 0 || len(h.replies.sent()) != 0 {
		t.Fatal("a refused answer must do nothing and say nothing")
	}
	if trigger := h.disposition(0); trigger.Disposition != core.SuperAgentDispositionPending || !strings.Contains(trigger.Reason, `worker "ghost" is not on the roster`) {
		t.Fatalf("the failed attempt must be on the record: %+v", trigger)
	}
	h.advance(retryDelay + 1)
	h.settle()
	retry := h.router.prompts[1]
	for _, want := range []string{"Your previous answer to it was not carried out: worker \"ghost\" is not on the roster", "Nothing happened"} {
		if !strings.Contains(retry, want) {
			t.Fatalf("retry prompt lacks %q:\n%s", want, retry)
		}
	}
	if strings.Contains(retry, "do not act on it again") {
		t.Fatal("a retry must not tell the router the event was handled")
	}
	if len(h.store.allTickets()) != 1 || h.disposition(0).Disposition != core.SuperAgentDispositionAccepted {
		t.Fatalf("the retry must go through: %+v", h.disposition(0))
	}
}

func TestAttachFromAnotherThreadLinksIt(t *testing.T) {
	h := newHarness(t)
	h.ingestOn("github:o/r#1", "build the CSV export", `{"repo":"o/r","number":1}`)
	h.settle()
	ticket := h.store.allTickets()[0]
	h.router.answer = actions("Passed it on to the export work.",
		fmt.Sprintf(`{"action":"attach","ticket_id":%q,"instructions":"JSON Lines now, not CSV"}`, ticket.ID))
	h.ingestOn("github:o/r#2", "finance needs JSON Lines instead", `{"repo":"o/r","number":2}`)
	h.settle()

	if !strings.Contains(h.router.prompts[1], `"id": "`+ticket.ID+`"`) || !strings.Contains(h.router.prompts[1], `"thread": "GitHub o/r#1"`) {
		t.Fatalf("the router must see work on other threads:\n%s", h.router.prompts[1])
	}
	input := h.workers.calls[1].input
	if !strings.Contains(input, "JSON Lines now, not CSV") || !strings.Contains(input, "finance needs JSON Lines instead") || !strings.Contains(input, `"where": "GitHub o/r#2"`) {
		t.Fatalf("follow-up:\n%s", input)
	}
	got, _ := h.store.GetSuperAgentTicket(context.Background(), ticket.ID)
	if string(got.Reply) != `{"repo":"o/r","number":1}` || len(got.Links) != 1 || got.Links[0].ThreadKey != "github:o/r#2" {
		t.Fatalf("the ticket keeps its thread and links the other: %+v", got)
	}
	if replies := h.replies.sentTo(`{"repo":"o/r","number":2}`); !slices.Equal(replies, []string{"Passed it on to the export work."}) {
		t.Fatalf("replies on #2 = %v", replies)
	}
	if update := h.router.updatePrompts[len(h.router.updatePrompts)-1]; !strings.Contains(update, "github:o/r#2") {
		t.Fatalf("the ticket update must name the linked thread:\n%s", update)
	}
}

func TestStopReachesATicketOnAnotherThread(t *testing.T) {
	h := newHarness(t)
	release := make(chan struct{})
	h.workers.run = func(ctx context.Context, _ workerCall) (string, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return "late", ctx.Err()
	}
	h.ingestOn("github:o/r#1", "duplicate work", `{"repo":"o/r","number":1}`)
	for len(h.store.allTickets()) == 0 || h.store.allTickets()[0].Status != core.SuperAgentTicketRunning {
		h.service.Pass(context.Background())
	}
	ticket := h.store.allTickets()[0]
	h.router.answer = actions("Stopped the duplicate.", fmt.Sprintf(`{"action":"stop","ticket_id":%q}`, ticket.ID))
	h.ingestOn("github:o/r#2", "stop the duplicate", `{"repo":"o/r","number":2}`)
	h.settle()
	close(release)
	if got, _ := h.store.GetSuperAgentTicket(context.Background(), ticket.ID); got.Status != core.SuperAgentTicketStopped {
		t.Fatalf("ticket = %+v", got)
	}
	if h.disposition(1).Disposition != core.SuperAgentDispositionStopped {
		t.Fatalf("trigger = %+v", h.disposition(1))
	}
}

func TestPostReachesKnownPlacesOnly(t *testing.T) {
	h := newHarness(t)
	h.ingestOn("github:o/r#1", "fix the rounding", `{"repo":"o/r","number":1}`)
	h.settle()
	ticket := h.store.allTickets()[0]
	h.router.answer = actions("Noted.",
		fmt.Sprintf(`{"action":"post","to":%q,"text":"Heads up: PR #7 duplicates this."}`, ticket.ID),
		`{"action":"post","to":"github:o/r#7","text":"Please hold: this is already being fixed."}`)
	h.ingestOn("github:o/r#7", "PR: epsilon fix", `{"repo":"o/r","number":7}`)
	h.settle()
	if got := h.replies.sentTo(`{"repo":"o/r","number":1}`); !slices.Contains(got, "Heads up: PR #7 duplicates this.") {
		t.Fatalf("thread #1 = %v", got)
	}
	if got := h.replies.sentTo(`{"repo":"o/r","number":7}`); !slices.Equal(got, []string{"Noted.", "Please hold: this is already being fixed."}) {
		t.Fatalf("thread #7 = %v", got)
	}

	before := len(h.replies.sent())
	h.router.answer = actions("", `{"action":"post","to":"chat:oc_elsewhere","text":"leak"}`)
	h.ingestOn("github:o/r#8", "tell the other group", `{"repo":"o/r","number":8}`)
	h.exhaustRetries()
	if got := h.replies.sent()[before:]; slices.Contains(got, "leak") {
		t.Fatalf("a post must not reach a place this Super Agent does not know: %v", got)
	}
}

func TestDependentTicketWaitsAndStartsFromItsBase(t *testing.T) {
	h := newHarness(t)
	release := make(chan struct{})
	var mu sync.Mutex
	started := map[string]string{}
	h.workers.run = func(ctx context.Context, call workerCall) (string, error) {
		mu.Lock()
		started[call.conv] = call.base
		mu.Unlock()
		if strings.Contains(call.input, "Decimal pricing") {
			<-release
		}
		return "done: " + call.conv, nil
	}
	h.router.answer = actions("Two tasks: pricing first, then currencies on top of it.",
		`{"action":"accept","ref":"pricing","worker_id":"local","title":"Decimal pricing"}`,
		`{"action":"accept","worker_id":"local","title":"Multi-currency","depends_on":["pricing"]}`)
	h.ingest("alice", "github:o/r#1", "pricing then currencies", true)
	for len(h.store.allTickets()) < 2 || h.store.allTickets()[0].Status != core.SuperAgentTicketRunning {
		h.service.Pass(context.Background())
	}
	tickets := h.store.allTickets()
	pricing, currency := tickets[0], tickets[1]
	if pricing.Title != "Decimal pricing" {
		pricing, currency = currency, pricing
	}
	if !slices.Equal(currency.DependsOn, []string{pricing.ID}) || currency.BaseTicket != pricing.ID {
		t.Fatalf("the ref must resolve to the new ticket: %+v", currency)
	}
	if !strings.Contains(currency.Inputs[0], pricing.ID) || !strings.Contains(currency.Inputs[0], "Build on them") {
		t.Fatalf("the dependent worker must be told what it builds on:\n%s", currency.Inputs[0])
	}
	// The base ticket committed its work.
	home, _ := h.service.homeDir(h.agent.ID)
	work := filepath.Join(home, ticketsDirName, pricing.ID, ticketWorkDir)
	commit := gitCommit(t, work)

	h.service.Pass(context.Background())
	if got, _ := h.store.GetSuperAgentTicket(context.Background(), currency.ID); got.Status != core.SuperAgentTicketQueued || h.workers.callCount() != 1 {
		t.Fatalf("a dependent ticket must wait: %+v", got)
	}
	close(release)
	h.settle()
	mu.Lock()
	base := started[currency.ConversationID]
	mu.Unlock()
	if base != commit {
		t.Fatalf("the dependent ticket must start from its base's commit: %q != %q", base, commit)
	}
	if !slices.ContainsFunc(h.router.updatePrompts, func(prompt string) bool { return strings.Contains(prompt, "AgentMux is now starting "+currency.ID) }) {
		t.Fatalf("the router must hear what the finished ticket unblocked: %v", h.router.updatePrompts)
	}
}

func gitCommit(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "base"},
	} {
		if output, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, output)
		}
	}
	output, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(output))
}

func TestFinishedTurnWakesTheRouterOnItsThread(t *testing.T) {
	h := newHarness(t)
	h.workers.run = func(context.Context, workerCall) (string, error) {
		return "Committed 1f9f9a5: Decimal pricing, tax after discount. 8 tests pass.\nDECISION NEEDED: round per line or per invoice?", nil
	}
	h.router.updates = func(req core.InvocationRequest) (string, error) {
		return "Decimal fix is in (1f9f9a5). <at user_id=\"ou_finance\">Lee</at>, per line or per invoice?", nil
	}
	h.ingest("alice", "github:o/r#1", "fix rounding", true)
	h.settle()
	if len(h.router.updatePrompts) != 1 {
		t.Fatalf("updates = %d", len(h.router.updatePrompts))
	}
	update := h.router.updatePrompts[0]
	for _, want := range []string{`"kind": "ticket_update"`, "finished a turn and is ready", "DECISION NEEDED: round per line"} {
		if !strings.Contains(update, want) {
			t.Fatalf("update prompt lacks %q:\n%s", want, update)
		}
	}
	if got := h.replies.sent(); !slices.Equal(got, []string{"on it", "Decimal fix is in (1f9f9a5). <at user_id=\"ou_finance\">Lee</at>, per line or per invoice?"}) {
		t.Fatalf("replies = %v", got)
	}
}

func TestFinishedTurnFallsBackToTheWorkersReport(t *testing.T) {
	h := newHarness(t)
	h.router.updates = func(core.InvocationRequest) (string, error) { return "", errors.New("model down") }
	h.ingest("alice", "github:o/r#1", "fix it", true)
	h.exhaustRetries()
	if got := h.replies.sent(); !slices.Equal(got, []string{"on it", "done"}) {
		t.Fatalf("people must still hear the worker when the router is down: %v", got)
	}
}

// steeringWorkers takes input into a running turn.
type steeringWorkers struct {
	*workerStub
	mu      sync.Mutex
	steered []string
}

func (w *steeringWorkers) Steer(_ context.Context, _ core.SuperAgentWorker, _ string, text string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.steered = append(w.steered, text)
	return nil
}

func TestSteeringReachesARunningTurn(t *testing.T) {
	h := newHarness(t)
	steering := &steeringWorkers{workerStub: h.workers}
	h.service.workers = steering
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	h.workers.run = func(context.Context, workerCall) (string, error) {
		once.Do(func() { close(entered) })
		<-release
		return "JSON Lines export done", nil
	}
	h.ingest("alice", "github:o/r#1", "CSV export", true)
	for running := true; running; {
		select {
		case <-entered:
			running = false
		default:
			h.service.Pass(context.Background())
			time.Sleep(time.Millisecond)
		}
	}
	ticket := h.store.allTickets()[0]
	h.router.answer = actions("Passed it on.", fmt.Sprintf(`{"action":"attach","ticket_id":%q}`, ticket.ID))
	h.ingest("alice", "github:o/r#1", "make it JSON Lines", true)
	for h.disposition(1).Disposition == core.SuperAgentDispositionPending {
		h.service.Pass(context.Background())
	}
	close(release)
	h.settle()
	steering.mu.Lock()
	steered := slices.Clone(steering.steered)
	steering.mu.Unlock()
	if len(steered) != 1 || !strings.Contains(steered[0], "make it JSON Lines") {
		t.Fatalf("steered = %v", steered)
	}
	if h.workers.callCount() != 1 {
		t.Fatalf("a steered message needs no second turn: calls = %d", h.workers.callCount())
	}
	if inputs := readText(t, filepath.Join(h.service.existingTicketDir(ticket), ticketInputsFile)); !strings.Contains(inputs, "delivered during the running turn") {
		t.Fatalf("inputs.md must record the steer:\n%s", inputs)
	}
}

type scopeStub struct{ missing []string }

func (s scopeStub) ScopeStatus(context.Context, string, []string, []string) (FeishuScopeStatus, error) {
	return FeishuScopeStatus{Tenant: FeishuScopeGap{Missing: s.missing, ApplyURL: "https://open.feishu.cn/app/cli_x/auth"}}, nil
}

type directoryStub struct{}

func (directoryStub) Chats(context.Context, string) (FeishuChatList, error) {
	return FeishuChatList{Items: []FeishuChat{{ChatID: "oc_team", Name: "Orderly dev"}, {ChatID: "oc_finance", Name: "Orderly finance"}}}, nil
}

func (directoryStub) Members(_ context.Context, _ string, chats []string) (FeishuUserList, error) {
	return FeishuUserList{Items: []FeishuUser{{OpenID: "ou_alice", Name: "Alice", ChatIDs: chats}, {OpenID: "ou_lee", Name: "Lee", ChatIDs: []string{"oc_finance"}}}}, nil
}

func TestFeishuSourceWithoutGroupMessagesWarnsAndTellsTheRouter(t *testing.T) {
	h := newHarness(t)
	h.service.ctx = context.Background() // as after Start, so a save checks scopes
	h.service.scopes = scopeStub{missing: []string{feishuGroupMessages}}
	h.service.directory = directoryStub{}
	h = refeishu(t, h, "oc_team,oc_finance")
	h.service.wg.Wait()
	activity, _ := h.service.Activity(context.Background(), h.agent.ID)
	if warning := activity.Sources["msg"].Warning; !strings.Contains(warning, feishuGroupMessages) || !strings.Contains(warning, "https://open.feishu.cn/app/cli_x/auth") {
		t.Fatalf("warning = %q", warning)
	}
	h.router.answer = func(core.InvocationRequest) (string, error) { return "", nil }
	_, _ = h.service.InterceptMessage(context.Background(), &core.Message{ID: "m1", SourceMessageID: "m1", ChannelID: "ch1", ChatID: "oc_team", UserID: "ou_alice", Text: "status?", MentionedBot: true, ChatType: "group"})
	h.settle()
	prompt := h.router.prompts[0]
	for _, want := range []string{
		"NOTE: Feishu delivers only messages that @mention you",
		`"to": "chat:oc_finance"`, `"name": "Orderly finance"`, `"open_id": "ou_lee"`,
		`"where": "Feishu group \"Orderly dev\", a new message"`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, prompt)
		}
	}

	h.service.scopes = scopeStub{}
	h = refeishu(t, h, "oc_team,oc_finance")
	h.service.wg.Wait()
	if activity, _ := h.service.Activity(context.Background(), h.agent.ID); activity.Sources["msg"].Warning != "" {
		t.Fatalf("a granted scope clears the warning: %q", activity.Sources["msg"].Warning)
	}
}

func TestPostToAFeishuGroup(t *testing.T) {
	h := newHarness(t)
	h.service.directory = directoryStub{}
	h = refeishu(t, h, "oc_team,oc_finance")
	h.router.answer = actions("Asked Lee in the finance group.",
		`{"action":"post","to":"chat:oc_finance","text":"<at user_id=\"ou_lee\">Lee</at> per line or per invoice?"}`)
	_, _ = h.service.InterceptMessage(context.Background(), &core.Message{ID: "m1", SourceMessageID: "m1", ChannelID: "ch1", ChatID: "oc_team", UserID: "ou_alice", Text: "who decides rounding?", MentionedBot: true, ChatType: "group"})
	h.settle()
	if got := h.replies.sentTo(`{"channel_id":"ch1","chat_id":"oc_finance"}`); len(got) != 1 || !strings.Contains(got[0], "ou_lee") {
		t.Fatalf("finance group = %v (all: %v %v)", got, h.replies.sent(), h.replies.targets)
	}
}

// refeishu gives the harness's Super Agent one Feishu message Source.
func refeishu(t *testing.T, h *harness, chats string) *harness {
	t.Helper()
	h.service.channelHasAgent = func(context.Context, string) bool { return true }
	saved, err := h.service.Save(context.Background(), core.SuperAgent{
		ID: h.agent.ID, Name: "sa", RouterAgentID: "router", Enabled: true, Workers: h.agent.Workers,
		Sources: []core.SuperAgentSource{{ID: "msg", Kind: core.SuperAgentSourceFeishuMessage, Enabled: true, AllowedActors: []string{"ou_alice"}, Config: map[string]string{"channel_id": "ch1", "chat_ids": chats}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := h.store.GetSuperAgent(context.Background(), saved.ID)
	h.agent, h.source = *stored, stored.Sources[0]
	return h
}

func TestLinkLetsAnotherThreadHearTheOutcome(t *testing.T) {
	h := newHarness(t)
	release := make(chan struct{})
	h.workers.run = func(context.Context, workerCall) (string, error) {
		<-release
		return "multi-currency done on agentmux/fx", nil
	}
	h.ingestOn("github:o/r#1", "multi-currency", `{"repo":"o/r","number":1}`)
	for len(h.store.allTickets()) == 0 || h.store.allTickets()[0].Status != core.SuperAgentTicketRunning {
		h.service.Pass(context.Background())
	}
	ticket := h.store.allTickets()[0]
	h.router.answer = actions("It is under way; I'll tell you here when it lands.", fmt.Sprintf(`{"action":"link","ticket_id":%q}`, ticket.ID))
	h.ingestOn("github:o/r#5", "will multi-currency make Friday?", `{"repo":"o/r","number":5}`)
	for h.disposition(1).Disposition == core.SuperAgentDispositionPending {
		h.service.Pass(context.Background())
	}
	got, _ := h.store.GetSuperAgentTicket(context.Background(), ticket.ID)
	if len(got.Links) != 1 || got.Links[0].ThreadKey != "github:o/r#5" || len(got.Inputs) != 1 {
		t.Fatalf("a link must not disturb the worker: %+v", got)
	}
	close(release)
	h.settle()
	if h.workers.callCount() != 1 || !strings.Contains(h.router.updatePrompts[0], "github:o/r#5") {
		t.Fatalf("calls = %d, update:\n%s", h.workers.callCount(), h.router.updatePrompts[0])
	}
}

func TestAnAnswerElsewhereFindsItsWayBack(t *testing.T) {
	h := newHarness(t)
	support, finance := `{"repo":"o/r","number":1}`, `{"repo":"o/r","number":9}`
	h.router.answer = actions("Not decided yet; I asked Finance.",
		`{"action":"post","to":"github:o/r#9","text":"@lee can a USD payment be refunded in EUR?"}`)
	h.ingestOn("github:o/r#1", "can Lena get her refund in EUR?", support)
	h.settle()
	if got := h.replies.sentTo(finance); !slices.Equal(got, []string{"@lee can a USD payment be refunded in EUR?"}) {
		t.Fatalf("finance = %v", got)
	}

	h.router.answer = actions("Thanks, noted.", `{"action":"post","to":"github:o/r#1","text":"Finance says yes, at the refund day's rate."}`)
	h.ingestOn("github:o/r#9", "yes, at the refund day's rate", finance)
	h.settle()
	prompt := h.router.prompts[1]
	for _, want := range []string{"QUESTIONS YOU ASKED HERE (on behalf", `"on_behalf_of": "GitHub o/r#1"`, `"to": "github:o/r#1"`, "can a USD payment be refunded in EUR?"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	if got := h.replies.sentTo(support); !slices.Contains(got, "Finance says yes, at the refund day's rate.") {
		t.Fatalf("support = %v", got)
	}

	// Answered asks are settled and stop showing.
	h.router.answer = func(core.InvocationRequest) (string, error) { return "", nil }
	h.ingestOn("github:o/r#9", "anything else?", finance)
	h.settle()
	if strings.Contains(h.router.prompts[2], "QUESTIONS YOU ASKED HERE (on behalf") {
		t.Fatalf("a settled ask must not show again:\n%s", h.router.prompts[2])
	}
}
