package superagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/mailbox"
)

// Direct messages. An agent reaches other agents and people through the
// mailbox: a message to it arrives as an event on the thread it shares with
// its sender, and its reply, or a post to an address, goes out as a message.
// Who may write to whom is the mailbox's decision, not the router's.

const (
	mailSourceID     = "mailbox"
	mailThreadPrefix = "mail:"
	// maxMailPlaces bounds how many parties a router is shown.
	maxMailPlaces = 60
)

// Mailbox is the part of the mailbox an agent uses.
type Mailbox interface {
	Send(ctx context.Context, from, to, text string) (core.MailMessage, error)
	Reachable(ctx context.Context, from string) ([]core.MailParty, error)
}

// mailReply is the reply address of a mailbox thread.
type mailReply struct {
	MailTo   string `json:"mail_to"`
	MailFrom string `json:"mail_from"`
}

// SetMailbox connects the mailbox. Without one, agents have no direct
// messages.
func (s *Service) SetMailbox(box Mailbox) {
	s.mu.Lock()
	s.mailbox = box
	s.mu.Unlock()
}

func (s *Service) mailService() Mailbox {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mailbox
}

// Stored lists every agent as it is kept, for callers that only need to
// know which agents exist.
func (s *Service) Stored(ctx context.Context) ([]core.SuperAgent, error) {
	return s.store.ListSuperAgents(ctx)
}

func agentAddress(agentID string) string { return core.MailPartyAgent + ":" + agentID }

func mailThreadKey(peer string) string { return mailThreadPrefix + peer }

// ReceiveMail hands a message to one of this machine's agents as an event on
// its thread with the sender. The mailbox already decided the sender may
// write to it. Receiving the same message twice records it once.
func (s *Service) ReceiveMail(ctx context.Context, agentID string, message core.MailMessage) error {
	agent, err := s.store.GetSuperAgent(ctx, agentID)
	if err != nil {
		return err
	}
	if agent == nil {
		return mailbox.Refuse("there is no agent %q on this machine", agentID)
	}
	if !agent.Enabled {
		return mailbox.Refuse("%s is turned off", agent.Name)
	}
	reply, _ := json.Marshal(mailReply{MailTo: message.From, MailFrom: agentAddress(agent.ID)})
	now := s.now().UTC()
	trigger := core.SuperAgentTrigger{
		ID: "sat_" + uuid.NewString(), SuperAgentID: agent.ID, SourceID: mailSourceID, SourceKind: core.SuperAgentSourceMailbox,
		DedupeKey: mailThreadPrefix + message.ID, ThreadKey: mailThreadKey(message.From),
		Actor: message.From, ActorName: message.FromName, Addressed: true,
		Text: truncateRunes(message.Text, maxTriggerRunes), Reply: reply,
		Disposition: core.SuperAgentDispositionPending, ReceivedAt: now, UpdatedAt: now,
	}
	if _, err := s.store.InsertSuperAgentTrigger(ctx, trigger); err != nil {
		return err
	}
	s.recordEvent(mailSourceID, now)
	s.notify()
	return nil
}

// mailPlaces lists the agents and people an agent can write to, as places
// its router can post to.
func (s *Service) mailPlaces(ctx context.Context, agent core.SuperAgent) []placeView {
	box := s.mailService()
	if box == nil {
		return nil
	}
	parties, err := box.Reachable(ctx, agentAddress(agent.ID))
	if err != nil {
		if ctx.Err() == nil {
			s.log.Debug("list an agent's contacts", "agent", agent.ID, "err", err)
		}
		return nil
	}
	var places []placeView
	for _, party := range parties {
		if len(places) >= maxMailPlaces {
			break
		}
		kind := "Person: a direct message, which they read in AgentMux"
		if party.Kind == core.MailPartyAgent {
			where := "this machine"
			if party.Machine != "" {
				where = party.Machine
			}
			kind = "Agent on " + where + ": a direct message"
			if !party.Online {
				kind += " (that machine is offline; the message is delivered when it returns)"
			}
		}
		places = append(places, placeView{To: party.Address, Kind: kind, Name: party.Name, About: oneLine(party.Description, 300)})
	}
	return places
}

// mailDestination checks an address against who the agent may write to.
func (s *Service) mailDestination(ctx context.Context, agent core.SuperAgent, to string) (destination, error) {
	box := s.mailService()
	if box == nil {
		return destination{}, fmt.Errorf("cannot post to %q: direct messages are not available on this machine", to)
	}
	parties, err := box.Reachable(ctx, agentAddress(agent.ID))
	if err != nil {
		return destination{}, err
	}
	for _, party := range parties {
		if strings.EqualFold(party.Address, to) {
			reply, _ := json.Marshal(mailReply{MailTo: party.Address, MailFrom: agentAddress(agent.ID)})
			return destination{sourceID: mailSourceID, reply: reply}, nil
		}
	}
	return destination{}, fmt.Errorf("cannot post to %q: it is not one of the agents or people in PLACES that this agent can write to", to)
}

// sendMail sends a reply or a post as a direct message.
func (s *Service) sendMail(ctx context.Context, target []byte, text string) error {
	var reply mailReply
	if err := json.Unmarshal(target, &reply); err != nil {
		return err
	}
	box := s.mailService()
	if box == nil || reply.MailTo == "" || reply.MailFrom == "" {
		return fmt.Errorf("direct messages are not available on this machine")
	}
	_, err := box.Send(ctx, reply.MailFrom, reply.MailTo, truncateRunes(text, mailbox.MaxMessageRunes))
	if err != nil && ctx.Err() == nil {
		s.log.Warn("direct message was not sent", "from", reply.MailFrom, "to", reply.MailTo, "err", err)
	}
	return err
}
