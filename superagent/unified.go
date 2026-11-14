package superagent

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
)

// AgentMux has one kind of agent: a core.SuperAgent whose brain is an Agent
// instance. An agent made as one, through the Console or the agent-instances
// API, shares its id with its brain. Agents saved before there was one kind
// were Agent instances bound to channels; UnifyAgents gives each of them an
// agent record around it when the daemon starts.

// channelSourcePrefix starts the id of a channel source: a channel bound to an
// agent's brain. Channel sources are derived from the binding, never stored.
const channelSourcePrefix = "channel:"

func channelSourceID(channelID string) string { return channelSourcePrefix + channelID }

// ownsBrain reports whether an agent's brain is its own Agent instance, made
// with it, as opposed to a router agent or an Agent a Super Agent routed with
// before there was one kind.
func ownsBrain(agent core.SuperAgent) bool {
	return agent.ID != "" && agent.RouterAgentID == agent.ID
}

// canStartWorkers reports whether an agent has a worker policy. Without one its
// brain only chats, exactly as an agent did before it could start workers.
func canStartWorkers(agent core.SuperAgent) bool {
	return agent.AutoWorkers || len(agent.Workers) > 0
}

// isAgentRecord reports whether an Agent instance is an agent's brain in its
// own right, as opposed to a router agent or an auto worker, which belong to
// an agent and are kept out of view.
func isAgentRecord(instance core.AgentInstance) bool {
	return !IsRouterAgent(instance) && !IsManagedAgent(instance)
}

// unifiedAgent is the agent record around an Agent instance that has none. It
// acts, since the instance already answered the people who reach its
// channels, and it starts no workers until the operator gives it a policy, so
// its chats run exactly as they did.
func (s *Service) unifiedAgent(instance core.AgentInstance) core.SuperAgent {
	now := s.now().UTC()
	created := instance.CreatedAt
	if created.IsZero() {
		created = now
	}
	name := strings.TrimSpace(instance.Name)
	if name == "" {
		name = defaultSuperAgentName(instance)
	}
	return core.SuperAgent{
		ID: instance.ID, Name: name, RouterAgentID: instance.ID,
		MaxConcurrency: defaultConcurrency,
		Workers:        []core.SuperAgentWorker{}, Sources: []core.SuperAgentSource{},
		Enabled: instance.Enabled, CreatedAt: created, UpdatedAt: now,
	}
}

// UnifyAgents gives every Agent instance that is no agent's brain an agent
// record of its own, with the same id. It only adds records: the instance,
// its channel bindings, conversations and grants stay as they are, so the
// people who reach it keep reaching it. Running it again adds nothing.
func (s *Service) UnifyAgents(ctx context.Context) (int, error) {
	instances, err := s.store.ListAgentInstances(ctx)
	if err != nil {
		return 0, fmt.Errorf("list agent instances: %w", err)
	}
	agents, err := s.store.ListSuperAgents(ctx)
	if err != nil {
		return 0, fmt.Errorf("list agents: %w", err)
	}
	brains, ids := map[string]bool{}, map[string]bool{}
	for _, agent := range agents {
		brains[agent.RouterAgentID], ids[agent.ID] = true, true
	}
	slices.SortFunc(instances, func(a, b core.AgentInstance) int {
		if order := a.CreatedAt.Compare(b.CreatedAt); order != 0 {
			return order
		}
		return strings.Compare(a.ID, b.ID)
	})
	created := 0
	for _, instance := range instances {
		if !isAgentRecord(instance) || brains[instance.ID] {
			continue
		}
		if ids[instance.ID] {
			s.log.Warn("agent id is taken by another agent; left as it is", "agent", instance.ID)
			continue
		}
		if err := validSuperAgentID(instance.ID); err != nil {
			s.log.Warn("agent id cannot name a home; left as it is", "agent", instance.ID, "err", err)
			continue
		}
		if err := s.store.UpsertSuperAgent(ctx, s.unifiedAgent(instance)); err != nil {
			return created, fmt.Errorf("give agent %s its record: %w", instance.ID, err)
		}
		brains[instance.ID], ids[instance.ID] = true, true
		created++
	}
	return created, nil
}

// EnsureAgent is called after an Agent instance is saved through the
// agent-instances API. An instance that is no agent's brain becomes one, as a
// new agent; the agent that owns it follows its name and whether it is
// enabled. Router agents and auto workers are left alone.
func (s *Service) EnsureAgent(ctx context.Context, instance core.AgentInstance) (*core.SuperAgent, error) {
	if !isAgentRecord(instance) || strings.TrimSpace(instance.ID) == "" {
		return nil, nil
	}
	agent, err := s.agentForBrain(ctx, instance.ID)
	if err != nil {
		return nil, err
	}
	if agent != nil {
		if !ownsBrain(*agent) || (agent.Name == instance.Name && agent.Enabled == instance.Enabled) {
			return agent, nil
		}
		agent.Name, agent.Enabled, agent.UpdatedAt = instance.Name, instance.Enabled, s.now().UTC()
	} else {
		taken, err := s.store.GetSuperAgent(ctx, instance.ID)
		if err != nil {
			return nil, err
		}
		if taken != nil {
			return nil, fmt.Errorf("agent id %q is already taken by %q", instance.ID, taken.Name)
		}
		if err := validSuperAgentID(instance.ID); err != nil {
			return nil, err
		}
		created := s.unifiedAgent(instance)
		agent = &created
	}
	if err := s.store.UpsertSuperAgent(ctx, *agent); err != nil {
		return nil, err
	}
	if err := s.refreshClaims(ctx); err != nil {
		return nil, err
	}
	return agent, nil
}

// AgentFor returns the agent whose brain is an Agent instance, or nil.
func (s *Service) AgentFor(ctx context.Context, brainID string) (*core.SuperAgent, error) {
	return s.agentForBrain(ctx, brainID)
}

// agentForBrain finds the agent a brain belongs to. An agent made as one has
// its brain's id, so that lookup comes first.
func (s *Service) agentForBrain(ctx context.Context, brainID string) (*core.SuperAgent, error) {
	if brainID == "" {
		return nil, nil
	}
	agent, err := s.store.GetSuperAgent(ctx, brainID)
	if err != nil {
		return nil, err
	}
	if agent != nil && agent.RouterAgentID == brainID {
		return agent, nil
	}
	agents, err := s.store.ListSuperAgents(ctx)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(agents, func(a, b core.SuperAgent) int { return a.CreatedAt.Compare(b.CreatedAt) })
	for index := range agents {
		if agents[index].RouterAgentID == brainID {
			return &agents[index], nil
		}
	}
	return nil, nil
}

// withChannelSources lists the channels bound to an agent's brain among its
// sources. They are read from the bindings each time, never stored.
func (s *Service) withChannelSources(ctx context.Context, agent core.SuperAgent) core.SuperAgent {
	agent.Sources = slices.DeleteFunc(slices.Clone(agent.Sources), func(source core.SuperAgentSource) bool {
		return source.Kind == core.SuperAgentSourceChannel
	})
	if agent.RouterAgentID == "" {
		return agent
	}
	channels, err := s.store.ListChannels(ctx)
	if err != nil {
		s.log.Warn("list channels for agent sources", "agent", agent.ID, "err", err)
		return agent
	}
	for _, channel := range channels {
		if channel.AgentID != agent.RouterAgentID {
			continue
		}
		agent.Sources = append(agent.Sources, channelSource(channel))
	}
	return agent
}

func channelSource(channel core.Channel) core.SuperAgentSource {
	return core.SuperAgentSource{
		ID: channelSourceID(channel.ID), Kind: core.SuperAgentSourceChannel, Name: channel.Name, Enabled: channel.Enabled,
		AllowedActors: []string{}, Config: map[string]string{"channel_id": channel.ID, "channel_type": channel.Type},
	}
}

// replySource is the source a reply to sourceID goes through: one of the
// agent's stored sources, or the channel a channel source names.
func replySource(agent core.SuperAgent, sourceID string) (core.SuperAgentSource, bool) {
	for _, source := range agent.Sources {
		if source.ID == sourceID {
			return source, true
		}
	}
	if sourceID == mailSourceID {
		return core.SuperAgentSource{ID: sourceID, Kind: core.SuperAgentSourceMailbox, Enabled: true}, true
	}
	if channelID, ok := strings.CutPrefix(sourceID, channelSourcePrefix); ok && channelID != "" {
		return core.SuperAgentSource{
			ID: sourceID, Kind: core.SuperAgentSourceChannel, Enabled: true,
			Config: map[string]string{"channel_id": channelID},
		}, true
	}
	return core.SuperAgentSource{}, false
}

// syncOwnBrain makes an agent's own brain carry its name and whether it is
// enabled, so the channel engine, which reads the instance, agrees with it.
func (s *Service) syncOwnBrain(ctx context.Context, agent core.SuperAgent) error {
	if !ownsBrain(agent) {
		return nil
	}
	brain, err := s.store.GetAgentInstance(ctx, agent.RouterAgentID)
	if err != nil || brain == nil {
		return err
	}
	if brain.Name == agent.Name && brain.Enabled == agent.Enabled {
		return nil
	}
	brain.Name, brain.Enabled, brain.UpdatedAt = agent.Name, agent.Enabled, s.now().UTC()
	return s.store.UpsertAgentInstance(ctx, brain)
}
