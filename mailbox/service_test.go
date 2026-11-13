package mailbox

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

type fakeDirectory struct {
	parties []core.MailParty
	devices map[string][2]string // id -> owner, name
}

func (d *fakeDirectory) Parties(context.Context) ([]core.MailParty, error) { return d.parties, nil }

func (d *fakeDirectory) Device(_ context.Context, id string) (string, string, bool) {
	device, ok := d.devices[id]
	return device[0], device[1], ok
}

type delivery struct {
	where   string
	message core.MailMessage
}

// fakeWorld records where messages go: this machine's agents, its people,
// the machines linked to it and the hub it joined.
type fakeWorld struct {
	mu        sync.Mutex
	got       []delivery
	agentErr  error
	deviceErr error
	hubErr    error
	joined    bool
	remote    []core.MailParty
}

func (w *fakeWorld) record(where string, message core.MailMessage) {
	w.mu.Lock()
	w.got = append(w.got, delivery{where, message})
	w.mu.Unlock()
}

func (w *fakeWorld) deliveries() []delivery {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]delivery(nil), w.got...)
}

func (w *fakeWorld) ReceiveMail(_ context.Context, agentID string, message core.MailMessage) error {
	if w.agentErr != nil {
		return w.agentErr
	}
	w.record("agent "+agentID, message)
	return nil
}

func (w *fakeWorld) Deliver(_ context.Context, deviceID string, message core.MailMessage) error {
	if w.deviceErr != nil {
		return w.deviceErr
	}
	w.record("device "+deviceID, message)
	return nil
}

func (w *fakeWorld) Joined() bool { return w.joined }

func (w *fakeWorld) Send(_ context.Context, message core.MailMessage) error {
	if w.hubErr != nil {
		return w.hubErr
	}
	w.record("hub", message)
	return nil
}

func (w *fakeWorld) Reachable(context.Context, string) ([]core.MailParty, error) {
	return w.remote, nil
}

type fixture struct {
	t       *testing.T
	service *Service
	store   *store.Store
	dir     *fakeDirectory
	world   *fakeWorld
	clock   time.Time
}

func agent(id, owner string) core.MailParty {
	return core.MailParty{Address: "agent:" + id, Kind: core.MailPartyAgent, Name: strings.ToUpper(id[:1]) + id[1:], Owner: owner, Online: true}
}

func person(login, owner string, admin bool) core.MailParty {
	return core.MailParty{Address: "person:" + login, Kind: core.MailPartyPerson, Name: login, Owner: owner, Admin: admin, Online: true}
}

func newFixture(t *testing.T, parties ...core.MailParty) *fixture {
	t.Helper()
	st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "mail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f := &fixture{
		t: t, store: st, world: &fakeWorld{}, clock: time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC),
		dir: &fakeDirectory{parties: parties, devices: map[string][2]string{}},
	}
	f.service = New(Options{
		Store: st, Directory: f.dir, Agents: f.world, Devices: f.world, Hub: f.world,
		Notify: func(_ context.Context, login string, message core.MailMessage) {
			f.world.record("person "+login, message)
		},
		Now: func() time.Time { f.clock = f.clock.Add(time.Second); return f.clock },
	})
	return f
}

func (f *fixture) send(from, to, text string) (core.MailMessage, error) {
	return f.service.Send(context.Background(), from, to, text)
}

func (f *fixture) mustSend(from, to, text string) core.MailMessage {
	f.t.Helper()
	message, err := f.send(from, to, text)
	if err != nil {
		f.t.Fatalf("send %s -> %s: %v", from, to, err)
	}
	return message
}

var (
	root  = Viewer{Address: "person:root", Name: "root", Admin: true}
	alice = Viewer{Address: "person:alice", Name: "alice", Owner: "ten_alice"}
	bob   = Viewer{Address: "person:bob", Name: "bob", Owner: "ten_bob"}
)

func TestAgentsOfOneOwnerWriteToEachOther(t *testing.T) {
	f := newFixture(t, agent("planner", "ten_alice"), agent("builder", "ten_alice"))
	message := f.mustSend("agent:planner", "agent:builder", "  Start the release build.  ")
	if message.DeliveredAt == nil || message.FromName != "Planner" || message.ToName != "Builder" || message.Text != "Start the release build." {
		t.Fatalf("message = %+v", message)
	}
	got := f.world.deliveries()
	if len(got) != 1 || got[0].where != "agent builder" || got[0].message.From != "agent:planner" {
		t.Fatalf("deliveries = %+v", got)
	}
}

func TestDifferentOwnersNeedAnAcceptedContact(t *testing.T) {
	f := newFixture(t, agent("planner", "ten_alice"), agent("reviewer", "ten_bob"), person("alice", "ten_alice", false), person("bob", "ten_bob", false))
	ctx := context.Background()
	if _, err := f.send("agent:planner", "agent:reviewer", "hi"); !IsRefusal(err) || !strings.Contains(err.Error(), "not contacts") {
		t.Fatalf("strangers must be refused: %v", err)
	}
	// Alice asks; the contact waits for Bob.
	contact, err := f.service.AddContact(ctx, alice, "agent:planner", "agent:reviewer")
	if err != nil || contact.Status != core.MailContactPending || contact.WaitingFor != "agent:reviewer" {
		t.Fatalf("contact = %+v, %v", contact, err)
	}
	if _, err := f.send("agent:planner", "agent:reviewer", "hi"); !IsRefusal(err) {
		t.Fatalf("a pending contact carries nothing: %v", err)
	}
	if _, err := f.service.AcceptContact(ctx, alice, contact.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the asker cannot accept for the other side: %v", err)
	}
	if accepted, err := f.service.AcceptContact(ctx, bob, contact.ID); err != nil || accepted.Status != core.MailContactAccepted {
		t.Fatalf("accept = %+v, %v", accepted, err)
	}
	f.mustSend("agent:planner", "agent:reviewer", "hi")
	f.mustSend("agent:reviewer", "agent:planner", "hello")
	// Either side ends it.
	if err := f.service.RemoveContact(ctx, bob, contact.ID); err != nil {
		t.Fatal(err)
	}
	// What was said stays, so an answer to it is still allowed; a party
	// that was never written to is not.
	f.mustSend("agent:planner", "agent:reviewer", "one more")
	if _, err := f.send("person:alice", "agent:reviewer", "hi"); !IsRefusal(err) {
		t.Fatalf("alice herself was never a contact: %v", err)
	}
}

func TestAskingForAContactTheOtherSideAskedForAcceptsIt(t *testing.T) {
	f := newFixture(t, agent("planner", "ten_alice"), agent("reviewer", "ten_bob"))
	ctx := context.Background()
	if _, err := f.service.AddContact(ctx, alice, "agent:planner", "agent:reviewer"); err != nil {
		t.Fatal(err)
	}
	contact, err := f.service.AddContact(ctx, bob, "agent:reviewer", "agent:planner")
	if err != nil || contact.Status != core.MailContactAccepted {
		t.Fatalf("contact = %+v, %v", contact, err)
	}
	contacts, _ := f.service.Contacts(ctx, root)
	if len(contacts) != 1 {
		t.Fatalf("one contact per pair: %+v", contacts)
	}
}

func TestContactRules(t *testing.T) {
	f := newFixture(t, agent("planner", "ten_alice"), agent("builder", "ten_alice"), agent("reviewer", "ten_bob"),
		person("alice", "ten_alice", false), person("bob", "ten_bob", false))
	ctx := context.Background()
	if _, err := f.service.AddContact(ctx, alice, "agent:planner", "agent:builder"); !IsRefusal(err) {
		t.Fatalf("same owner needs no contact: %v", err)
	}
	if _, err := f.service.AddContact(ctx, root, "person:alice", "person:bob"); !IsRefusal(err) {
		t.Fatalf("people are not contacts of each other: %v", err)
	}
	if _, err := f.service.AddContact(ctx, alice, "agent:reviewer", "person:bob"); !IsRefusal(err) && !errors.Is(err, ErrForbidden) {
		t.Fatalf("alice owns neither side: %v", err)
	}
	// An administrator answers for everyone.
	contact, err := f.service.AddContact(ctx, root, "agent:reviewer", "person:alice")
	if err != nil || contact.Status != core.MailContactAccepted {
		t.Fatalf("contact = %+v, %v", contact, err)
	}
	if seen, _ := f.service.Contacts(ctx, bob); len(seen) != 1 {
		t.Fatalf("bob sees the contact of his agent: %+v", seen)
	}
	f.mustSend("person:alice", "agent:reviewer", "hello")
}

func TestPeopleAndAdministrators(t *testing.T) {
	f := newFixture(t, agent("planner", "ten_alice"), agent("ops", ""), person("alice", "ten_alice", false), person("bob", "ten_bob", false), person("root", "", true))
	if _, err := f.send("person:alice", "person:bob", "hi"); !IsRefusal(err) {
		t.Fatalf("people do not write to people here: %v", err)
	}
	if _, err := f.send("person:bob", "agent:planner", "hi"); !IsRefusal(err) {
		t.Fatalf("bob does not own the planner: %v", err)
	}
	// A person writes to their own agent, and it tells them things.
	f.mustSend("person:alice", "agent:planner", "status?")
	note := f.mustSend("agent:planner", "person:alice", "All green.")
	if note.DeliveredAt == nil {
		t.Fatalf("a message to a person is delivered when it is in their inbox: %+v", note)
	}
	// An administrator writes to anyone, and may be answered.
	if _, err := f.send("agent:planner", "person:root", "hello"); !IsRefusal(err) {
		t.Fatalf("an agent does not start on an administrator: %v", err)
	}
	f.mustSend("person:root", "agent:planner", "who are you?")
	f.mustSend("agent:planner", "person:root", "Alice's planner.")
	// Agents the administrators own may write to them first.
	f.mustSend("agent:ops", "person:root", "disk is full")
	got := f.world.deliveries()
	if last := got[len(got)-1]; last.where != "person root" || last.message.Text != "disk is full" {
		t.Fatalf("deliveries = %+v", got)
	}
}

func TestTwoAgentsCannotTradeMessagesWithoutEnd(t *testing.T) {
	f := newFixture(t, agent("planner", "ten_alice"), agent("builder", "ten_alice"), person("alice", "ten_alice", false))
	for range agentExchangeLimit / 2 {
		f.mustSend("agent:planner", "agent:builder", "ping")
		f.mustSend("agent:builder", "agent:planner", "pong")
	}
	if _, err := f.send("agent:planner", "agent:builder", "ping"); !IsRefusal(err) || !strings.Contains(err.Error(), "a person should look") {
		t.Fatalf("the exchange must stop: %v", err)
	}
	// People are not limited, and the agents may go on after a while.
	f.mustSend("person:alice", "agent:planner", "stop that")
	f.clock = f.clock.Add(agentExchangeWindow)
	f.mustSend("agent:planner", "agent:builder", "ping")
}

func TestAHubRoutesBetweenItsMachines(t *testing.T) {
	onStudio := core.MailParty{Address: "agent:trainer@dev_studio", Kind: core.MailPartyAgent, Name: "Trainer", Owner: "ten_alice", Machine: "studio", Online: true}
	f := newFixture(t, agent("planner", "ten_alice"), onStudio, agent("reviewer", "ten_bob"))
	f.dir.devices = map[string][2]string{"dev_studio": {"ten_alice", "studio"}, "dev_laptop": {"ten_alice", "laptop"}}
	ctx := context.Background()

	// Down a link: addresses are rewritten the way that machine reads them.
	f.mustSend("agent:planner", "agent:trainer@dev_studio", "train it")
	got := f.world.deliveries()
	if len(got) != 1 || got[0].where != "device dev_studio" || got[0].message.From != "agent:planner@hub" || got[0].message.To != "agent:trainer" {
		t.Fatalf("deliveries = %+v", got)
	}

	// Up a link: the link names the machine, whatever the message says.
	up := core.MailMessage{ID: "mail_up", From: "agent:writer", FromName: "Writer", To: "agent:planner@hub", Text: "draft is ready"}
	if err := f.service.AcceptFromDevice(ctx, "dev_laptop", up); err != nil {
		t.Fatal(err)
	}
	got = f.world.deliveries()
	if last := got[len(got)-1]; last.where != "agent planner" || last.message.From != "agent:writer@dev_laptop" || last.message.To != "agent:planner" {
		t.Fatalf("deliveries = %+v", got)
	}
	// Sent again, it is kept once.
	if err := f.service.AcceptFromDevice(ctx, "dev_laptop", up); err != nil || len(f.world.deliveries()) != len(got) {
		t.Fatalf("redelivery: %v, %+v", err, f.world.deliveries())
	}
	// Between two machines, through the hub.
	across := core.MailMessage{ID: "mail_across", From: "agent:writer", To: "agent:trainer@dev_studio", Text: "use the new data"}
	if err := f.service.AcceptFromDevice(ctx, "dev_laptop", across); err != nil {
		t.Fatal(err)
	}
	got = f.world.deliveries()
	if last := got[len(got)-1]; last.where != "device dev_studio" || last.message.From != "agent:writer@dev_laptop" || last.message.To != "agent:trainer" {
		t.Fatalf("deliveries = %+v", got)
	}

	// A machine speaks only for its own agents, and the hub's rules apply.
	for name, message := range map[string]core.MailMessage{
		"another machine's agent": {ID: "m1", From: "agent:trainer@dev_studio", To: "agent:planner@hub", Text: "x"},
		"a person":                {ID: "m2", From: "person:alice", To: "agent:planner@hub", Text: "x"},
		"not a contact":           {ID: "m3", From: "agent:writer", To: "agent:reviewer@hub", Text: "x"},
		"nobody there":            {ID: "m4", From: "agent:writer", To: "agent:ghost@hub", Text: "x"},
	} {
		if err := f.service.AcceptFromDevice(ctx, "dev_laptop", message); !IsRefusal(err) {
			t.Fatalf("%s must be refused: %v", name, err)
		}
	}
}

func TestMailWaitsForAMachineThatIsOffline(t *testing.T) {
	onStudio := core.MailParty{Address: "agent:trainer@dev_studio", Kind: core.MailPartyAgent, Name: "Trainer", Owner: "ten_alice", Machine: "studio"}
	f := newFixture(t, agent("planner", "ten_alice"), onStudio)
	f.dir.devices = map[string][2]string{"dev_studio": {"ten_alice", "studio"}}
	ctx := context.Background()
	f.world.deviceErr = errors.New("device is offline")
	message := f.mustSend("agent:planner", "agent:trainer@dev_studio", "train it")
	if message.DeliveredAt != nil || message.Attempts != 1 || !strings.Contains(message.Error, "offline") {
		t.Fatalf("message = %+v", message)
	}
	f.service.Retry(ctx)
	if len(f.world.deliveries()) != 0 {
		t.Fatalf("deliveries = %+v", f.world.deliveries())
	}
	f.world.deviceErr = nil
	f.service.Retry(ctx)
	stored, _ := f.store.GetMailMessage(ctx, message.ID)
	if got := f.world.deliveries(); len(got) != 1 || stored.DeliveredAt == nil || stored.Error != "" {
		t.Fatalf("deliveries = %+v, stored = %+v", got, stored)
	}
	f.service.Retry(ctx)
	if len(f.world.deliveries()) != 1 {
		t.Fatal("a delivered message is not sent again")
	}

	// A machine that never returns: the message is given up after a week.
	f.world.deviceErr = errors.New("device is offline")
	lost := f.mustSend("agent:planner", "agent:trainer@dev_studio", "still there?")
	f.clock = f.clock.Add(giveUpAfter + time.Hour)
	f.service.Retry(ctx)
	if stored, _ := f.store.GetMailMessage(ctx, lost.ID); stored.FailedAt == nil || !strings.Contains(stored.Error, "7 days") {
		t.Fatalf("stored = %+v", stored)
	}
}

func TestANodeSendsThroughItsHub(t *testing.T) {
	f := newFixture(t, agent("writer", ""), person("carol", "", true))
	ctx := context.Background()
	if _, err := f.send("agent:writer", "agent:planner@hub", "hi"); !IsRefusal(err) {
		t.Fatalf("a machine without a hub has nowhere to send it: %v", err)
	}
	f.world.joined = true
	f.world.remote = []core.MailParty{{Address: "agent:planner@hub", Kind: core.MailPartyAgent, Name: "Planner", Machine: "the hub", Online: true}}

	message := f.mustSend("agent:writer", "agent:planner@hub", "draft is ready")
	got := f.world.deliveries()
	if len(got) != 1 || got[0].where != "hub" || got[0].message.To != "agent:planner@hub" || message.ToName != "Planner" || message.DeliveredAt == nil {
		t.Fatalf("deliveries = %+v, message = %+v", got, message)
	}
	// The hub's no is final and reaches the sender.
	f.world.hubErr = Refuse("Writer and Reviewer are not contacts")
	if _, err := f.send("agent:writer", "agent:reviewer@hub", "hi"); !IsRefusal(err) {
		t.Fatalf("the hub's refusal must come back: %v", err)
	}
	// A link that is down only delays it.
	f.world.hubErr = errors.New("device is offline")
	queued := f.mustSend("agent:writer", "agent:planner@hub", "second draft")
	if queued.DeliveredAt != nil {
		t.Fatalf("queued = %+v", queued)
	}
	f.world.hubErr = nil
	f.service.Retry(ctx)
	if stored, _ := f.store.GetMailMessage(ctx, queued.ID); stored.DeliveredAt == nil {
		t.Fatalf("stored = %+v", stored)
	}
	// People on a node use the hub's Console for the hub's agents.
	if _, err := f.send("person:carol", "agent:planner@hub", "hi"); !IsRefusal(err) {
		t.Fatalf("a node's person does not write through the link: %v", err)
	}
	// Who the agent can reach includes what the hub allows.
	reachable, err := f.service.Reachable(ctx, "agent:writer")
	if err != nil || len(reachable) != 2 || reachable[1].Address != "agent:planner@hub" {
		t.Fatalf("reachable = %+v, %v", reachable, err)
	}
}

func TestANodeTakesWhatItsHubPassesDown(t *testing.T) {
	f := newFixture(t, agent("writer", ""))
	ctx := context.Background()
	message := core.MailMessage{ID: "mail_down", From: "person:alice@hub", FromName: "Alice", To: "agent:writer", Text: "write it up"}
	for range 2 {
		if err := f.service.ReceiveFromHub(ctx, message); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.world.deliveries(); len(got) != 1 || got[0].where != "agent writer" {
		t.Fatalf("deliveries = %+v", got)
	}
	// The agent answers the person on the hub, through the hub.
	f.world.joined = true
	f.mustSend("agent:writer", "person:alice@hub", "done")
	if got := f.world.deliveries(); got[len(got)-1].where != "hub" {
		t.Fatalf("deliveries = %+v", got)
	}
	for name, bad := range map[string]core.MailMessage{
		"a local sender":   {ID: "d1", From: "agent:other", To: "agent:writer", Text: "x"},
		"a remote address": {ID: "d2", From: "agent:x@hub", To: "agent:y@dev_2", Text: "x"},
		"no text":          {ID: "d3", From: "agent:x@hub", To: "agent:writer"},
	} {
		if err := f.service.ReceiveFromHub(ctx, bad); !IsRefusal(err) {
			t.Fatalf("%s must be refused: %v", name, err)
		}
	}
	f.world.agentErr = Refuse("there is no agent")
	if err := f.service.ReceiveFromHub(ctx, core.MailMessage{ID: "d4", From: "agent:x@hub", To: "agent:gone", Text: "x"}); !IsRefusal(err) {
		t.Fatalf("a missing agent must be refused: %v", err)
	}
}

func TestWhoAnAgentOnALinkedMachineCanReach(t *testing.T) {
	onStudio := core.MailParty{Address: "agent:trainer@dev_studio", Kind: core.MailPartyAgent, Name: "Trainer", Owner: "ten_alice", Machine: "studio", Online: true}
	onLaptop := core.MailParty{Address: "agent:writer@dev_laptop", Kind: core.MailPartyAgent, Name: "Writer", Owner: "ten_alice", Machine: "laptop", Online: true}
	f := newFixture(t, agent("planner", "ten_alice"), agent("reviewer", "ten_bob"), person("alice", "ten_alice", false), onStudio, onLaptop)
	f.dir.devices = map[string][2]string{"dev_studio": {"ten_alice", "studio"}, "dev_laptop": {"ten_alice", "laptop"}}
	parties, err := f.service.ReachableFromDevice(context.Background(), "dev_laptop", "writer")
	if err != nil {
		t.Fatal(err)
	}
	var addresses []string
	for _, party := range parties {
		addresses = append(addresses, party.Address)
	}
	if got := strings.Join(addresses, " "); got != "agent:planner@hub person:alice@hub agent:trainer@dev_studio" {
		t.Fatalf("reachable = %s", got)
	}
	if parties[0].Machine != "the hub" || parties[2].Machine != "studio" {
		t.Fatalf("parties = %+v", parties)
	}
}

func TestWhatAPersonSeesInTheConsole(t *testing.T) {
	f := newFixture(t, agent("planner", "ten_alice"), agent("builder", "ten_alice"), agent("reviewer", "ten_bob"),
		person("alice", "ten_alice", false), person("bob", "ten_bob", false), person("root", "", true))
	ctx := context.Background()
	f.mustSend("person:alice", "agent:planner", "status?")
	f.mustSend("agent:planner", "person:alice", "All green.")
	f.mustSend("agent:planner", "person:alice", "Deploy is next.")
	f.mustSend("agent:planner", "agent:builder", "build it")
	f.mustSend("person:bob", "agent:reviewer", "hello")

	mine, err := f.service.Conversations(ctx, alice, ScopeMine)
	if err != nil || len(mine) != 1 || mine[0].Peer.Address != "agent:planner" || mine[0].Unread != 2 || mine[0].Messages != 3 || mine[0].Last.Text != "Deploy is next." {
		t.Fatalf("mine = %+v, %v", mine, err)
	}
	if unread, _ := f.service.Unread(ctx, alice); unread != 2 {
		t.Fatalf("unread = %d", unread)
	}
	// Alice watches her agents talk; Bob's are not hers to read.
	agents, _ := f.service.Conversations(ctx, alice, ScopeAgents)
	if len(agents) != 1 || core.MailPair(agents[0].Self.Address, agents[0].Peer.Address) != core.MailPair("agent:planner", "agent:builder") {
		t.Fatalf("agents = %+v", agents)
	}
	if all, _ := f.service.Conversations(ctx, root, ScopeAgents); len(all) != 3 {
		t.Fatalf("an administrator sees every conversation: %+v", all)
	}
	if _, err := f.service.Messages(ctx, alice, "agent:reviewer", "person:bob"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("alice must not read bob's messages: %v", err)
	}
	if watched, err := f.service.Messages(ctx, alice, "agent:planner", "agent:builder"); err != nil || len(watched) != 1 {
		t.Fatalf("watched = %+v, %v", watched, err)
	}
	// Reading her own conversation marks it read; watching does not.
	messages, err := f.service.Messages(ctx, alice, "agent:planner", "person:alice")
	if err != nil || len(messages) != 3 || messages[0].Text != "status?" {
		t.Fatalf("messages = %+v, %v", messages, err)
	}
	if unread, _ := f.service.Unread(ctx, alice); unread != 0 {
		t.Fatalf("unread after reading = %d", unread)
	}
	parties, reachable, err := f.service.Directory(ctx, alice)
	if err != nil || len(parties) != 5 || strings.Join(reachable, " ") != "agent:builder agent:planner" {
		t.Fatalf("directory = %+v reachable = %v, %v", parties, reachable, err)
	}
	if controlled, _ := f.service.Controlled(ctx, alice); strings.Join(controlled, " ") != "agent:builder agent:planner person:alice" {
		t.Fatalf("controlled = %v", controlled)
	}
}

func TestAddressesAndViews(t *testing.T) {
	for value, want := range map[string]core.MailAddress{
		"agent:planner":         {Kind: "agent", ID: "planner"},
		" person:alice@hub ":    {Kind: "person", ID: "alice", Machine: "hub"},
		"agent:writer@dev_1":    {Kind: "agent", ID: "writer", Machine: "dev_1"},
		"agent:with:colon@dev1": {Kind: "agent", ID: "with:colon", Machine: "dev1"},
	} {
		if got, ok := core.ParseMailAddress(value); !ok || got != want {
			t.Fatalf("%q = %+v, %v", value, got, ok)
		}
	}
	for _, bad := range []string{"", "planner", "robot:x", "agent:", "agent:a@", "agent:a b", "agent:a@b@c"} {
		if _, ok := core.ParseMailAddress(bad); ok {
			t.Fatalf("%q must not parse", bad)
		}
	}
	if got := addressFromHub("agent:writer", "dev_1"); got != "agent:writer@dev_1" {
		t.Fatal(got)
	}
	if got := addressFromHub("person:alice@hub", "dev_1"); got != "person:alice" {
		t.Fatal(got)
	}
	if got := addressFromDevice("agent:writer@dev_1", "dev_1"); got != "agent:writer" {
		t.Fatal(got)
	}
	if got := addressFromDevice("agent:planner", "dev_1"); got != "agent:planner@hub" {
		t.Fatal(got)
	}
	if got := addressFromDevice("agent:trainer@dev_2", "dev_1"); got != "agent:trainer@dev_2" {
		t.Fatal(got)
	}
}
