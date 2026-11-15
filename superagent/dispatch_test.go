package superagent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

var dispatchTokenPattern = regexp.MustCompile(`amd1\.[A-Za-z0-9_.\-]*`)

// dispatchGates holds the parent's turn and its helpers' turns until the test
// lets them finish.
type dispatchGates struct {
	mu     sync.Mutex
	parent chan struct{}
	helper chan struct{}
	closed map[chan struct{}]bool
	answer string
}

func (g *dispatchGates) open(gate chan struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.closed[gate] {
		g.closed[gate] = true
		close(gate)
	}
}

// withDispatch turns dispatch on and makes workers wait at the gates: the
// Ticket's own turn at parent, helpers at helper.
func (h *harness) withDispatch() *dispatchGates {
	h.t.Helper()
	h.setDispatch(true)
	gates := &dispatchGates{parent: make(chan struct{}), helper: make(chan struct{}), closed: map[chan struct{}]bool{}, answer: "helper found it"}
	h.workers.run = func(ctx context.Context, call workerCall) (string, error) {
		gate, answer := gates.parent, "parent done"
		if strings.HasPrefix(call.conv, "superagent-"+helperIDPrefix) {
			gate, answer = gates.helper, gates.answer
		}
		select {
		case <-gate:
			return answer, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	h.t.Cleanup(func() {
		gates.open(gates.parent)
		gates.open(gates.helper)
		h.service.dispatch.wg.Wait()
	})
	return gates
}

func (h *harness) setDispatch(on bool) {
	h.t.Helper()
	h.agent.Dispatch = on
	if err := h.store.UpsertSuperAgent(context.Background(), h.agent); err != nil {
		h.t.Fatal(err)
	}
}

// ticketToken is the token a Ticket's worker was given in its first input.
func (h *harness) ticketToken(ticket core.SuperAgentTicket) string {
	h.t.Helper()
	for _, call := range h.workerCalls() {
		if call.conv == ticket.ConversationID {
			if token := dispatchTokenPattern.FindString(call.input); token != "" {
				return token
			}
		}
	}
	for _, input := range ticket.Inputs {
		if token := dispatchTokenPattern.FindString(input); token != "" {
			return token
		}
	}
	h.t.Fatalf("ticket %s was given no dispatch token", ticket.ID)
	return ""
}

func (h *harness) workerCalls() []workerCall {
	h.workers.mu.Lock()
	defer h.workers.mu.Unlock()
	return append([]workerCall(nil), h.workers.calls...)
}

func (h *harness) startHelper(token, worker string) (DispatchResult, error) {
	return h.service.DispatchStart(context.Background(), DispatchRequest{Token: token, Worker: worker, Title: "look it up", Instructions: "find the config file and report its path"})
}

// waitHelper waits until a helper's record has the status.
func (h *harness) waitHelper(ticketID, helperID, status string) core.SuperAgentHelper {
	h.t.Helper()
	for range 2000 {
		if helper, ok := findHelper(h.ticket(ticketID), helperID); ok && helper.Status == status {
			return helper
		}
		time.Sleep(time.Millisecond)
	}
	helper, _ := findHelper(h.ticket(ticketID), helperID)
	h.t.Fatalf("helper %s is %q, want %q", helperID, helper.Status, status)
	return core.SuperAgentHelper{}
}

func dispatchStatus(err error) int {
	var refused DispatchError
	if errors.As(err, &refused) {
		return refused.Status
	}
	return 0
}

func TestDispatchIsOffByDefault(t *testing.T) {
	h := newHarness(t)
	release := h.blockWorkers()
	h.ingest("alice", "github:o/r#1", "fix it", true)
	ticket := h.waitRunning()
	input := h.workerCalls()[0].input
	if strings.Contains(input, "HELPERS") || strings.Contains(input, "dispatch") || ticket.DispatchKey != "" {
		t.Fatalf("a worker of an agent without dispatch hears of it:\n%s", input)
	}
	forged := strings.Join([]string{dispatchTokenPrefix, "", ticket.ID, "guess"}, ".")
	if _, err := h.startHelper(forged, "local"); dispatchStatus(err) != http.StatusUnauthorized {
		t.Fatalf("start with a forged token: %v", err)
	}
	release()
	h.settle()
}

func TestDispatchBriefNamesTheCallsAndLimits(t *testing.T) {
	h := newHarness(t)
	gates := h.withDispatch()
	h.service.SetDispatchHost(DispatchHost{Addr: "127.0.0.1:8765", Command: "/usr/local/bin/amux"})
	h.ingest("alice", "github:o/r#1", "fix it", true)
	ticket := h.waitRunning()
	input := h.workerCalls()[0].input
	token := h.ticketToken(ticket)
	for _, want := range []string{
		"/usr/local/bin/amux dispatch start --addr 127.0.0.1:8765 --token " + token,
		"/usr/local/bin/amux dispatch read --addr 127.0.0.1:8765 --token " + token,
		"Workers you may name: local, remote.",
		"at most 3 helpers run at once", "12 are started in all", "cannot start helpers of its own",
		"stopped when your turn ends",
	} {
		if !strings.Contains(input, want) {
			t.Errorf("brief lacks %q:\n%s", want, input)
		}
	}
	if ticket.DispatchKey != dispatchKey(token) || strings.Contains(ticket.DispatchKey, token) {
		t.Fatalf("the Ticket keeps %q for token %q", ticket.DispatchKey, token)
	}
	gates.open(gates.parent)
	h.settle()
}

func TestDispatchBriefWithoutCommandUsesHTTP(t *testing.T) {
	h := newHarness(t)
	h.setDispatch(true)
	h.service.SetDispatchHost(DispatchHost{Addr: "127.0.0.1:12101"})
	h.ingest("alice", "github:o/r#1", "fix it", true)
	h.settle()
	input := h.workerCalls()[0].input
	if !strings.Contains(input, "curl -s -X POST http://127.0.0.1:12101"+DispatchStartPath) || !strings.Contains(input, DispatchReadPath) {
		t.Fatalf("brief:\n%s", input)
	}
}

func TestDispatchBriefForRemoteWorkerNeedsAHub(t *testing.T) {
	h := newHarness(t)
	h.setDispatch(true)
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("remote"), nil }
	h.ingest("alice", "github:o/r#1", "fix it", true)
	h.settle()
	if input := h.workerCalls()[0].input; strings.Contains(input, "HELPERS") {
		t.Fatalf("a worker that cannot reach this machine is offered helpers:\n%s", input)
	}

	home := "dev_agent"
	h.service.SetDispatchHost(DispatchHost{Home: func() string { return home }, Command: "/opt/amux"})
	h.ingest("alice", "github:o/r#2", "fix it too", true)
	h.settle()
	calls := h.workerCalls()
	input := calls[len(calls)-1].input
	if !strings.Contains(input, "amux dispatch start --token amd1.dev_agent.tkt_") || strings.Contains(input, "/opt/amux") || strings.Contains(input, "--addr") {
		t.Fatalf("a remote worker should run its own machine's amux:\n%s", input)
	}
}

func TestDispatchStartRunsHelperAndReadReturnsItsResult(t *testing.T) {
	h := newHarness(t)
	gates := h.withDispatch()
	h.ingest("alice", "github:o/r#1", "fix it", true)
	ticket := h.waitRunning()
	token := h.ticketToken(ticket)

	started, err := h.startHelper(token, "remote")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(started.ID, helperIDPrefix) || started.Status != core.SuperAgentHelperRunning || started.Running != 1 || started.WorkerID != "remote" {
		t.Fatalf("started = %+v", started)
	}
	read, err := h.service.DispatchRead(context.Background(), DispatchRequest{Token: token, ID: started.ID})
	if err != nil || read.Status != core.SuperAgentHelperRunning || read.Result != "" {
		t.Fatalf("read while running = %+v, %v", read, err)
	}

	gates.open(gates.helper)
	read, err = h.service.DispatchRead(context.Background(), DispatchRequest{Token: token, ID: started.ID, WaitSeconds: 5})
	if err != nil || read.Status != core.SuperAgentHelperDone || read.Result != "helper found it" || read.Running != 0 {
		t.Fatalf("read after it finished = %+v, %v", read, err)
	}
	record := h.waitHelper(ticket.ID, started.ID, core.SuperAgentHelperDone)
	if record.Worker.ID != "remote" || record.Worker.HostID != "host-1" || record.FinishedAt.IsZero() {
		t.Fatalf("record = %+v", record)
	}

	// One level: the helper is given no token and is told it cannot dispatch.
	var helperInput string
	for _, call := range h.workerCalls() {
		if call.conv == "superagent-"+started.ID {
			helperInput = call.input
		}
	}
	if helperInput == "" || strings.Contains(helperInput, dispatchTokenPrefix+".") || strings.Contains(helperInput, "dispatch start") ||
		!strings.Contains(helperInput, "You cannot hand work to other workers") || !strings.Contains(helperInput, "find the config file") {
		t.Fatalf("helper input:\n%s", helperInput)
	}
	gates.open(gates.parent)
	h.settle()
}

func TestDispatchLimits(t *testing.T) {
	h := newHarness(t)
	gates := h.withDispatch()
	h.ingest("alice", "github:o/r#1", "fix it", true)
	ticket := h.waitRunning()
	token := h.ticketToken(ticket)

	if _, err := h.startHelper(token, "elsewhere"); dispatchStatus(err) != http.StatusForbidden {
		t.Fatalf("a worker off the roster: %v", err)
	}
	if _, err := h.service.DispatchStart(context.Background(), DispatchRequest{Token: token, Runtime: "codex", Instructions: "x"}); dispatchStatus(err) != http.StatusForbidden {
		t.Fatalf("an auto worker the agent may not launch: %v", err)
	}
	if _, err := h.startHelper(token, ""); dispatchStatus(err) != http.StatusBadRequest {
		t.Fatalf("no worker: %v", err)
	}
	if _, err := h.service.DispatchStart(context.Background(), DispatchRequest{Token: token, Worker: "local"}); dispatchStatus(err) != http.StatusBadRequest {
		t.Fatalf("no instructions: %v", err)
	}

	// At most three run at once.
	var running []string
	for range maxRunningHelpers {
		started, err := h.startHelper(token, "local")
		if err != nil {
			t.Fatal(err)
		}
		running = append(running, started.ID)
	}
	if _, err := h.startHelper(token, "local"); dispatchStatus(err) != http.StatusConflict {
		t.Fatalf("a fourth running helper: %v", err)
	}

	// Twelve in all.
	gates.open(gates.helper)
	for _, id := range running {
		h.waitHelper(ticket.ID, id, core.SuperAgentHelperDone)
	}
	for range maxTicketHelpers - maxRunningHelpers {
		started, err := h.startHelper(token, "local")
		if err != nil {
			t.Fatal(err)
		}
		h.waitHelper(ticket.ID, started.ID, core.SuperAgentHelperDone)
	}
	if _, err := h.startHelper(token, "local"); dispatchStatus(err) != http.StatusConflict {
		t.Fatalf("a thirteenth helper: %v", err)
	}
	if got := len(h.ticket(ticket.ID).Helpers); got != maxTicketHelpers {
		t.Fatalf("helpers recorded = %d", got)
	}

	// Turning dispatch off refuses further calls at once.
	h.setDispatch(false)
	if _, err := h.service.DispatchRead(context.Background(), DispatchRequest{Token: token, ID: running[0]}); dispatchStatus(err) != http.StatusForbidden {
		t.Fatalf("read with dispatch off: %v", err)
	}
	h.setDispatch(true)

	// Only while the Ticket's turn runs.
	gates.open(gates.parent)
	h.settle()
	if _, err := h.startHelper(token, "local"); dispatchStatus(err) != http.StatusConflict {
		t.Fatalf("start after the turn ended: %v", err)
	}
	if read, err := h.service.DispatchRead(context.Background(), DispatchRequest{Token: token, ID: running[0]}); err != nil || read.Result != "helper found it" {
		t.Fatalf("a finished helper stays readable: %+v, %v", read, err)
	}
}

func TestDispatchTokenIsBoundToItsTicket(t *testing.T) {
	h := newHarness(t)
	gates := h.withDispatch()
	h.agent.MaxConcurrency = 1
	h.setDispatch(true)
	h.ingest("alice", "github:o/r#1", "fix it", true)
	first := h.waitRunning()
	firstToken := h.ticketToken(first)
	h.ingest("alice", "github:o/r#2", "and this", true)
	var second core.SuperAgentTicket
	for range 2000 {
		h.service.Pass(context.Background())
		if queued, _ := h.store.ListSuperAgentTicketsByStatus(context.Background(), core.SuperAgentTicketQueued); len(queued) > 0 {
			second = queued[0]
			break
		}
		time.Sleep(time.Millisecond)
	}
	if second.ID == "" {
		t.Fatal("no second ticket")
	}
	secondToken := h.ticketToken(second)
	if firstToken == secondToken {
		t.Fatal("two Tickets share a token")
	}

	helper, err := h.startHelper(firstToken, "local")
	if err != nil {
		t.Fatal(err)
	}
	// Another Ticket's token cannot read this Ticket's helper, nor start one
	// while that Ticket is not running.
	if _, err := h.service.DispatchRead(context.Background(), DispatchRequest{Token: secondToken, ID: helper.ID}); dispatchStatus(err) != http.StatusNotFound {
		t.Fatalf("read another Ticket's helper: %v", err)
	}
	if _, err := h.startHelper(secondToken, "local"); dispatchStatus(err) != http.StatusConflict {
		t.Fatalf("start for a queued Ticket: %v", err)
	}
	// A token whose Ticket id is swapped for another's is refused.
	swapped := strings.Replace(firstToken, first.ID, second.ID, 1)
	if _, err := h.startHelper(swapped, "local"); dispatchStatus(err) != http.StatusUnauthorized {
		t.Fatalf("swapped Ticket id: %v", err)
	}
	// A token naming a helper is no token: helpers cannot dispatch.
	asHelper := strings.Replace(firstToken, first.ID, helper.ID, 1)
	if _, err := h.startHelper(asHelper, "local"); dispatchStatus(err) != http.StatusUnauthorized {
		t.Fatalf("token naming a helper: %v", err)
	}
	// The agent is the Ticket's: a token for another agent's Ticket names
	// that agent, whose dispatch setting and roster apply.
	other := h.agent
	other.ID, other.Dispatch = "other-agent", false
	if err := h.store.UpsertSuperAgent(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if err := h.service.quietlyMutateTicket(context.Background(), second.ID, func(ticket *core.SuperAgentTicket) bool {
		ticket.SuperAgentID = other.ID
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.startHelper(secondToken, "local"); dispatchStatus(err) != http.StatusForbidden {
		t.Fatalf("token for a Ticket of an agent without dispatch: %v", err)
	}
	gates.open(gates.helper)
	gates.open(gates.parent)
}

func TestHelpersStopWhenTheParentTurnEnds(t *testing.T) {
	h := newHarness(t)
	gates := h.withDispatch()
	h.ingest("alice", "github:o/r#1", "fix it", true)
	ticket := h.waitRunning()
	token := h.ticketToken(ticket)
	helper, err := h.startHelper(token, "local")
	if err != nil {
		t.Fatal(err)
	}
	gates.open(gates.parent)
	stopped := h.waitHelper(ticket.ID, helper.ID, core.SuperAgentHelperStopped)
	if stopped.Error != "the parent Ticket's turn ended" || stopped.FinishedAt.IsZero() {
		t.Fatalf("helper = %+v", stopped)
	}
	h.settle()
	h.service.dispatch.mu.Lock()
	left := len(h.service.dispatch.running)
	h.service.dispatch.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d Tickets still have helpers in memory", left)
	}
}

func TestHelpersStopWithTheirTicket(t *testing.T) {
	h := newHarness(t)
	h.withDispatch()
	h.ingest("alice", "github:o/r#1", "fix it", true)
	ticket := h.waitRunning()
	helper, err := h.startHelper(h.ticketToken(ticket), "local")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.service.StopTicket(context.Background(), ticket.ID); err != nil {
		t.Fatal(err)
	}
	h.waitHelper(ticket.ID, helper.ID, core.SuperAgentHelperStopped)
	h.settle()
}

func TestStaleRunningHelperIsClosed(t *testing.T) {
	h := newHarness(t)
	gates := h.withDispatch()
	h.ingest("alice", "github:o/r#1", "fix it", true)
	ticket := h.waitRunning()
	token := h.ticketToken(ticket)
	// A helper a daemon that stopped left marked running.
	stale := core.SuperAgentHelper{ID: helperIDPrefix + "stale", WorkerID: "local", Status: core.SuperAgentHelperRunning, Instructions: "x"}
	if err := h.service.quietlyMutateTicket(context.Background(), ticket.ID, func(current *core.SuperAgentTicket) bool {
		current.Helpers = append(current.Helpers, stale)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	read, err := h.service.DispatchRead(context.Background(), DispatchRequest{Token: token, ID: stale.ID})
	if err != nil || read.Status != core.SuperAgentHelperStopped {
		t.Fatalf("read = %+v, %v", read, err)
	}
	gates.open(gates.parent)
	h.waitHelper(ticket.ID, stale.ID, core.SuperAgentHelperStopped)
	h.settle()
}

func TestBrainSeesRunningHelpers(t *testing.T) {
	h := newHarness(t)
	gates := h.withDispatch()
	trigger := h.ingest("alice", "github:o/r#1", "fix it", true)
	ticket := h.waitRunning()
	helper, err := h.startHelper(h.ticketToken(ticket), "local")
	if err != nil {
		t.Fatal(err)
	}
	trigger.ThreadKey = "github:o/r#1"
	view := h.service.routerView(context.Background(), h.agent, trigger, []core.SuperAgentTicket{h.ticket(ticket.ID)})
	if len(view.Tickets) != 1 || len(view.Tickets[0].Helpers) != 1 || !strings.Contains(view.Tickets[0].Helpers[0], helper.ID+" on local, running: look it up") {
		t.Fatalf("view = %+v", view.Tickets)
	}
	gates.open(gates.helper)
	h.waitHelper(ticket.ID, helper.ID, core.SuperAgentHelperDone)
	view = h.service.routerView(context.Background(), h.agent, trigger, []core.SuperAgentTicket{h.ticket(ticket.ID)})
	if len(view.Tickets[0].Helpers) != 0 {
		t.Fatalf("a finished helper is still shown: %+v", view.Tickets[0].Helpers)
	}
	gates.open(gates.parent)
	h.settle()
}

type uplinkStub struct {
	mu   sync.Mutex
	urls []string
}

func (u *uplinkStub) Uplink() http.RoundTripper { return u }

func (u *uplinkStub) RoundTrip(request *http.Request) (*http.Response, error) {
	u.mu.Lock()
	u.urls = append(u.urls, request.URL.String())
	u.mu.Unlock()
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"id":"hlp_x","status":"running"}`)), Header: http.Header{}}, nil
}

func TestDispatchForTicketElsewhereGoesUpTheHubLink(t *testing.T) {
	h := newHarness(t)
	uplink := &uplinkStub{}
	h.service.SetDispatchHost(DispatchHost{Home: func() string { return "dev_worker" }, Uplink: uplink})

	_, err := h.service.Dispatch(context.Background(), DispatchCallStart, DispatchRequest{Token: "amd1.dev_agent.tkt_elsewhere.secret", Worker: "local", Instructions: "x"})
	var elsewhere DispatchElsewhere
	if !errors.As(err, &elsewhere) || elsewhere.Home != "dev_agent" {
		t.Fatalf("err = %v", err)
	}
	// A token naming this machine for a Ticket it does not have is refused.
	if _, err := h.service.Dispatch(context.Background(), DispatchCallRead, DispatchRequest{Token: "amd1.dev_worker.tkt_gone.secret"}); dispatchStatus(err) != http.StatusUnauthorized {
		t.Fatalf("err = %v", err)
	}

	status, body, err := h.service.ForwardDispatch(context.Background(), DispatchCallStart, "dev_agent", []byte(`{}`))
	if err != nil || status != http.StatusOK || !strings.Contains(string(body), "hlp_x") {
		t.Fatalf("forward = %d %s %v", status, body, err)
	}
	if _, _, err := h.service.ForwardDispatch(context.Background(), DispatchCallRead, DispatchHomeHub, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	want := []string{"http://hub" + WorkerRelayPrefix + "dev_agent" + DispatchStartPath, "http://hub" + DispatchUplinkReadPath}
	if strings.Join(uplink.urls, " ") != strings.Join(want, " ") {
		t.Fatalf("urls = %v", uplink.urls)
	}
	if !RelayAllowed(http.MethodPost, DispatchStartPath) || !RelayAllowed(http.MethodPost, DispatchReadPath) {
		t.Fatal("the worker relay must carry dispatch calls")
	}

	h.service.SetDispatchHost(DispatchHost{Home: func() string { return "dev_worker" }})
	if _, _, err := h.service.ForwardDispatch(context.Background(), DispatchCallStart, "dev_agent", []byte(`{}`)); err == nil {
		t.Fatal("a machine without a hub forwarded a call")
	}
}

func TestParseDispatchToken(t *testing.T) {
	for token, want := range map[string]dispatchToken{
		"amd1..tkt_1.abc":            {home: "", ticketID: "tkt_1"},
		"amd1.hub.tkt_1.abc":         {home: "hub", ticketID: "tkt_1"},
		"amd1.dev_a.b.tkt_1.abc-_x":  {home: "dev_a.b", ticketID: "tkt_1"},
		" amd1.dev_a.tkt_2.secret\n": {home: "dev_a", ticketID: "tkt_2"},
	} {
		if got, ok := parseDispatchToken(token); !ok || got != want {
			t.Errorf("parse %q = %+v, %v", token, got, ok)
		}
	}
	for _, token := range []string{"", "amd1.tkt_1", "amd1..tkt_1.", "amd1..hlp_1.abc", "amd2..tkt_1.abc", "tkt_1"} {
		if _, ok := parseDispatchToken(token); ok {
			t.Errorf("parsed %q", token)
		}
	}
}
