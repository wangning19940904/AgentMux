package superagent

import (
	"context"
	"strings"
	"sync"

	"github.com/wangning19940904/AgentMux/core"
)

type sourceClaim struct {
	agent  core.SuperAgent
	source core.SuperAgentSource
}

// claimIndex answers, without a database query on every inbound message, which
// enabled Sources listen where. refreshClaims rebuilds it after each change.
type claimIndex struct {
	mu        sync.RWMutex
	bySource  map[string]sourceClaim
	byChannel map[string][]sourceClaim
}

func (c *claimIndex) replace(bySource map[string]sourceClaim, byChannel map[string][]sourceClaim) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bySource, c.byChannel = bySource, byChannel
}

func (c *claimIndex) source(id string) (sourceClaim, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	claim, ok := c.bySource[id]
	return claim, ok
}

func (c *claimIndex) channelSources(channelID, kind string) []sourceClaim {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []sourceClaim
	for _, claim := range c.byChannel[channelID] {
		if claim.source.Kind == kind {
			out = append(out, claim)
		}
	}
	return out
}

func (c *claimIndex) ofKind(kind string) []sourceClaim {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []sourceClaim
	for _, claim := range c.bySource {
		if claim.source.Kind == kind {
			out = append(out, claim)
		}
	}
	return out
}

func (s *Service) refreshClaims(ctx context.Context) error {
	agents, err := s.store.ListSuperAgents(ctx)
	if err != nil {
		return err
	}
	bySource := map[string]sourceClaim{}
	byChannel := map[string][]sourceClaim{}
	var documents, messages []core.SuperAgentSource
	for _, agent := range agents {
		if !agent.Enabled {
			continue
		}
		for _, source := range agent.Sources {
			if !source.Enabled {
				continue
			}
			claim := sourceClaim{agent: agent, source: source}
			bySource[source.ID] = claim
			if channelID := strings.TrimSpace(source.Config["channel_id"]); channelID != "" {
				byChannel[channelID] = append(byChannel[channelID], claim)
			}
			switch source.Kind {
			case core.SuperAgentSourceFeishuDoc:
				documents = append(documents, source)
			case core.SuperAgentSourceFeishuMessage:
				messages = append(messages, source)
			}
		}
	}
	s.claims.replace(bySource, byChannel)
	if s.ctx != nil {
		for _, source := range documents {
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.subscribeDocuments(s.ctx, source)
			}()
		}
		// A save may have changed the channel or its app, so the scope check
		// starts fresh.
		s.mu.Lock()
		clear(s.scopeChecks)
		s.mu.Unlock()
		for _, source := range messages {
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.checkFeishuScopes(s.ctx, source)
			}()
		}
	}
	return nil
}

// sendReply delivers one message back to where a Trigger came from. Console
// requests have no reply path; their result stays on the Ticket.
func (s *Service) sendReply(ctx context.Context, agent core.SuperAgent, sourceID string, target []byte, text string) error {
	if strings.TrimSpace(text) == "" || len(target) == 0 || s.replies == nil {
		return nil
	}
	for _, source := range agent.Sources {
		if source.ID == sourceID {
			err := s.replies.Send(ctx, source, target, text)
			if err != nil && ctx.Err() == nil {
				s.log.Warn("super agent reply failed", "source", sourceID, "kind", source.Kind, "err", err)
			}
			return err
		}
	}
	return nil
}
