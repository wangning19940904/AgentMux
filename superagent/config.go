package superagent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/wangning19940904/AgentMux/core"
)

const redactedValue = "<redacted>"

// secretConfigKeys are write-only through the API: reads return redactedValue
// and a save that sends it back keeps the stored secret.
var secretConfigKeys = []string{"password", "webhook_secret", "github_token"}

// List returns every Super Agent with its secrets redacted.
func (s *Service) List(ctx context.Context) ([]core.SuperAgent, error) {
	agents, err := s.store.ListSuperAgents(ctx)
	if err != nil {
		return nil, err
	}
	for index := range agents {
		agents[index] = redactAgent(agents[index])
	}
	return agents, nil
}

// Save validates and stores a Super Agent, then applies its Sources.
func (s *Service) Save(ctx context.Context, agent core.SuperAgent) (core.SuperAgent, error) {
	var existing *core.SuperAgent
	if strings.TrimSpace(agent.ID) != "" {
		found, err := s.store.GetSuperAgent(ctx, agent.ID)
		if err != nil {
			return core.SuperAgent{}, err
		}
		existing = found
	}
	if err := s.normalize(ctx, &agent, existing); err != nil {
		return core.SuperAgent{}, err
	}
	if err := s.store.UpsertSuperAgent(ctx, agent); err != nil {
		return core.SuperAgent{}, err
	}
	if err := s.refreshClaims(ctx); err != nil {
		return core.SuperAgent{}, err
	}
	return redactAgent(agent), nil
}

// Delete stops the Super Agent's running turns and removes its records.
func (s *Service) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	for key, cancel := range s.running {
		if strings.HasPrefix(key, id+"|") {
			s.stopping[strings.TrimPrefix(key, id+"|")] = true
			cancel()
		}
	}
	s.mu.Unlock()
	if err := s.store.DeleteSuperAgent(ctx, id); err != nil {
		return err
	}
	if err := s.store.DeleteAgentInstance(ctx, autoWorkerAgentID(id)); err != nil {
		return err
	}
	return s.refreshClaims(ctx)
}

func (s *Service) normalize(ctx context.Context, agent *core.SuperAgent, existing *core.SuperAgent) error {
	now := s.now().UTC()
	agent.ID = strings.TrimSpace(agent.ID)
	agent.Name = strings.TrimSpace(agent.Name)
	agent.RouterAgentID = strings.TrimSpace(agent.RouterAgentID)
	if agent.ID == "" {
		agent.ID = "sa_" + uuid.NewString()
	}
	if existing != nil {
		agent.CreatedAt = existing.CreatedAt
	} else {
		agent.CreatedAt = now
	}
	agent.UpdatedAt = now
	switch agent.Mode {
	case "":
		agent.Mode = core.SuperAgentModeObserve
	case core.SuperAgentModeAct, core.SuperAgentModeObserve:
	default:
		return fmt.Errorf("unknown mode %q", agent.Mode)
	}
	if agent.MaxConcurrency == 0 {
		agent.MaxConcurrency = defaultConcurrency
	}
	if agent.MaxConcurrency < 1 || agent.MaxConcurrency > maxConcurrencyLimit {
		return fmt.Errorf("max_concurrency must be between 1 and %d", maxConcurrencyLimit)
	}
	router, err := s.validateRouter(ctx, agent.RouterAgentID)
	if err != nil {
		return err
	}
	if agent.Name == "" {
		agent.Name = router.Name
	}
	if err := s.normalizeWorkers(ctx, agent); err != nil {
		return err
	}
	if err := normalizeAutoWorkers(agent, router); err != nil {
		return err
	}
	return normalizeSources(agent, existing)
}

// validateRouter keeps the blast radius of a prompt injection small. The router
// reads untrusted events and is told not to use tools, but only the Agent's own
// approval mode can enforce that.
func (s *Service) validateRouter(ctx context.Context, id string) (*core.AgentInstance, error) {
	if id == "" {
		return nil, errors.New("router_agent_id is required")
	}
	router, err := s.store.GetAgentInstance(ctx, id)
	if err != nil {
		return nil, err
	}
	if router == nil {
		return nil, fmt.Errorf("router agent %q not found on this machine", id)
	}
	if router.DefaultApprovalMode == core.ApprovalModeYolo {
		return nil, errors.New("the router reads untrusted events, so its Agent cannot use the yolo approval mode; choose plan mode")
	}
	return router, nil
}

func normalizeAutoWorkers(agent *core.SuperAgent, router *core.AgentInstance) error {
	agent.AutoWorkerDir = strings.TrimSpace(agent.AutoWorkerDir)
	agent.AutoWorkerApprovalMode = strings.TrimSpace(agent.AutoWorkerApprovalMode)
	if !agent.AutoWorkers {
		return nil
	}
	if agent.AutoWorkerApprovalMode == "" {
		agent.AutoWorkerApprovalMode = core.ApprovalModeAutoEdit
	}
	if err := validateAutoWorkerApproval(agent.AutoWorkerApprovalMode); err != nil {
		return err
	}
	if firstNonEmpty(agent.AutoWorkerDir, router.WorkDir) == "" {
		return errors.New("auto workers need a working directory: set one on the router Agent or on this Super Agent")
	}
	return nil
}

func (s *Service) normalizeWorkers(ctx context.Context, agent *core.SuperAgent) error {
	if len(agent.Workers) == 0 && !agent.AutoWorkers {
		return errors.New("at least one worker is required unless auto workers are on")
	}
	seen := map[string]bool{}
	for index := range agent.Workers {
		worker := &agent.Workers[index]
		worker.HostID = strings.TrimSpace(worker.HostID)
		worker.AgentID = strings.TrimSpace(worker.AgentID)
		worker.Label = strings.TrimSpace(worker.Label)
		worker.Capabilities = strings.TrimSpace(worker.Capabilities)
		if worker.AgentID == "" {
			return fmt.Errorf("worker %d must choose an agent", index+1)
		}
		key := worker.HostID + "/" + worker.AgentID
		if seen[key] {
			return fmt.Errorf("worker %q is listed twice", worker.AgentID)
		}
		seen[key] = true
		if worker.ID == autoWorkerID {
			return fmt.Errorf("worker id %q is reserved", autoWorkerID)
		}
		if worker.ID == "" {
			worker.ID = "wrk_" + uuid.NewString()
		}
		if worker.HostID != "" {
			continue // A remote host may be offline while it is being configured.
		}
		local, err := s.store.GetAgentInstance(ctx, worker.AgentID)
		if err != nil {
			return err
		}
		if local == nil {
			return fmt.Errorf("worker agent %q not found on this machine", worker.AgentID)
		}
	}
	return nil
}

func normalizeSources(agent *core.SuperAgent, existing *core.SuperAgent) error {
	seen := map[string]bool{}
	for index := range agent.Sources {
		source := &agent.Sources[index]
		source.ID = strings.TrimSpace(source.ID)
		if source.ID == "" {
			source.ID = "src_" + uuid.NewString()
		}
		if seen[source.ID] {
			return fmt.Errorf("source %q is listed twice", source.ID)
		}
		seen[source.ID] = true
		restoreSecrets(source, existing)
		source.AllowedActors = normalizeActors(source.AllowedActors)
		if err := validateSource(*source); err != nil {
			return fmt.Errorf("%s source: %w", source.Kind, err)
		}
	}
	return nil
}

func normalizeActors(actors []string) []string {
	out := make([]string, 0, len(actors))
	for _, actor := range actors {
		if actor = strings.TrimSpace(actor); actor != "" && !slices.Contains(out, actor) {
			out = append(out, actor)
		}
	}
	return out
}

func validateSource(source core.SuperAgentSource) error {
	if source.Enabled && len(source.AllowedActors) == 0 {
		return errors.New("an enabled source needs at least one allowed sender")
	}
	// A wildcard is only reasonable where the platform already limits who can
	// reach the bot. Anyone can comment on a public repository or send an email,
	// and a document watcher would be triggered by its own workers' edits.
	if slices.Contains(source.AllowedActors, "*") && source.Kind != core.SuperAgentSourceFeishuMessage {
		return errors.New(`"*" is only allowed for Feishu messages; list the allowed senders`)
	}
	required := map[string][]string{
		core.SuperAgentSourceFeishuMessage: {"channel_id"},
		core.SuperAgentSourceFeishuDoc:     {"channel_id", "documents"},
		core.SuperAgentSourceEmail:         {"imap_host", "username", "password"},
		core.SuperAgentSourceGitHub:        {"webhook_secret", "agent_login"},
	}
	keys, known := required[source.Kind]
	if !known {
		return fmt.Errorf("unknown kind %q", source.Kind)
	}
	for _, key := range keys {
		if strings.TrimSpace(source.Config[key]) == "" {
			return fmt.Errorf("%s is required", key)
		}
	}
	return nil
}

func restoreSecrets(source *core.SuperAgentSource, existing *core.SuperAgent) {
	if existing == nil {
		return
	}
	for _, previous := range existing.Sources {
		if previous.ID != source.ID {
			continue
		}
		for key, value := range source.Config {
			if value == redactedValue {
				source.Config[key] = previous.Config[key]
			}
		}
	}
}

func redactAgent(agent core.SuperAgent) core.SuperAgent {
	sources := make([]core.SuperAgentSource, len(agent.Sources))
	for index, source := range agent.Sources {
		config := make(map[string]string, len(source.Config))
		for key, value := range source.Config {
			if slices.Contains(secretConfigKeys, key) && value != "" {
				value = redactedValue
			}
			config[key] = value
		}
		source.Config = config
		sources[index] = source
	}
	agent.Sources = sources
	return agent
}

func splitList(value string) []string {
	return normalizeActors(strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n' || r == ' ' || r == '\t' || r == '\r'
	}))
}
