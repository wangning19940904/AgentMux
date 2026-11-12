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
		agents[index] = redactAgent(s.withChannelSources(ctx, s.withWorkerRuntimes(ctx, agents[index])))
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
	if agent.RouterAgentID == "" {
		created, err := s.SaveRouter(ctx, Router{RuntimeID: agent.RouterRuntime, Name: routerName(agent.Name)})
		if err != nil {
			return core.SuperAgent{}, err
		}
		agent.RouterAgentID = created.ID
	}
	if err := s.store.UpsertSuperAgent(ctx, agent); err != nil {
		return core.SuperAgent{}, err
	}
	if err := s.bindRouter(ctx, agent); err != nil {
		return core.SuperAgent{}, err
	}
	if err := s.nameRouter(ctx, agent); err != nil {
		return core.SuperAgent{}, err
	}
	if err := s.syncOwnBrain(ctx, agent); err != nil {
		return core.SuperAgent{}, err
	}
	if err := s.refreshClaims(ctx); err != nil {
		return core.SuperAgent{}, err
	}
	return redactAgent(s.withChannelSources(ctx, agent)), nil
}

// Delete stops the Super Agent's running turns and removes its records, its
// auto workers and its router agent, or its own brain. Its home stays on disk,
// since tickets' worktrees there can hold work that was never pushed. Channels
// bound to a deleted brain are left without an agent, as when an Agent
// instance is deleted.
func (s *Service) Delete(ctx context.Context, id string) error {
	agent, err := s.store.GetSuperAgent(ctx, id)
	if err != nil {
		return err
	}
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
	if err := s.deleteAutoWorkers(ctx, id); err != nil {
		return err
	}
	if agent != nil && ownsBrain(*agent) {
		if err := s.store.DeleteAgentInstance(ctx, agent.RouterAgentID); err != nil {
			return err
		}
	} else if agent != nil {
		if err := s.deleteOwnedRouter(ctx, agent.RouterAgentID); err != nil {
			return err
		}
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
	if err := validSuperAgentID(agent.ID); err != nil {
		return err
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
	current := ""
	if existing != nil {
		current = existing.RouterAgentID
	}
	router, err := s.resolveRouter(ctx, agent, current, true)
	if err != nil {
		return err
	}
	if err := s.checkRouterOwner(ctx, agent.ID, router); err != nil {
		return err
	}
	if agent.Name == "" {
		agent.Name = defaultSuperAgentName(*router)
	}
	if err := s.normalizeWorkers(ctx, agent); err != nil {
		return err
	}
	if err := s.normalizeAutoWorkers(agent, router); err != nil {
		return err
	}
	return normalizeSources(agent, existing)
}

// normalizeAutoWorkers checks the worker frameworks. Workers need no read-only
// mode, so a framework that cannot route, such as Kimi, can still work. An empty
// approval mode gives each framework its own default.
func (s *Service) normalizeAutoWorkers(agent *core.SuperAgent, router *core.AgentInstance) error {
	agent.AutoWorkerDir = strings.TrimSpace(agent.AutoWorkerDir)
	agent.AutoWorkerApprovalMode = strings.TrimSpace(agent.AutoWorkerApprovalMode)
	if !agent.AutoWorkers {
		agent.WorkerRuntimes = nil
		return nil
	}
	agent.WorkerRuntimes = normalizeActors(agent.WorkerRuntimes)
	if len(agent.WorkerRuntimes) == 0 {
		agent.WorkerRuntimes = []string{router.RuntimeID}
	}
	mode := agent.AutoWorkerApprovalMode
	if mode != "" {
		if err := validateAutoWorkerApproval(mode); err != nil {
			return err
		}
	}
	for _, runtimeID := range agent.WorkerRuntimes {
		// The router's own framework already runs here.
		if runtimeID != router.RuntimeID && s.runtimeAvailable != nil && !s.runtimeAvailable(runtimeID) {
			return fmt.Errorf("worker framework %q is not installed on this machine", runtimeID)
		}
		if modes := core.ApprovalModeValuesForRuntime(runtimeID); mode != "" && len(modes) > 0 && !core.ApprovalModeSupported(runtimeID, mode) {
			return fmt.Errorf("approval mode %q is not supported by %s; choose another, or leave it empty for each framework's default", mode, runtimeID)
		}
	}
	return validateAutoWorkerDir(agent.AutoWorkerDir)
}

func (s *Service) normalizeWorkers(ctx context.Context, agent *core.SuperAgent) error {
	// An agent with its own brain may only chat; a router has nothing to do
	// without workers.
	if len(agent.Workers) == 0 && !agent.AutoWorkers && !ownsBrain(*agent) {
		return errors.New("at least one worker is required unless auto workers are on")
	}
	if agent.Workers == nil {
		agent.Workers = []core.SuperAgentWorker{}
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
		if isAutoWorkerID(worker.ID) {
			return fmt.Errorf("worker id %q is reserved", worker.ID)
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
	// Channel sources are the brain's channel bindings, read back on every
	// list; a client that sends them back does not store them.
	agent.Sources = slices.DeleteFunc(agent.Sources, func(source core.SuperAgentSource) bool {
		return source.Kind == core.SuperAgentSourceChannel
	})
	if agent.Sources == nil {
		agent.Sources = []core.SuperAgentSource{}
	}
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
	keys, err := requiredConfig(source)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if strings.TrimSpace(source.Config[key]) == "" {
			return fmt.Errorf("%s is required", key)
		}
	}
	return nil
}

// requiredConfig lists the settings a Source cannot run without. A GitHub
// Source that polls signs in with its token and has no webhook to verify.
func requiredConfig(source core.SuperAgentSource) ([]string, error) {
	switch source.Kind {
	case core.SuperAgentSourceFeishuMessage:
		return []string{"channel_id"}, nil
	case core.SuperAgentSourceFeishuDoc:
		return []string{"channel_id", "documents"}, nil
	case core.SuperAgentSourceEmail:
		return []string{"imap_host", "username", "password"}, nil
	case core.SuperAgentSourceGitHub:
		switch delivery := strings.TrimSpace(source.Config["delivery"]); delivery {
		case "", "webhook":
			return []string{"webhook_secret", "agent_login"}, nil
		case GitHubDeliveryPoll:
			return []string{"github_token", "agent_login"}, nil
		default:
			return nil, fmt.Errorf(`delivery must be "webhook" or %q, not %q`, GitHubDeliveryPoll, delivery)
		}
	}
	return nil, fmt.Errorf("unknown kind %q", source.Kind)
}

// requiredSecrets are the secrets among requiredConfig, which only the form
// collects.
func requiredSecrets(source core.SuperAgentSource) []string {
	keys, _ := requiredConfig(source)
	return slices.DeleteFunc(keys, func(key string) bool { return !slices.Contains(secretConfigKeys, key) })
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
