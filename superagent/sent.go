package superagent

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// A person often answers the agent by replying to one of its chat messages,
// in a chat where it reports on several pieces of work. Feishu tells AgentMux
// which message was replied to, but that message's thread is a new one, so
// without a record the router has only "this one" to go on. AgentMux keeps a
// short record of what each message it posted was about and hands it to the
// router with the reply.

const (
	sentSettingKey = "super_agent_sent:"
	maxSentRecords = 400
	sentRecordTTL  = 30 * 24 * time.Hour
	maxSentRunes   = 800
)

// sentAbout is what a message the agent posted was about: the thread it was
// sent for and the Tickets it reported on.
type sentAbout struct {
	Thread  string   `json:"thread,omitempty"`
	Tickets []string `json:"tickets,omitempty"`
}

func (a sentAbout) empty() bool { return a.Thread == "" && len(a.Tickets) == 0 }

// sentRecord is one chat message the agent posted.
type sentRecord struct {
	MessageID string    `json:"message_id"`
	Text      string    `json:"text"`
	About     sentAbout `json:"about"`
	At        time.Time `json:"at"`
}

// triggerAbout is what the agent's answer to an event is about: the event's
// thread, the Ticket an AgentMux event reports on and the Ticket the router
// acted on.
func triggerAbout(trigger core.SuperAgentTrigger) sentAbout {
	return sentAbout{Thread: trigger.ThreadKey, Tickets: compactIDs(trigger.SubjectTicketID, trigger.TicketID)}
}

// heldAbout is what a released message is about. A digest about several
// threads keeps only its Tickets.
func heldAbout(held []HeldMessage) sentAbout {
	var about sentAbout
	for index, message := range held {
		if index == 0 {
			about.Thread = message.About.Thread
		} else if about.Thread != message.About.Thread {
			about.Thread = ""
		}
		about.Tickets = append(about.Tickets, message.About.Tickets...)
	}
	about.Tickets = compactIDs(about.Tickets...)
	return about
}

func compactIDs(ids ...string) []string {
	var out []string
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// rememberSent records a posted message. A message the platform gave no id
// for, or that was about nothing, is not recorded. Failures are only logged:
// the message went out either way.
func (s *Service) rememberSent(ctx context.Context, agentID, messageID, text string, about sentAbout) {
	if messageID == "" || agentID == "" || about.empty() {
		return
	}
	s.sentMu.Lock()
	defer s.sentMu.Unlock()
	records, err := s.loadSent(ctx, agentID)
	if err != nil {
		s.log.Warn("load sent messages", "agent", agentID, "err", err)
		return
	}
	now := s.now().UTC()
	records = slices.DeleteFunc(records, func(record sentRecord) bool {
		return record.MessageID == messageID || now.Sub(record.At) > sentRecordTTL
	})
	records = append(records, sentRecord{MessageID: messageID, Text: truncateRunes(strings.TrimSpace(text), maxSentRunes), About: about, At: now})
	if len(records) > maxSentRecords {
		records = records[len(records)-maxSentRecords:]
	}
	if err := s.saveSetting(ctx, sentSettingKey+agentID, records); err != nil {
		s.log.Warn("record sent message", "agent", agentID, "err", err)
	}
}

func (s *Service) loadSent(ctx context.Context, agentID string) ([]sentRecord, error) {
	var records []sentRecord
	err := s.loadSetting(ctx, sentSettingKey+agentID, &records)
	return records, err
}

// repliedTo finds the agent's own message that msg replies to: the message
// it answers directly, else the root of its thread.
func (s *Service) repliedTo(ctx context.Context, agentID string, msg *core.Message) *core.SuperAgentRepliedMessage {
	ids := compactIDs(msg.ParentID, msg.RootID)
	if len(ids) == 0 {
		return nil
	}
	s.sentMu.Lock()
	records, err := s.loadSent(ctx, agentID)
	s.sentMu.Unlock()
	if err != nil {
		s.log.Warn("load sent messages", "agent", agentID, "err", err)
		return nil
	}
	for _, id := range ids {
		if id == msg.ID || id == msg.SourceMessageID {
			continue
		}
		for index := len(records) - 1; index >= 0; index-- {
			if record := records[index]; record.MessageID == id {
				return &core.SuperAgentRepliedMessage{MessageID: id, Text: record.Text, ThreadKey: record.About.Thread, TicketIDs: record.About.Tickets}
			}
		}
	}
	return nil
}
