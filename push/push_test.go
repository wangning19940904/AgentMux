package push

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
	"github.com/wangning19940904/AgentMux/superagent"
)

// fakeClock runs timers when the test advances time.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at      time.Time
	f       func()
	stopped bool
	fired   bool
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		pending := !t.stopped && !t.fired
		t.stopped = true
		return pending
	}
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []*fakeTimer
	for _, t := range c.timers {
		if !t.stopped && !t.fired && !t.at.After(c.now) {
			t.fired = true
			due = append(due, t)
		}
	}
	c.mu.Unlock()
	sort.Slice(due, func(i, j int) bool { return due[i].at.Before(due[j].at) })
	for _, t := range due {
		t.f()
	}
}

type batches struct {
	mu   sync.Mutex
	list []Batch
}

func (b *batches) emit(batch Batch) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.list = append(b.list, batch)
}

func (b *batches) take() []Batch {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.list
	b.list = nil
	return out
}

func waiting(id, agent string) Event {
	return WaitingEvent("", id, Waiting{Agent: agent}, time.Time{})
}

func TestNotifierCollapsesBursts(t *testing.T) {
	clock := newFakeClock()
	got := &batches{}
	n := newNotifier(time.Minute, clock, got.emit)

	// The first event goes out at once.
	n.Publish(waiting("i1", "Release bot"))
	if sent := got.take(); len(sent) != 1 || len(sent[0].Events) != 1 {
		t.Fatalf("first event: %+v", sent)
	}
	// The rest of the burst is held; a repeat of an ID is ignored.
	n.Publish(waiting("i2", "Docs"))
	n.Publish(waiting("i3", "Docs"))
	n.Publish(waiting("i2", "Docs"))
	// Another kind has its own window.
	n.Publish(TicketEvent(core.SuperAgentTicket{ID: "t1", Status: core.SuperAgentTicketFailed}, "Triage", clock.Now()))
	if sent := got.take(); len(sent) != 1 || sent[0].Kind != core.PushKindTickets {
		t.Fatalf("ticket in a quiet kind should go out at once: %+v", sent)
	}

	clock.Advance(time.Minute)
	sent := got.take()
	if len(sent) != 1 || sent[0].Kind != core.PushKindWaiting || len(sent[0].Events) != 2 {
		t.Fatalf("window end should send one summary of two: %+v", sent)
	}
	summary := sent[0].Render("en")
	if summary.Title != "2 requests are waiting for you" || summary.Body != "Docs" || summary.Tag != "summary:waiting" || summary.URL != "/#sessions" {
		t.Fatalf("summary = %+v", summary)
	}

	// The window stays open after a summary; one more held event goes out
	// alone when it ends, then the kind is quiet again.
	n.Publish(waiting("i4", "Docs"))
	if sent := got.take(); len(sent) != 0 {
		t.Fatalf("held during the follow-up window: %+v", sent)
	}
	clock.Advance(time.Minute)
	if sent := got.take(); len(sent) != 1 || len(sent[0].Events) != 1 || sent[0].Events[0].ID != "waiting:local:i4" {
		t.Fatalf("single held event: %+v", sent)
	}
	clock.Advance(time.Minute)
	n.Publish(waiting("i5", "Docs"))
	if sent := got.take(); len(sent) != 1 {
		t.Fatalf("after a quiet window the next event goes out at once: %+v", sent)
	}
}

func TestNotifierKeepsTheLatestEventPerTag(t *testing.T) {
	clock := newFakeClock()
	got := &batches{}
	n := newNotifier(time.Minute, clock, got.emit)
	n.Publish(MachineEvent("d0", "Other", false, clock.Now()))
	got.take()
	n.Publish(MachineEvent("d1", "Build box", false, clock.Now()))
	clock.Advance(time.Second)
	n.Publish(MachineEvent("d1", "Build box", true, clock.Now()))
	clock.Advance(time.Minute)
	sent := got.take()
	if len(sent) != 1 || len(sent[0].Events) != 1 || !sent[0].Events[0].Machine.Online {
		t.Fatalf("held events for one machine should collapse to the latest: %+v", sent)
	}
	n.Close()
	n.Publish(waiting("late", ""))
	if sent := got.take(); len(sent) != 0 {
		t.Fatalf("closed notifier sent %+v", sent)
	}
}

func TestPresenceWaitsOutBlips(t *testing.T) {
	clock := newFakeClock()
	var events []Event
	p := newPresence(2*time.Minute, clock, func(id string) string { return "Box " + id }, func(e Event) { events = append(events, e) })

	// A reconnect within the grace period says nothing.
	p.Changed("d1", false)
	clock.Advance(time.Minute)
	p.Changed("d1", true)
	clock.Advance(5 * time.Minute)
	if len(events) != 0 {
		t.Fatalf("blip notified: %+v", events)
	}
	// A device that never announced offline says nothing on connect.
	p.Changed("d2", true)
	// Staying away past the grace period is "offline"; returning is "back".
	p.Changed("d1", false)
	clock.Advance(2 * time.Minute)
	p.Changed("d1", false)
	clock.Advance(5 * time.Minute)
	p.Changed("d1", true)
	if len(events) != 2 || events[0].Machine.Online || !events[1].Machine.Online || events[0].Machine.Name != "Box d1" {
		t.Fatalf("events = %+v", events)
	}
	if events[0].Render("en").Title != "Box d1 went offline" || events[1].Render("zh").Title != "Box d1 已重新上线" {
		t.Fatalf("titles = %q / %q", events[0].Render("en").Title, events[1].Render("zh").Title)
	}
	// An unpaired device is forgotten, not reported offline.
	p.Changed("d3", false)
	p.Forget("d3")
	clock.Advance(time.Hour)
	if len(events) != 2 {
		t.Fatalf("forgotten device notified: %+v", events[2:])
	}
}

func TestMessagesNameThingsButCarryNoContent(t *testing.T) {
	at := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	question := WaitingEvent("dev-1", "int-1", Waiting{Question: true, Agent: "Docs", Machine: "Build box"}, at).Render("en")
	if question.Title != "An agent has a question for you" || question.Body != "Docs on Build box" || question.URL != "/#sessions" || question.Tag != "waiting:device:dev-1:int-1" {
		t.Fatalf("question = %+v", question)
	}
	ticket := core.SuperAgentTicket{ID: "t9", Status: core.SuperAgentTicketReady, Title: "rotate the prod key sk-live-123", Output: "secret output"}
	finished := TicketEvent(ticket, "Triage", at).Render("zh")
	if finished.Title != "Super Agent 工单已完成" || finished.Body != "Triage" || finished.URL != "/#super-agents" {
		t.Fatalf("ticket = %+v", finished)
	}
	payload, _ := json.Marshal(finished)
	if strings.Contains(string(payload), "sk-live") || strings.Contains(string(payload), "secret") {
		t.Fatalf("ticket notification leaks content: %s", payload)
	}
	summary := Batch{Kind: core.PushKindMachines, At: at, Events: []Event{
		MachineEvent("a", "A", false, at), MachineEvent("b", "B", false, at), MachineEvent("c", "C", true, at), MachineEvent("d", "D", false, at),
	}}.Render("en")
	if summary.Title != "4 linked machines changed: 3 offline, 1 back" || summary.Body != "A, B, C and 1 more" {
		t.Fatalf("machine summary = %+v", summary)
	}
}

// recordingSender answers with a status per endpoint.
type recordingSender struct {
	mu     sync.Mutex
	status map[string]int
	sent   []sentMessage
}

type sentMessage struct {
	endpoint string
	message  Message
	opts     SendOptions
}

func (r *recordingSender) Send(_ context.Context, sub core.PushSubscription, payload []byte, opts SendOptions) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var message Message
	_ = json.Unmarshal(payload, &message)
	r.sent = append(r.sent, sentMessage{endpoint: sub.Endpoint, message: message, opts: opts})
	if status, ok := r.status[sub.Endpoint]; ok {
		if status < 0 {
			return 0, errors.New("network unreachable")
		}
		return status, nil
	}
	return 201, nil
}

func (r *recordingSender) take() []sentMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.sent
	r.sent = nil
	return out
}

func newTestService(t *testing.T, sender Sender, clock clock) (*Service, *store.Store) {
	t.Helper()
	st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "push.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc, err := New(context.Background(), Options{Store: st, Sender: sender, Window: time.Minute, clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc, st
}

// browserKeys makes a subscription's keys the way a browser does.
func browserKeys(t *testing.T) (p256dh, auth string) {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secret := make([]byte, 16)
	_, _ = rand.Read(secret)
	return base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), base64.RawURLEncoding.EncodeToString(secret)
}

func subscribeRequest(t *testing.T, endpoint string, kinds ...string) SubscribeRequest {
	req := SubscribeRequest{Endpoint: endpoint, Label: "Phone", Lang: "en"}
	req.Keys.P256dh, req.Keys.Auth = browserKeys(t)
	if kinds != nil {
		req.Kinds = &kinds
	}
	return req
}

func TestServiceKeepsOneKeyPair(t *testing.T) {
	st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "push.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	first, err := New(context.Background(), Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
	second, err := New(context.Background(), Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	second.Close()
	key, err := base64.RawURLEncoding.DecodeString(first.PublicKey())
	if err != nil || len(key) != 65 || first.PublicKey() != second.PublicKey() {
		t.Fatalf("public keys %q / %q (%d bytes, %v)", first.PublicKey(), second.PublicKey(), len(key), err)
	}
}

func TestSubscribeValidatesWhatABrowserSends(t *testing.T) {
	svc, _ := newTestService(t, &recordingSender{}, newFakeClock())
	ctx := context.Background()
	good := subscribeRequest(t, "https://fcm.googleapis.com/fcm/send/abc")
	for name, mutate := range map[string]func(*SubscribeRequest){
		"http endpoint": func(r *SubscribeRequest) { r.Endpoint = "http://push.example/x" },
		"no endpoint":   func(r *SubscribeRequest) { r.Endpoint = "" },
		"short key":     func(r *SubscribeRequest) { r.Keys.P256dh = base64.RawURLEncoding.EncodeToString([]byte("short")) },
		"bad auth":      func(r *SubscribeRequest) { r.Keys.Auth = "not base64 !!" },
		"long auth":     func(r *SubscribeRequest) { r.Keys.Auth = base64.RawURLEncoding.EncodeToString(make([]byte, 32)) },
	} {
		req := good
		mutate(&req)
		if _, err := svc.Subscribe(ctx, SubscriberAdmin, req); !errors.Is(err, ErrInvalidSubscription) {
			t.Errorf("%s: err = %v", name, err)
		}
	}

	// Omitted kinds mean all; unknown kinds are dropped; the ID follows the
	// endpoint so subscribing again updates in place.
	sub, err := svc.Subscribe(ctx, SubscriberAdmin, good)
	if err != nil || sub.ID != SubscriptionID(good.Endpoint) || len(sub.Kinds) != 3 || sub.Label != "Phone" {
		t.Fatalf("subscribe = %+v, %v", sub, err)
	}
	again := subscribeRequest(t, good.Endpoint, core.PushKindMachines, "bogus")
	again.Lang = "zh"
	updated, err := svc.Subscribe(ctx, SubscriberAdmin, again)
	if err != nil || updated.ID != sub.ID || len(updated.Kinds) != 1 || updated.Lang != "zh" || !updated.CreatedAt.Equal(sub.CreatedAt) {
		t.Fatalf("resubscribe = %+v, %v", updated, err)
	}
	list, _ := svc.Subscriptions(ctx, SubscriberAdmin)
	if len(list) != 1 {
		t.Fatalf("subscriptions = %+v", list)
	}
	if removed, _ := svc.Unsubscribe(ctx, "tenant:t1", sub.ID); removed {
		t.Fatal("another subscriber removed the subscription")
	}
	if removed, _ := svc.Unsubscribe(ctx, SubscriberAdmin, sub.ID); !removed {
		t.Fatal("unsubscribe failed")
	}
}

func TestDeliveryFollowsKindsAndDropsDeadSubscriptions(t *testing.T) {
	clock := newFakeClock()
	sender := &recordingSender{status: map[string]int{
		"https://push.example/gone":   410,
		"https://push.example/broken": 500,
	}}
	svc, st := newTestService(t, sender, clock)
	ctx := context.Background()
	for _, req := range []SubscribeRequest{
		subscribeRequest(t, "https://push.example/all"),
		subscribeRequest(t, "https://push.example/machines-only", core.PushKindMachines),
		subscribeRequest(t, "https://push.example/gone"),
		subscribeRequest(t, "https://push.example/broken"),
	} {
		if _, err := svc.Subscribe(ctx, SubscriberAdmin, req); err != nil {
			t.Fatal(err)
		}
	}
	tenantReq := subscribeRequest(t, "https://push.example/tenant")
	if _, err := svc.Subscribe(ctx, "tenant:t1", tenantReq); err != nil {
		t.Fatal(err)
	}

	svc.deliver(ctx, Batch{Kind: core.PushKindWaiting, Events: []Event{waiting("i1", "Docs")}}, SubscriberAdmin, "")
	sent := sender.take()
	endpoints := []string{}
	for _, m := range sent {
		endpoints = append(endpoints, m.endpoint)
	}
	sort.Strings(endpoints)
	if strings.Join(endpoints, " ") != "https://push.example/all https://push.example/broken https://push.example/gone" {
		t.Fatalf("waiting went to %v", endpoints)
	}
	if sent[0].opts.TTL != 600 || sent[0].opts.Urgency != "high" || len(sent[0].opts.Topic) != 32 || sent[0].message.Title != "An agent is waiting for your approval" {
		t.Fatalf("message = %+v", sent[0])
	}

	// 410 removes the subscription; 500 is recorded and kept.
	if gone, _ := st.GetPushSubscription(ctx, SubscriptionID("https://push.example/gone")); gone != nil {
		t.Fatalf("gone subscription kept: %+v", gone)
	}
	broken, _ := st.GetPushSubscription(ctx, SubscriptionID("https://push.example/broken"))
	if broken == nil || broken.LastError != "the push service answered 500" {
		t.Fatalf("broken = %+v", broken)
	}
	ok, _ := st.GetPushSubscription(ctx, SubscriptionID("https://push.example/all"))
	if ok.LastSuccessAt.IsZero() {
		t.Fatalf("delivery not recorded: %+v", ok)
	}

	// A test goes to one subscription regardless of its kinds.
	results := svc.SendTest(ctx, SubscriberAdmin, SubscriptionID("https://push.example/machines-only"))
	if len(results) != 1 || !results[0].Delivered || results[0].Label != "Phone" {
		t.Fatalf("test results = %+v", results)
	}
	if sent := sender.take(); len(sent) != 1 || sent[0].message.Kind != "test" || sent[0].message.URL != "/#settings" {
		t.Fatalf("test sent %+v", sent)
	}
	// A tenant's test reaches only the tenant's own subscription.
	if results := svc.SendTest(ctx, "tenant:t1", ""); len(results) != 1 {
		t.Fatalf("tenant test = %+v", results)
	}
	sender.take()

	// Published events take the asynchronous path to the admin's devices.
	svc.Publish(MachineEvent("d1", "Build box", false, clock.Now()))
	deadline := time.Now().Add(2 * time.Second)
	for {
		sent := sender.take()
		if len(sent) > 0 {
			got := map[string]bool{}
			for _, m := range sent {
				got[m.endpoint] = true
			}
			if !got["https://push.example/machines-only"] || !got["https://push.example/all"] || got["https://push.example/tenant"] {
				t.Fatalf("machine event went to %v", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("published event was not delivered")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type fakeLookup struct {
	interactions map[string]*core.ChannelInteraction
}

func (f fakeLookup) GetChannelInteraction(_ context.Context, id string) (*core.ChannelInteraction, error) {
	return f.interactions[id], nil
}
func (fakeLookup) GetChannel(_ context.Context, id string) (*core.Channel, error) {
	return &core.Channel{ID: id, Name: "Feishu bot", AgentID: "agent-1"}, nil
}
func (fakeLookup) GetAgentInstance(_ context.Context, id string) (*core.AgentInstance, error) {
	return &core.AgentInstance{ID: id, Name: "Release bot"}, nil
}

// published captures what reaches the notifier.
func captured(t *testing.T) (*Service, *batches) {
	t.Helper()
	got := &batches{}
	svc := &Service{}
	svc.notifier = newNotifier(time.Minute, newFakeClock(), got.emit)
	return svc, got
}

func waitFor(t *testing.T, got *batches, n int) []Batch {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var all []Batch
	for time.Now().Before(deadline) {
		all = append(all, got.take()...)
		if len(all) >= n {
			return all
		}
		time.Sleep(5 * time.Millisecond)
	}
	return all
}

func TestEngineSinkNotifiesOnlyWhenAPersonIsNeeded(t *testing.T) {
	svc, got := captured(t)
	sink := svc.EngineSink(fakeLookup{interactions: map[string]*core.ChannelInteraction{
		"q1":   {ID: "q1", ChannelID: "ch1", Status: core.ChannelInteractionPending, Request: core.AgentInteraction{Kind: core.AgentInteractionUserInput}},
		"done": {ID: "done", ChannelID: "ch1", Status: core.ChannelInteractionResolved},
	}})
	sink(core.HookPermission, map[string]string{"interaction_id": "g1", "guard_decision": "allow"})
	sink(core.HookTaskCompleted, map[string]string{"interaction_id": "x"})
	sink(core.HookPermission, map[string]string{"interaction_id": "done", "channel_id": "ch1"})
	sink(core.HookPermission, map[string]string{"interaction_id": "q1", "channel_id": "ch1"})
	batches := waitFor(t, got, 1)
	time.Sleep(20 * time.Millisecond)
	batches = append(batches, got.take()...)
	if len(batches) != 1 {
		t.Fatalf("batches = %+v", batches)
	}
	message := batches[0].Render("en")
	if message.Title != "An agent has a question for you" || message.Body != "Release bot" {
		t.Fatalf("message = %+v", message)
	}
}

type memorySink struct {
	applied []core.DeviceReport
}

func (m *memorySink) DeviceReportState(context.Context, string, string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (m *memorySink) ApplyDeviceReport(_ context.Context, _ string, report core.DeviceReport, _ time.Time) error {
	m.applied = append(m.applied, report)
	return nil
}

type fakeNames struct{}

func (fakeNames) DeviceName(_ context.Context, id string) string { return "Build box" }
func (fakeNames) DeviceAgentName(_ context.Context, _, channelID string) string {
	if channelID == "ch1" {
		return "Docs"
	}
	return ""
}

func TestReportSinkNotifiesFromTheHubsCopy(t *testing.T) {
	svc, got := captured(t)
	inner := &memorySink{}
	sink := svc.ReportSink(inner, fakeNames{})
	doc := func(id string, status core.ChannelInteractionStatus) core.DeviceReportDoc {
		data, _ := json.Marshal(core.ChannelInteraction{ID: id, ChannelID: "ch1", Status: status})
		return core.DeviceReportDoc{ID: id, Data: data}
	}
	ctx := context.Background()
	if err := sink.ApplyDeviceReport(ctx, "dev-1", core.DeviceReport{Collection: "agents", Upserts: []core.DeviceReportDoc{doc("a", core.ChannelInteractionPending)}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	report := core.DeviceReport{Collection: PendingInteractionsCollection, Upserts: []core.DeviceReportDoc{
		doc("p1", core.ChannelInteractionPending), doc("r1", core.ChannelInteractionResolved),
	}}
	if err := sink.ApplyDeviceReport(ctx, "dev-1", report, time.Now()); err != nil {
		t.Fatal(err)
	}
	// The same interaction reported again (its message ID changed) is not a
	// second notification.
	if err := sink.ApplyDeviceReport(ctx, "dev-1", report, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(inner.applied) != 3 {
		t.Fatalf("reports must still reach the store: %d", len(inner.applied))
	}
	sent := got.take()
	if len(sent) != 1 {
		t.Fatalf("batches = %+v", sent)
	}
	if message := sent[0].Render("en"); message.Body != "Docs on Build box" || message.Tag != "waiting:device:dev-1:p1" {
		t.Fatalf("message = %+v", message)
	}
}

// ticketMemory is the part of the Super Agent store the wrapper touches.
type ticketMemory struct {
	superagent.Store
	tickets map[string]core.SuperAgentTicket
}

func (m *ticketMemory) UpsertSuperAgentTicket(_ context.Context, ticket core.SuperAgentTicket) error {
	m.tickets[ticket.ID] = ticket
	return nil
}
func (m *ticketMemory) GetSuperAgentTicket(_ context.Context, id string) (*core.SuperAgentTicket, error) {
	ticket, ok := m.tickets[id]
	if !ok {
		return nil, nil
	}
	return &ticket, nil
}
func (m *ticketMemory) GetSuperAgent(_ context.Context, id string) (*core.SuperAgent, error) {
	return &core.SuperAgent{ID: id, Name: "Triage"}, nil
}

func TestTicketStoreNotifiesOnFinalStates(t *testing.T) {
	svc, got := captured(t)
	memory := &ticketMemory{tickets: map[string]core.SuperAgentTicket{}}
	wrapped := svc.TicketStore(memory)
	ctx := context.Background()
	ticket := core.SuperAgentTicket{ID: "t1", SuperAgentID: "sa1", ThreadKey: "k"}
	for _, status := range []string{core.SuperAgentTicketQueued, core.SuperAgentTicketRunning, core.SuperAgentTicketReady, core.SuperAgentTicketReady, core.SuperAgentTicketRunning, core.SuperAgentTicketFailed} {
		ticket.Status = status
		if err := wrapped.UpsertSuperAgentTicket(ctx, ticket); err != nil {
			t.Fatal(err)
		}
		ticket.FinishedAt = ticket.FinishedAt.Add(time.Second)
	}
	sent := got.take()
	if len(sent) != 1 {
		t.Fatalf("first final state should go out at once, the next is held: %+v", sent)
	}
	if message := sent[0].Render("en"); message.Title != "A Super Agent Ticket finished" || message.Body != "Triage" {
		t.Fatalf("message = %+v", message)
	}
	if memory.tickets["t1"].Status != core.SuperAgentTicketFailed {
		t.Fatalf("the store must still be written: %+v", memory.tickets["t1"])
	}
	// ready → running → failed: the failure is held for the window, then sent.
	svc.notifier.clock.(*fakeClock).Advance(time.Minute)
	if sent := got.take(); len(sent) != 1 || sent[0].Render("en").Title != "A Super Agent Ticket failed" {
		t.Fatalf("held failure = %+v", sent)
	}
}

// The Console computes the same ID in the browser (web/src/pwa/pushSupport.ts).
func TestSubscriptionIDMatchesTheConsole(t *testing.T) {
	if got := SubscriptionID("https://web.push.apple.com/QGuQyavXutnMH8"); got != "d8c2f451d1ba492cdf55edeac3a8e3f9" {
		t.Fatalf("SubscriptionID = %s", got)
	}
}
