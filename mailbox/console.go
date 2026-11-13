package mailbox

import (
	"context"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/wangning19940904/AgentMux/core"
)

// Conversation scopes. ScopeMine is the viewer's own messages with agents;
// ScopeAgents is what agents the viewer may see wrote to each other and to
// other people.
const (
	ScopeMine   = "mine"
	ScopeAgents = "agents"
)

// Conversations lists the conversations a person may see, newest first.
func (s *Service) Conversations(ctx context.Context, viewer Viewer, scope string) ([]core.MailConversation, error) {
	messages, err := s.store.ListRecentMail(ctx, recentLimit)
	if err != nil {
		return nil, err
	}
	known, err := s.partyIndex(ctx)
	if err != nil {
		return nil, err
	}
	byPair := map[string]*core.MailConversation{}
	order := []string{}
	for _, message := range messages {
		mine := message.From == viewer.Address || message.To == viewer.Address
		if mine != (scope != ScopeAgents) {
			continue
		}
		from, to := partyOf(known, message.From, message.FromName), partyOf(known, message.To, message.ToName)
		if !mine && !viewer.controls(from) && !viewer.controls(to) {
			continue
		}
		pair := core.MailPair(message.From, message.To)
		conversation, seen := byPair[pair]
		if !seen {
			conversation = &core.MailConversation{Self: from, Peer: to, Last: message}
			switch {
			case mine && message.To == viewer.Address:
				conversation.Self, conversation.Peer = to, from
			case !mine && !viewer.controls(from) && viewer.controls(to):
				conversation.Self, conversation.Peer = to, from
			}
			byPair[pair] = conversation
			order = append(order, pair)
		}
		conversation.Messages++
		if mine && message.To == viewer.Address && message.ReadAt == nil {
			conversation.Unread++
		}
	}
	out := make([]core.MailConversation, 0, len(order))
	for _, pair := range order {
		out = append(out, *byPair[pair])
	}
	return out, nil
}

// Unread counts the messages a person has not read.
func (s *Service) Unread(ctx context.Context, viewer Viewer) (int, error) {
	conversations, err := s.Conversations(ctx, viewer, ScopeMine)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, conversation := range conversations {
		total += conversation.Unread
	}
	return total, nil
}

// Messages returns the conversation between two addresses, oldest first.
// When the viewer is one of them, what they received is marked read.
func (s *Service) Messages(ctx context.Context, viewer Viewer, a, b string) ([]core.MailMessage, error) {
	mine := a == viewer.Address || b == viewer.Address
	if !mine {
		known, err := s.partyIndex(ctx)
		if err != nil {
			return nil, err
		}
		if !viewer.controls(partyOf(known, a, "")) && !viewer.controls(partyOf(known, b, "")) {
			return nil, ErrForbidden
		}
	}
	messages, err := s.store.ListMailConversation(ctx, a, b, historyLimit)
	if err != nil {
		return nil, err
	}
	if mine {
		peer := a
		if a == viewer.Address {
			peer = b
		}
		if err := s.store.MarkMailRead(ctx, viewer.Address, peer, s.now().UTC()); err != nil {
			return nil, err
		}
	}
	return messages, nil
}

// Directory lists every party the viewer can name, for choosing who to
// write to and who to make contacts of. reachable holds the addresses the
// viewer may write to now.
func (s *Service) Directory(ctx context.Context, viewer Viewer) (parties []core.MailParty, reachable []string, err error) {
	all, err := s.parties(ctx)
	if err != nil {
		return nil, nil, err
	}
	sender := viewer.party()
	all = slices.Clone(all)
	sortParties(all)
	parties, reachable = []core.MailParty{}, []string{}
	for _, party := range all {
		if party.Address == viewer.Address {
			continue
		}
		parties = append(parties, party)
		if allowed, err := s.allowed(ctx, sender, party); err != nil {
			return nil, nil, err
		} else if allowed {
			reachable = append(reachable, party.Address)
		}
	}
	return parties, reachable, nil
}

// Contacts lists the contacts a person may see: an administrator sees all,
// anyone else those with a side of their own.
func (s *Service) Contacts(ctx context.Context, viewer Viewer) ([]core.MailContact, error) {
	contacts, err := s.store.ListMailContacts(ctx)
	if err != nil {
		return nil, err
	}
	known, err := s.partyIndex(ctx)
	if err != nil {
		return nil, err
	}
	out := []core.MailContact{}
	for _, contact := range contacts {
		a, b := partyOf(known, contact.A, contact.AName), partyOf(known, contact.B, contact.BName)
		if viewer.controls(a) || viewer.controls(b) {
			contact.AName, contact.BName = a.Name, b.Name
			out = append(out, contact)
		}
	}
	return out, nil
}

// AddContact lets two parties of different owners write to each other. It
// takes effect at once when the viewer answers for both sides; otherwise it
// waits for the other side's owner.
func (s *Service) AddContact(ctx context.Context, viewer Viewer, first, second string) (core.MailContact, error) {
	a, knownA, err := s.party(ctx, first)
	if err != nil {
		return core.MailContact{}, err
	}
	b, knownB, err := s.party(ctx, second)
	if err != nil {
		return core.MailContact{}, err
	}
	switch {
	case !knownA || !knownB:
		return core.MailContact{}, Refuse("a contact joins two parties this AgentMux knows")
	case a.Address == b.Address:
		return core.MailContact{}, Refuse("a contact joins two different parties")
	case a.Kind == core.MailPartyPerson && b.Kind == core.MailPartyPerson:
		return core.MailContact{}, Refuse("a contact has an agent on at least one side")
	case a.Owner == b.Owner:
		return core.MailContact{}, Refuse("%s and %s have the same owner and can already write to each other", a.Name, b.Name)
	}
	if b.Address < a.Address {
		a, b = b, a
	}
	if !viewer.controls(a) && !viewer.controls(b) {
		return core.MailContact{}, ErrForbidden
	}
	now := s.now().UTC()
	contact := core.MailContact{
		ID: "mct_" + uuid.NewString(), A: a.Address, AName: a.Name, B: b.Address, BName: b.Name,
		Status: core.MailContactAccepted, RequestedBy: viewer.Address, CreatedAt: now, AcceptedAt: &now,
	}
	if existing, err := s.store.GetMailContact(ctx, a.Address, b.Address); err != nil {
		return core.MailContact{}, err
	} else if existing != nil {
		contact.ID, contact.CreatedAt = existing.ID, existing.CreatedAt
		if existing.Status == core.MailContactAccepted {
			return *existing, nil
		}
		// Asking for a contact the other side already asked for accepts it.
		if existing.WaitingFor != "" && viewer.controls(partyByAddress(a, b, existing.WaitingFor)) {
			contact.RequestedBy = existing.RequestedBy
		}
	}
	switch {
	case viewer.controls(a) && viewer.controls(b):
	case contact.RequestedBy != viewer.Address:
	case viewer.controls(a):
		contact.Status, contact.AcceptedAt, contact.WaitingFor = core.MailContactPending, nil, b.Address
	default:
		contact.Status, contact.AcceptedAt, contact.WaitingFor = core.MailContactPending, nil, a.Address
	}
	if err := s.store.UpsertMailContact(ctx, contact); err != nil {
		return core.MailContact{}, err
	}
	return contact, nil
}

func partyByAddress(a, b core.MailParty, address string) core.MailParty {
	if a.Address == address {
		return a
	}
	return b
}

// AcceptContact accepts a pending contact on behalf of the side it waits for.
func (s *Service) AcceptContact(ctx context.Context, viewer Viewer, id string) (core.MailContact, error) {
	contact, err := s.store.GetMailContactByID(ctx, strings.TrimSpace(id))
	if err != nil {
		return core.MailContact{}, err
	}
	if contact == nil {
		return core.MailContact{}, Refuse("this contact no longer exists")
	}
	if contact.Status == core.MailContactAccepted {
		return *contact, nil
	}
	known, err := s.partyIndex(ctx)
	if err != nil {
		return core.MailContact{}, err
	}
	if !viewer.controls(partyOf(known, contact.WaitingFor, "")) {
		return core.MailContact{}, ErrForbidden
	}
	now := s.now().UTC()
	contact.Status, contact.AcceptedAt, contact.WaitingFor = core.MailContactAccepted, &now, ""
	if err := s.store.UpsertMailContact(ctx, *contact); err != nil {
		return core.MailContact{}, err
	}
	return *contact, nil
}

// RemoveContact ends a contact, or declines or withdraws a pending one.
func (s *Service) RemoveContact(ctx context.Context, viewer Viewer, id string) error {
	contact, err := s.store.GetMailContactByID(ctx, strings.TrimSpace(id))
	if err != nil || contact == nil {
		return err
	}
	known, err := s.partyIndex(ctx)
	if err != nil {
		return err
	}
	if !viewer.controls(partyOf(known, contact.A, "")) && !viewer.controls(partyOf(known, contact.B, "")) {
		return ErrForbidden
	}
	return s.store.DeleteMailContact(ctx, contact.ID)
}

func (s *Service) partyIndex(ctx context.Context) (map[string]core.MailParty, error) {
	parties, err := s.parties(ctx)
	if err != nil {
		return nil, err
	}
	index := make(map[string]core.MailParty, len(parties))
	for _, party := range parties {
		index[party.Address] = party
	}
	return index, nil
}

// partyOf describes an address: the directory's entry, or what the address
// itself says when the party is elsewhere or gone. A party that is not in
// the directory has an owner nobody shares.
func partyOf(known map[string]core.MailParty, address, name string) core.MailParty {
	if party, ok := known[address]; ok {
		return party
	}
	party := core.MailParty{Address: address, Name: name, Owner: "\x00unknown"}
	if parsed, ok := core.ParseMailAddress(address); ok {
		party.Kind, party.Machine = parsed.Kind, parsed.Machine
		party.Name = firstNonEmpty(name, parsed.ID)
		if parsed.Machine == core.MailHub {
			party.Machine = "the hub"
		}
	}
	return party
}

// Controlled lists the addresses of the viewer's own parties, so a Console can
// mark them.
func (s *Service) Controlled(ctx context.Context, viewer Viewer) ([]string, error) {
	parties, err := s.parties(ctx)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, party := range parties {
		if viewer.controls(party) {
			out = append(out, party.Address)
		}
	}
	slices.Sort(out)
	return out, nil
}
