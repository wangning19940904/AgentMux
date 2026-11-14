package server

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
	"github.com/wangning19940904/AgentMux/superagent"
)

// mailRecords keeps agents and the events they are handed in memory: the
// test database has no tables for them.
type mailRecords struct {
	*agentRecords
	triggers []core.SuperAgentTrigger
}

func (r *mailRecords) InsertSuperAgentTrigger(_ context.Context, trigger core.SuperAgentTrigger) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.triggers {
		if existing.SuperAgentID == trigger.SuperAgentID && existing.DedupeKey == trigger.DedupeKey {
			return false, nil
		}
	}
	r.triggers = append(r.triggers, trigger)
	return true, nil
}

// withAgents gives a test server the agent runtime, unstarted: mail for an
// agent is recorded as an event waiting for its brain.
func withAgents(t *testing.T, s *Server, st *store.Store) *mailRecords {
	t.Helper()
	records := &mailRecords{agentRecords: &agentRecords{Store: st, agents: map[string]core.SuperAgent{}}}
	s.SetSuperAgents(superagent.New(superagent.Options{Store: records, StateDir: t.TempDir()}), nil)
	return records
}

func (r *mailRecords) addAgent(t *testing.T, id, name string) {
	t.Helper()
	ctx := context.Background()
	if err := r.UpsertAgentInstance(ctx, &core.AgentInstance{ID: id, Name: name, RuntimeID: "claudecode", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := r.UpsertSuperAgent(ctx, core.SuperAgent{ID: id, Name: name, RouterAgentID: id, Enabled: true}); err != nil {
		t.Fatal(err)
	}
}

// events are the messages one agent has been handed, oldest first.
func (r *mailRecords) events(agentID string) []core.SuperAgentTrigger {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []core.SuperAgentTrigger
	for _, trigger := range r.triggers {
		if trigger.SuperAgentID == agentID && trigger.SourceKind == core.SuperAgentSourceMailbox {
			out = append(out, trigger)
		}
	}
	return out
}

func TestMembersReachAgentsThroughAcceptedContacts(t *testing.T) {
	host := newAccountHost(t, true)
	records := withAgents(t, host.srv, host.st)
	ctx := context.Background()
	bridge := host.bearer(t, "bridge-secret")
	bridge.mustOK(http.MethodPost, "/api/v1/auth/setup", map[string]string{"login": "root", "password": "root-password-1"}, nil)
	admin := host.browser(t)
	if code := admin.signIn("root", "root-password-1"); code != http.StatusOK {
		t.Fatalf("admin sign-in = %d", code)
	}
	for _, login := range []string{"bob", "carol"} {
		admin.mustOK(http.MethodPost, "/api/v1/accounts", map[string]any{"login": login, "password": login + "-password-1"}, nil)
	}
	bob, carol := host.browser(t), host.browser(t)
	if bob.signIn("bob", "bob-password-1") != http.StatusOK || carol.signIn("carol", "carol-password-1") != http.StatusOK {
		t.Fatal("member sign-in failed")
	}
	// Agents on this machine belong to its administrators.
	records.addAgent(t, "ops", "Ops")

	var overview mailOverview
	bob.mustOK(http.MethodGet, "/api/v1/mailbox", nil, &overview)
	if overview.Me.Address != "person:bob" || overview.Admin || strings.Join(overview.Controlled, " ") != "person:bob" {
		t.Fatalf("overview = %+v", overview)
	}

	// Bob cannot write to the administrators' agent until a contact joins them.
	if code := bob.do(http.MethodPost, "/api/v1/mailbox/messages", map[string]string{"to": "agent:ops", "text": "hi"}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("a stranger's agent = %d", code)
	}
	var contact core.MailContact
	bob.mustOK(http.MethodPost, "/api/v1/mailbox/contacts", map[string]string{"a": "person:bob", "b": "agent:ops"}, &contact)
	if contact.Status != core.MailContactPending || contact.WaitingFor != "agent:ops" || contact.RequestedBy != "person:bob" {
		t.Fatalf("contact = %+v", contact)
	}
	if code := carol.do(http.MethodPost, "/api/v1/mailbox/contacts/accept", map[string]string{"id": contact.ID}, nil); code != http.StatusForbidden {
		t.Fatalf("carol accepting = %d", code)
	}
	var seen []core.MailContact
	carol.mustOK(http.MethodGet, "/api/v1/mailbox/contacts", nil, &seen)
	if len(seen) != 0 {
		t.Fatalf("carol sees %+v", seen)
	}
	admin.mustOK(http.MethodPost, "/api/v1/mailbox/contacts/accept", map[string]string{"id": contact.ID}, &contact)
	if contact.Status != core.MailContactAccepted {
		t.Fatalf("contact = %+v", contact)
	}

	// Now Bob writes to Ops; it is handed the message.
	var sent core.MailMessage
	bob.mustOK(http.MethodPost, "/api/v1/mailbox/messages", map[string]string{"to": "agent:ops", "text": "status?"}, &sent)
	if sent.DeliveredAt == nil || sent.From != "person:bob" || sent.ToName != "Ops" {
		t.Fatalf("sent = %+v", sent)
	}
	events := records.events("ops")
	if len(events) != 1 || events[0].Actor != "person:bob" || events[0].ThreadKey != "mail:person:bob" || events[0].Text != "status?" {
		t.Fatalf("events = %+v", events)
	}
	// Carol cannot read Bob's messages, and the link delivery path is not hers.
	if code := carol.do(http.MethodGet, "/api/v1/mailbox/messages?with=agent:ops&and=person:bob", nil, nil); code != http.StatusForbidden {
		t.Fatalf("carol reading bob's messages = %d", code)
	}
	if code := bob.do(http.MethodPost, mailDeliverPath, core.MailMessage{ID: "x", From: "agent:a@hub", To: "agent:ops", Text: "x"}, nil); code != http.StatusForbidden {
		t.Fatalf("member deliver = %d", code)
	}

	// The agent answers; Bob has one unread message until he opens it.
	if _, err := host.srv.mail.Send(ctx, "agent:ops", "person:bob", "All green."); err != nil {
		t.Fatal(err)
	}
	bob.mustOK(http.MethodGet, "/api/v1/mailbox", nil, &overview)
	if overview.Unread != 1 || len(overview.Conversations) != 1 || overview.Conversations[0].Peer.Name != "Ops" {
		t.Fatalf("overview = %+v", overview)
	}
	var messages []core.MailMessage
	bob.mustOK(http.MethodGet, "/api/v1/mailbox/messages?with=agent:ops", nil, &messages)
	bob.mustOK(http.MethodGet, "/api/v1/mailbox", nil, &overview)
	if len(messages) != 2 || messages[1].Text != "All green." || overview.Unread != 0 {
		t.Fatalf("messages = %+v unread = %d", messages, overview.Unread)
	}
	carol.mustOK(http.MethodGet, "/api/v1/mailbox", nil, &overview)
	if len(overview.Conversations) != 0 || len(overview.AgentConversations) != 0 {
		t.Fatalf("carol's overview = %+v", overview)
	}

	var directory struct {
		Parties   []core.MailParty `json:"parties"`
		Reachable []string         `json:"reachable"`
	}
	bob.mustOK(http.MethodGet, "/api/v1/mailbox/directory", nil, &directory)
	if !strings.Contains(strings.Join(directory.Reachable, " "), "agent:ops") {
		t.Fatalf("directory = %+v", directory)
	}
	bob.mustOK(http.MethodDelete, "/api/v1/mailbox/contacts?id="+url.QueryEscape(contact.ID), nil, nil)
	admin.mustOK(http.MethodGet, "/api/v1/mailbox/contacts", nil, &seen)
	if len(seen) != 0 {
		t.Fatalf("contacts after removal = %+v", seen)
	}
}

// An agent on a node and an agent on its hub write to each other through
// the link, in both directions, and a person on the hub reaches the node's
// agent the same way.
func TestMailTravelsBetweenAHubAndItsNode(t *testing.T) {
	const nodeToken = "node-bridge-token"
	hub := newLiveServer(t, nil)
	node := newLiveServer(t, func(cfg *config.Config) {
		cfg.Bridge.Enabled, cfg.Bridge.Token = true, nodeToken
	})
	node.node.reportInterval = 25 * time.Millisecond
	onHub, onNodeRecords := withAgents(t, hub.Server, hub.st), withAgents(t, node.Server, node.st)
	onHub.addAgent(t, "planner", "Planner")
	onNodeRecords.addAgent(t, "writer", "Writer")
	ctx := context.Background()
	device := joinNode(t, hub, node, nodeToken)
	onNode := "agent:writer@" + device

	// The hub learns the node's agents from its reports.
	eventually(t, "the hub to list the node's agent", func() bool {
		parties, err := hub.mail.Reachable(ctx, "agent:planner")
		return err == nil && slices.ContainsFunc(parties, func(party core.MailParty) bool {
			return party.Address == onNode && party.Machine == "reporting-node" && party.Online
		})
	})
	// The node asks the hub who its agent may write to.
	parties, err := node.mail.Reachable(ctx, "agent:writer")
	if err != nil || !slices.ContainsFunc(parties, func(party core.MailParty) bool {
		return party.Address == "agent:planner@hub" && party.Name == "Planner"
	}) {
		t.Fatalf("the node's agent must reach the hub's: %+v, %v", parties, err)
	}

	// Up the link.
	up, err := node.mail.Send(ctx, "agent:writer", "agent:planner@hub", "The draft is ready.")
	if err != nil || up.DeliveredAt == nil || up.ToName != "Planner" {
		t.Fatalf("up = %+v, %v", up, err)
	}
	events := onHub.events("planner")
	if len(events) != 1 || events[0].Actor != onNode || events[0].ActorName != "Writer" || events[0].Text != "The draft is ready." {
		t.Fatalf("hub events = %+v", events)
	}
	// Down the link.
	down, err := hub.mail.Send(ctx, "agent:planner", onNode, "Thanks, publish it.")
	if err != nil || down.DeliveredAt == nil {
		t.Fatalf("down = %+v, %v", down, err)
	}
	events = onNodeRecords.events("writer")
	if len(events) != 1 || events[0].Actor != "agent:planner@hub" || events[0].ActorName != "Planner" || events[0].ThreadKey != "mail:agent:planner@hub" {
		t.Fatalf("node events = %+v", events)
	}
	// Each machine keeps its side of the conversation.
	if kept, _ := node.st.ListMailConversation(ctx, "agent:writer", "agent:planner@hub", 10); len(kept) != 2 {
		t.Fatalf("node keeps %+v", kept)
	}
	if kept, _ := hub.st.ListMailConversation(ctx, "agent:planner", onNode, 10); len(kept) != 2 {
		t.Fatalf("hub keeps %+v", kept)
	}

	// A person on the hub writes to the node's agent and is answered.
	var sent core.MailMessage
	if code := hub.call(t, http.MethodPost, "/api/v1/mailbox/messages", "", map[string]string{"to": onNode, "text": "How long is it?"}, &sent); code != http.StatusOK || sent.DeliveredAt == nil {
		t.Fatalf("person to node agent = %d %+v", code, sent)
	}
	events = onNodeRecords.events("writer")
	if len(events) != 2 || events[1].Actor != "person:admin@hub" {
		t.Fatalf("node events = %+v", events)
	}
	if _, err := node.mail.Send(ctx, "agent:writer", "person:admin@hub", "Two pages."); err != nil {
		t.Fatal(err)
	}
	var overview mailOverview
	if code := hub.call(t, http.MethodGet, "/api/v1/mailbox", "", nil, &overview); code != http.StatusOK {
		t.Fatalf("overview = %d", code)
	}
	if overview.Unread != 1 || len(overview.Conversations) != 1 || overview.Conversations[0].Peer.Address != onNode || overview.Conversations[0].Last.Text != "Two pages." {
		t.Fatalf("overview = %+v", overview)
	}
	if len(overview.AgentConversations) != 1 {
		t.Fatalf("the administrator sees the agents' conversation: %+v", overview.AgentConversations)
	}

	// The hub's rules hold for what comes up the link.
	if _, err := node.mail.Send(ctx, "agent:writer", "agent:nobody@hub", "hello?"); err == nil || !strings.Contains(err.Error(), "no agent") {
		t.Fatalf("an unknown recipient must be refused by the hub: %v", err)
	}

	// A node that is gone gets its mail when it returns.
	node.node.stop()
	eventually(t, "the hub to see the node leave", func() bool {
		_, online := hub.linkHub.Connection(device)
		return !online
	})
	waiting, err := hub.mail.Send(ctx, "agent:planner", onNode, "One more thing.")
	if err != nil || waiting.DeliveredAt != nil {
		t.Fatalf("waiting = %+v, %v", waiting, err)
	}
	node.node.run(ctx)
	eventually(t, "the waiting message to arrive", func() bool {
		hub.mail.Retry(ctx)
		return len(onNodeRecords.events("writer")) == 3
	})
}
