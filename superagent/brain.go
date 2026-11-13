package superagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/wangning19940904/AgentMux/core"
)

// The brain of an agent bound to a channel chats through the channel engine,
// so its replies stream, queue and take steering, approval cards and /
// commands like any channel turn. When the agent has a worker policy, the
// engine asks the Service for a brief to put before each message, and hands
// back the brain's whole answer, whose action blocks the Service carries out
// as it would a router's. Ticket updates reach the brain as router turns and
// are posted to the chat the Ticket came from.

// brainCharter is the brief's first part. It is shorter than the router's:
// the brain is talking with the person, and the channel engine already tells
// it about the chat.
const brainCharter = `AGENTMUX CONTEXT (the person cannot see this part; the chat message follows it)
You are %q, an agent in AgentMux. You can start workers: separate sessions that do longer work on their own while you stay available here.
- Answer directly when you can do it well in this turn: questions, quick lookups, small changes.
- Start a worker for work that takes longer, can run in parallel, or belongs to another repository or machine. Check OPEN TICKETS first: never start a second ticket for work that already has one; attach to it instead.
- Say in your reply what you started or changed. AgentMux tells you when a worker finishes a turn, and posts your report to this chat.

YOUR HOME
%s holds tickets/<ticket id>/ (ticket.json, inputs.md, result.md, progress.md, work/) and memory/, indexed by memory/MEMORY.md, shown below. Keep what will help later in memory/; never store secrets.

ACTIONS
To have AgentMux act, end your reply with one block, holding one action or a JSON list of them. AgentMux removes it before the person sees your reply:
` + actionOpen + `
{"action":"accept","worker_id":"","title":"","instructions":"","reason":""}
` + actionClose + `
- "accept" starts a ticket: worker_id from the roster, a short title, and instructions with everything the worker needs.
- "attach" passes this message to an open ticket, with an optional note in "instructions"; "stop" stops one; both take "ticket_id".
- "reason" is a short note for the operator's audit log.
Name only workers on the roster and tickets listed below.
`

const brainObserveNote = `
OBSERVE MODE
AgentMux records the actions you ask for but does not carry them out. Answer the person as usual.
`

// BrainBrief implements core.BrainHook. An agent with no worker policy, or
// one that is disabled, gets no brief, so its turn is a plain chat turn.
func (s *Service) BrainBrief(ctx context.Context, brainID string, msg *core.Message) string {
	agent := s.briefedAgent(ctx, brainID, msg)
	if agent == nil {
		return ""
	}
	tickets, err := s.store.ListSuperAgentTickets(ctx, agent.ID, ticketListLimit)
	if err != nil {
		s.log.Warn("load tickets for brain brief", "agent", agent.ID, "err", err)
		return ""
	}
	home, err := s.ensureHome(agent.ID)
	if err != nil {
		s.log.Warn("create agent home for brain brief", "agent", agent.ID, "err", err)
		return ""
	}
	trigger := chatTrigger(*agent, msg)
	view := s.routerView(ctx, *agent, trigger, tickets)
	var brief strings.Builder
	fmt.Fprintf(&brief, brainCharter, agent.Name, home)
	if agent.Mode == core.SuperAgentModeObserve {
		brief.WriteString(brainObserveNote)
	}
	writeRouterContext(&brief, *agent, s.workerStatuses(ctx, *agent), view)
	return brief.String()
}

// BrainFinished implements core.BrainHook. It carries out the action blocks of
// a brain's answer. The chat already has the reply, so only the actions, and a
// note when they cannot be carried out, remain.
func (s *Service) BrainFinished(ctx context.Context, brainID string, msg *core.Message, answer string) {
	if !strings.Contains(answer, actionOpen) {
		return
	}
	agent := s.briefedAgent(ctx, brainID, msg)
	if agent == nil {
		return
	}
	now := s.now().UTC()
	trigger := chatTrigger(*agent, msg)
	trigger.ID = "sat_" + uuid.NewString()
	trigger.ReceivedAt, trigger.UpdatedAt = now, now
	// Not pending: the brain already answered, so the router never takes it.
	trigger.Disposition = core.SuperAgentDispositionAnswered
	decision, parseErr := parseRouterDecision(answer)
	inserted, err := s.store.InsertSuperAgentTrigger(ctx, trigger)
	if err != nil {
		s.log.Error("record brain actions", "agent", agent.ID, "err", err)
		return
	}
	if !inserted {
		return // A redelivered message already had its actions carried out.
	}
	s.recordEvent(trigger.SourceID, now)
	decision.Reply = ""
	if parseErr == nil && agent.Mode == core.SuperAgentModeAct {
		parseErr = s.preflight(ctx, *agent, decision)
	}
	if parseErr != nil {
		s.finishTrigger(ctx, trigger, core.SuperAgentDispositionFailed, "brain asked for something AgentMux cannot carry out: "+parseErr.Error(), "")
		if agent.Mode == core.SuperAgentModeAct {
			_ = s.sendReply(ctx, *agent, trigger.SourceID, trigger.Reply, "AgentMux could not carry out what I asked for, so nothing was started: "+parseErr.Error())
		}
		return
	}
	if agent.Mode == core.SuperAgentModeObserve {
		s.finishTrigger(ctx, trigger, core.SuperAgentDispositionObserved, decision.summary(), "")
		return
	}
	s.apply(ctx, *agent, trigger, decision)
}

// briefedAgent is the enabled agent with a worker policy that a brain
// belongs to, or nil.
func (s *Service) briefedAgent(ctx context.Context, brainID string, msg *core.Message) *core.SuperAgent {
	if msg == nil || msg.ChannelID == "" {
		return nil
	}
	agent, err := s.agentForBrain(ctx, brainID)
	if err != nil {
		s.log.Warn("find the agent of a brain", "brain", brainID, "err", err)
		return nil
	}
	if agent == nil || !agent.Enabled {
		return nil
	}
	*agent = s.withWorkerRuntimes(ctx, *agent)
	if !canStartWorkers(*agent) {
		return nil
	}
	return agent
}

// chatTrigger is a chat message to a brain as a Trigger. Its thread is the
// channel conversation, so Tickets started there are listed on the next turn
// and their reports return to it.
func chatTrigger(agent core.SuperAgent, msg *core.Message) core.SuperAgentTrigger {
	key := firstNonEmpty(msg.ConversationKey, core.ResolveConversationKey(msg))
	messageID := firstNonEmpty(msg.SourceMessageID, msg.ID)
	reply, _ := json.Marshal(feishuReply{ChannelID: msg.ChannelID, ChatID: msg.ChatID, MessageID: messageID})
	text := msg.Text
	if msg.DisplayText != nil {
		text = *msg.DisplayText
	}
	return core.SuperAgentTrigger{
		SuperAgentID: agent.ID, SourceID: channelSourceID(msg.ChannelID), SourceKind: core.SuperAgentSourceChannel,
		DedupeKey: messageID, ThreadKey: channelSourcePrefix + msg.ChannelID + ":" + key,
		Actor: msg.UserID, ActorName: msg.UserName, Addressed: true,
		Text: truncateRunes(text, maxTriggerRunes), Reply: reply,
	}
}
