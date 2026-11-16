package superagent

import (
	"context"
	"encoding/json"

	"github.com/wangning19940904/AgentMux/core"
)

// ChatReactor marks a chat message with its channel's ack reaction while the
// agent works on it, as a channel turn does. AddChatReaction returns "" when
// the channel has ack reactions turned off, its platform cannot react, or the
// reaction failed; the Engine logs the failure.
type ChatReactor interface {
	AddChatReaction(ctx context.Context, channelID, messageID string) string
	RemoveChatReaction(ctx context.Context, channelID, messageID, reactionID string)
}

// reactionMark is one reaction AgentMux put on the message behind a Trigger.
type reactionMark struct {
	channelID, messageID, reactionID string
}

// reactionKey names a Trigger before it is stored, so a reaction added on
// intake is found again when the Trigger finishes.
func reactionKey(agentID, sourceID, dedupeKey string) string {
	return agentID + "|" + sourceID + "|" + dedupeKey
}

// react marks the message a Feishu message Trigger replies to and remembers
// the mark under key. It does nothing when the message is already marked.
func (s *Service) react(ctx context.Context, key string, target []byte) {
	if s.reactor == nil {
		return
	}
	var reply feishuReply
	if err := json.Unmarshal(target, &reply); err != nil || reply.ChannelID == "" || reply.MessageID == "" {
		return
	}
	s.mu.Lock()
	_, marked := s.reactions[key]
	s.mu.Unlock()
	if marked {
		return
	}
	id := s.reactor.AddChatReaction(ctx, reply.ChannelID, reply.MessageID)
	if id == "" {
		return
	}
	s.mu.Lock()
	s.reactions[key] = reactionMark{channelID: reply.ChannelID, messageID: reply.MessageID, reactionID: id}
	s.mu.Unlock()
}

// unreact removes the mark remembered under key, if there is one.
func (s *Service) unreact(ctx context.Context, key string) {
	s.mu.Lock()
	mark, ok := s.reactions[key]
	delete(s.reactions, key)
	s.mu.Unlock()
	if ok && s.reactor != nil {
		s.reactor.RemoveChatReaction(ctx, mark.channelID, mark.messageID, mark.reactionID)
	}
}

// reactsTo reports whether a stored Trigger is a person's Feishu message,
// the only kind AgentMux marks while the router works on it.
func reactsTo(trigger core.SuperAgentTrigger) bool {
	return trigger.Kind == "" && trigger.SourceKind == core.SuperAgentSourceFeishuMessage
}

func triggerReactionKey(trigger core.SuperAgentTrigger) string {
	return reactionKey(trigger.SuperAgentID, trigger.SourceID, trigger.DedupeKey)
}
