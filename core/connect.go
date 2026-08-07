package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// ConnectService supervises console-managed channels and triggers: it loads
// them from the store, attaches enabled channels to the Engine, schedules
// cron triggers and fans engine lifecycle events out to event triggers. It is
// the runtime behind the console's "Channels & Triggers" panel.
type ConnectService struct {
	log      *slog.Logger
	eng      *Engine
	store    ConnectStore
	sched    *Scheduler
	cliNotes CLINoteResolver

	mu                sync.Mutex
	ctx               context.Context
	unsubscribeEvents func()
}

// CLINoteResolver maps managed CLI ids to display notes for prompt
// injection. It is injected by the daemon bootstrap so core does not depend
// on the tools catalog.
type CLINoteResolver func(ids []string) []CLINote

// SetCLINoteResolver installs the CLI catalog lookup used when composing
// agent system prompts. A nil resolver disables CLI notes.
func (c *ConnectService) SetCLINoteResolver(resolver CLINoteResolver) {
	c.cliNotes = resolver
}

// NewConnectService wires the service onto an engine and store. It registers
// an additive lifecycle subscription; construct it before Engine.Start.
func NewConnectService(log *slog.Logger, eng *Engine, store ConnectStore) *ConnectService {
	if log == nil {
		log = slog.Default()
	}
	c := &ConnectService{log: log, eng: eng, store: store}
	c.sched = NewScheduler(log, c.runScheduled)
	c.unsubscribeEvents = eng.SubscribeEventSink(c.onEvent)
	eng.SetRuntimeSettingsDefaultStore(c)
	return c
}

// UpdateAgentRuntimeSettings satisfies Engine's optional default-settings
// store. It is called by the interactive picker after core validation.
func (c *ConnectService) UpdateAgentRuntimeSettings(ctx context.Context, id string, settings RuntimeSettings) error {
	if c.store == nil {
		return fmt.Errorf("agent settings store unavailable")
	}
	return c.store.UpdateAgentRuntimeSettings(ctx, id, settings)
}

// Start attaches enabled channels, schedules cron triggers and starts the
// scheduler. It returns immediately; ctx cancellation stops the scheduler
// (channels are torn down by Engine shutdown).
func (c *ConnectService) Start(ctx context.Context) error {
	c.mu.Lock()
	c.ctx = ctx
	c.mu.Unlock()
	if err := c.ReloadChannels(ctx); err != nil {
		c.log.Error("load channels", "err", err)
	}
	if err := c.ReloadTriggers(ctx); err != nil {
		c.log.Error("load triggers", "err", err)
	}
	c.sched.Start()
	go func() {
		<-ctx.Done()
		c.Stop()
	}()
	c.log.Info("connect runtime started", "cron_entries", c.sched.Scheduled())
	return nil
}

// Stop halts the scheduler and removes the additive lifecycle subscription.
// Both operations are idempotent and also happen on context cancellation.
func (c *ConnectService) Stop() {
	c.sched.Stop()
	if c.unsubscribeEvents != nil {
		c.unsubscribeEvents()
	}
}

func (c *ConnectService) baseCtx() context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx != nil {
		return c.ctx
	}
	return context.Background()
}

// ReloadChannels diffs stored channels against attached runtimes: removed or
// disabled channels are detached, new or changed ones (re)attached.
func (c *ConnectService) ReloadChannels(ctx context.Context) error {
	channels, err := c.store.ListChannels(ctx)
	if err != nil {
		return err
	}
	desired := map[string]Channel{}
	for _, ch := range channels {
		if ch.Enabled {
			desired[ch.ID] = ch
		}
	}
	for id, version := range c.eng.AttachedChannels() {
		ch, ok := desired[id]
		if ok && ch.UpdatedAt.Equal(version) {
			delete(desired, id) // unchanged, keep running
			continue
		}
		if !ok {
			c.eng.DetachChannel(id)
		}
	}
	runCtx := c.baseCtx()
	for _, ch := range desired {
		agent, workDir, workspace := c.resolveAgent(ctx, ch.AgentID)
		if err := c.eng.AttachChannel(runCtx, ch, agent, workDir, workspace); err != nil {
			c.log.Error("attach channel", "channel", ch.Name, "type", ch.Type, "err", err)
		}
	}
	return nil
}

// ReloadTriggers re-syncs the cron scheduler with stored triggers.
func (c *ConnectService) ReloadTriggers(ctx context.Context) error {
	triggers, err := c.store.ListTriggers(ctx)
	if err != nil {
		return err
	}
	c.sched.Sync(triggers)
	return nil
}

// RestartChannel tears down and re-attaches one channel.
func (c *ConnectService) RestartChannel(ctx context.Context, id string) error {
	ch, err := c.store.GetChannel(ctx, id)
	if err != nil {
		return err
	}
	if ch == nil {
		return fmt.Errorf("channel %q not found", id)
	}
	c.eng.DetachChannel(id)
	if !ch.Enabled {
		return nil
	}
	agent, workDir, workspace := c.resolveAgent(ctx, ch.AgentID)
	return c.eng.AttachChannel(c.baseCtx(), *ch, agent, workDir, workspace)
}

// RestartChannelsForAgent refreshes every enabled channel bound to agentID.
// Agent records can change independently from Channel records, so relying on
// Channel.UpdatedAt alone would leave the old in-memory runtime serving new
// messages until the daemon or channel was restarted manually.
func (c *ConnectService) RestartChannelsForAgent(ctx context.Context, agentID string) error {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil
	}
	channels, err := c.store.ListChannels(ctx)
	if err != nil {
		return err
	}
	var restartErrs []error
	for _, ch := range channels {
		if !ch.Enabled || ch.AgentID != agentID {
			continue
		}
		if err := c.RestartChannel(ctx, ch.ID); err != nil {
			restartErrs = append(restartErrs, fmt.Errorf("restart channel %q: %w", ch.Name, err))
		}
	}
	return errors.Join(restartErrs...)
}

// ChannelStatuses reports the live state of attached channels.
func (c *ConnectService) ChannelStatuses() []ChannelStatus {
	return c.eng.ChannelStatuses()
}

func (c *ConnectService) ChannelCodexControlCapability(channelID string) (CodexControlCapability, bool) {
	return c.eng.ChannelCodexControlCapability(channelID)
}

func (c *ConnectService) BindChannelConversation(ctx context.Context, channelID, conversationID, threadID string) error {
	return c.eng.BindChannelConversation(ctx, channelID, conversationID, threadID)
}

func (c *ConnectService) ResolveChannelInteractionLocal(ctx context.Context, action AgentInteractionAction) error {
	return c.eng.ResolveChannelInteractionLocal(ctx, action)
}

// RunTriggerNow executes a trigger asynchronously (manual run: fires even
// when the trigger is disabled). input is appended to the prompt, e.g. a
// webhook payload.
func (c *ConnectService) RunTriggerNow(id, input string) {
	go c.runTrigger(c.baseCtx(), id, input, true)
}

// runScheduled is the cron callback.
func (c *ConnectService) runScheduled(id string) {
	c.runTrigger(c.baseCtx(), id, "", false)
}

func (c *ConnectService) runTrigger(ctx context.Context, id, input string, manual bool) {
	tr, err := c.store.GetTrigger(ctx, id)
	if err != nil || tr == nil {
		c.log.Error("trigger not found", "trigger_id", id, "err", err)
		return
	}
	if !manual && !tr.Enabled {
		return
	}
	start := time.Now()

	// Manual run of an event trigger tests its action with sample data.
	if tr.Kind == TriggerEvent {
		err := RunHookAction(ctx, tr.ActionType, tr.ActionTarget, tr.ActionTarget, map[string]string{
			"event":      tr.Event,
			"trigger_id": tr.ID,
			"trigger":    tr.Name,
			"test":       "true",
		})
		c.recordRun(tr, start, err)
		return
	}

	_ = c.store.UpdateTriggerRun(context.Background(), tr.ID, start, "running", "")
	runCtx, cancel := context.WithTimeout(ctx, DefaultTriggerTimeout)
	defer cancel()
	fallbackAgent, fallbackWorkDir, workspace := c.resolveAgent(runCtx, tr.AgentID)
	_, err = c.eng.ExecuteTrigger(runCtx, *tr, fallbackAgent, fallbackWorkDir, input, workspace)
	c.recordRun(tr, start, err)
}

func (c *ConnectService) recordRun(tr *Trigger, start time.Time, err error) {
	status, errMsg := "ok", ""
	if err != nil {
		status, errMsg = "error", err.Error()
		c.log.Error("trigger run failed", "trigger", tr.Name, "kind", tr.Kind, "err", err)
	} else {
		c.log.Info("trigger run ok", "trigger", tr.Name, "kind", tr.Kind, "took", time.Since(start).Round(time.Millisecond))
	}
	if uerr := c.store.UpdateTriggerRun(context.Background(), tr.ID, start, status, errMsg); uerr != nil {
		c.log.Error("record trigger run", "trigger", tr.Name, "err", uerr)
	}
}

// onEvent is the Engine event sink: it fans lifecycle events out to enabled
// event triggers, optionally filtered by channel.
func (c *ConnectService) onEvent(event HookEvent, data map[string]string) {
	go c.processEvent(event, data)
}

func (c *ConnectService) processEvent(event HookEvent, data map[string]string) {
	ctx := c.baseCtx()
	triggers, err := c.store.ListTriggers(ctx)
	if err != nil {
		c.log.Error("list triggers for event", "event", event, "err", err)
		return
	}
	for _, tr := range triggers {
		if tr.Kind != TriggerEvent || !tr.Enabled || tr.Event != string(event) {
			continue
		}
		if tr.ChannelID != "" && tr.ChannelID != data["channel_id"] {
			continue
		}
		tr := tr
		go func() {
			start := time.Now()
			payload := make(map[string]string, len(data)+2)
			for k, v := range data {
				payload[k] = v
			}
			payload["trigger_id"] = tr.ID
			payload["trigger"] = tr.Name
			err := RunHookAction(ctx, tr.ActionType, tr.ActionTarget, tr.ActionTarget, payload)
			status, errMsg := "ok", ""
			if err != nil {
				status, errMsg = "error", err.Error()
				c.log.Error("event trigger failed", "trigger", tr.Name, "event", event, "err", err)
			}
			_ = c.store.UpdateTriggerRun(context.Background(), tr.ID, start, status, errMsg)
		}()
	}
}

// resolveAgent builds an Agent runtime for a stored agent instance id.
// Returns (nil, "") when the id is empty or the instance cannot be resolved.
func (c *ConnectService) resolveAgent(ctx context.Context, agentID string) (Agent, string, WorkspaceInitOptions) {
	if agentID == "" {
		return nil, "", WorkspaceInitOptions{}
	}
	inst, err := c.store.GetAgentInstance(ctx, agentID)
	if err != nil || inst == nil {
		c.log.Warn("agent instance not found", "agent_id", agentID, "err", err)
		return nil, "", WorkspaceInitOptions{}
	}
	providerDefaults := c.agentProviderRuntimeDefaults(ctx, inst)
	runtimeDefaults := RuntimeSettings{
		Model:           inst.DefaultModel,
		ReasoningEffort: inst.DefaultReasoningEffort,
		ServiceTier:     inst.DefaultServiceTier,
		ApprovalMode:    inst.DefaultApprovalMode,
	}
	if runtimeDefaults.ReasoningEffort == "" {
		runtimeDefaults.ReasoningEffort = providerDefaults.ReasoningEffort
	}
	if runtimeDefaults.ServiceTier == "" {
		runtimeDefaults.ServiceTier = providerDefaults.ServiceTier
	}
	workspace := WorkspaceInitOptions{
		AgentID:         inst.ID,
		RuntimeID:       inst.RuntimeID,
		WorkDir:         inst.WorkDir,
		Skills:          append([]string(nil), inst.Skills...),
		MCPServers:      append([]string(nil), inst.MCPServers...),
		RuntimeDefaults: runtimeDefaults,
	}
	cfg := map[string]any{
		"work_dir":      inst.WorkDir,
		"system_prompt": c.composeAgentPrompt(ctx, inst),
	}
	if runtimeDefaults.Model != "" {
		cfg["model"] = runtimeDefaults.Model
	}
	if runtimeDefaults.ReasoningEffort != "" {
		cfg["reasoning_effort"] = runtimeDefaults.ReasoningEffort
	}
	if runtimeDefaults.ServiceTier != "" {
		cfg["service_tier"] = runtimeDefaults.ServiceTier
	}
	if runtimeDefaults.ApprovalMode != "" {
		cfg["approval_mode"] = runtimeDefaults.ApprovalMode
	}
	if modes := ApprovalModeValuesForRuntime(inst.RuntimeID); len(modes) > 0 {
		cfg["supported_approval_modes"] = modes
	}
	if models := c.agentModelOptions(ctx, inst); len(models) > 0 {
		cfg["supported_models"] = models
	}
	if caps := c.agentRuntimeSettingsCapabilities(ctx, inst); len(caps.ReasoningEfforts) > 0 || len(caps.ServiceTiers) > 0 {
		values := make([]string, 0, len(caps.ReasoningEfforts))
		for _, option := range caps.ReasoningEfforts {
			values = append(values, option.Value)
		}
		cfg["supported_reasoning_efforts"] = values
		values = values[:0]
		for _, option := range caps.ServiceTiers {
			values = append(values, option.Value)
		}
		if len(values) > 0 {
			cfg["supported_service_tiers"] = values
		}
	}
	if len(inst.Env) > 0 {
		cfg["env"] = inst.Env
	}
	agent, err := CreateAgent(inst.RuntimeID, cfg)
	if err != nil {
		c.log.Error("create agent runtime", "runtime", inst.RuntimeID, "err", err)
		return nil, "", workspace
	}
	return agent, inst.WorkDir, workspace
}

func (c *ConnectService) agentProviderRuntimeDefaults(ctx context.Context, inst *AgentInstance) RuntimeSettings {
	if c.store == nil || inst == nil {
		return RuntimeSettings{}
	}
	p, err := c.agentRuntimeProvider(ctx, inst)
	if err != nil || p == nil {
		return RuntimeSettings{}
	}
	return RuntimeSettings{
		ReasoningEffort: p.Meta.DefaultReasoningEffort,
		ServiceTier:     p.Meta.DefaultServiceTier,
	}
}

func (c *ConnectService) agentRuntimeProvider(ctx context.Context, inst *AgentInstance) (*Provider, error) {
	if c.store == nil || inst == nil {
		return nil, nil
	}
	if inst.ProviderID != "" {
		return c.store.GetProvider(ctx, inst.ProviderID)
	}
	tool := inst.ProviderTool
	if tool == "" {
		tool = inst.RuntimeID
	}
	routes, err := c.store.ActiveProviderRoutes(ctx)
	if err != nil {
		return nil, err
	}
	want := NormalizeProviderTool(tool)
	for _, route := range routes {
		if route.Tool == tool || NormalizeProviderTool(route.Tool) == want {
			return c.store.GetProvider(ctx, route.ProviderID)
		}
	}
	return nil, nil
}

func (c *ConnectService) agentRuntimeSettingsCapabilities(ctx context.Context, inst *AgentInstance) RuntimeSettingsCapabilities {
	if c.store == nil || inst == nil {
		return RuntimeSettingsCapabilities{}
	}
	p, err := c.agentRuntimeProvider(ctx, inst)
	if err != nil || p == nil {
		return RuntimeSettingsCapabilities{}
	}
	return RuntimeSettingsCapabilities{
		Models:           RuntimeOptions(ProviderModelOptions(p)),
		ReasoningEfforts: RuntimeOptions(p.Meta.SupportedReasoningEfforts),
		ServiceTiers:     RuntimeOptions(p.Meta.SupportedServiceTiers),
	}
}

// composeAgentPrompt builds the injected system prompt for an agent instance:
// the user-configured prompt plus the event-callback log paths for its bound
// channels and descriptions of any enabled CLI tools.
func (c *ConnectService) composeAgentPrompt(ctx context.Context, inst *AgentInstance) string {
	logPaths := c.agentChannelLogPaths(ctx, inst.ID)
	clis := c.agentCLINotes(inst.CLIs)
	return ComposeSystemPrompt(inst.SystemPrompt, logPaths, clis)
}

// agentChannelLogPaths returns the message log paths for every enabled channel
// bound to agentID.
func (c *ConnectService) agentChannelLogPaths(ctx context.Context, agentID string) []string {
	logger := c.eng.MessageLogger()
	if logger == nil || agentID == "" {
		return nil
	}
	channels, err := c.store.ListChannels(ctx)
	if err != nil {
		c.log.Warn("list channels for prompt injection", "agent_id", agentID, "err", err)
		return nil
	}
	var paths []string
	for _, ch := range channels {
		if ch.Enabled && ch.AgentID == agentID {
			paths = append(paths, logger.ChannelLogPath(ch.ID))
		}
	}
	return paths
}

// agentCLINotes resolves the catalog description for each enabled CLI id
// through the injected resolver.
func (c *ConnectService) agentCLINotes(ids []string) []CLINote {
	if c.cliNotes == nil {
		return nil
	}
	return c.cliNotes(ids)
}

func (c *ConnectService) agentModelOptions(ctx context.Context, inst *AgentInstance) []string {
	if c.store == nil || inst == nil {
		return nil
	}
	var p *Provider
	var err error
	if inst.ProviderID != "" {
		p, err = c.store.GetProvider(ctx, inst.ProviderID)
		if err != nil {
			c.log.Warn("load agent provider", "agent_id", inst.ID, "provider_id", inst.ProviderID, "err", err)
			return nil
		}
		return ProviderModelOptions(p)
	}
	tool := inst.ProviderTool
	if tool == "" {
		tool = inst.RuntimeID
	}
	routes, err := c.store.ActiveProviderRoutes(ctx)
	if err != nil {
		c.log.Warn("load active provider routes", "agent_id", inst.ID, "err", err)
		return nil
	}
	want := NormalizeProviderTool(tool)
	for _, route := range routes {
		if route.Tool == tool || NormalizeProviderTool(route.Tool) == want {
			p, err = c.store.GetProvider(ctx, route.ProviderID)
			if err != nil {
				c.log.Warn("load active provider", "agent_id", inst.ID, "provider_id", route.ProviderID, "err", err)
				return nil
			}
			return ProviderModelOptions(p)
		}
	}
	return nil
}
