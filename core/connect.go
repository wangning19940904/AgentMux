package core

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// ConnectService supervises console-managed channels and triggers: it loads
// them from the store, attaches enabled channels to the Engine, schedules
// cron triggers and fans engine lifecycle events out to event triggers. It is
// the runtime behind the console's "Channels & Triggers" panel.
type ConnectService struct {
	log   *slog.Logger
	eng   *Engine
	store ConnectStore
	sched *Scheduler

	mu  sync.Mutex
	ctx context.Context
}

// NewConnectService wires the service onto an engine and store. It registers
// itself as the engine's event sink; construct it before Engine.Start.
func NewConnectService(log *slog.Logger, eng *Engine, store ConnectStore) *ConnectService {
	if log == nil {
		log = slog.Default()
	}
	c := &ConnectService{log: log, eng: eng, store: store}
	c.sched = NewScheduler(log, c.runScheduled)
	eng.SetEventSink(c.onEvent)
	return c
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
		c.sched.Stop()
	}()
	c.log.Info("connect runtime started", "cron_entries", c.sched.Scheduled())
	return nil
}

// Stop halts the scheduler (idempotent; also happens on ctx cancellation).
func (c *ConnectService) Stop() { c.sched.Stop() }

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
		agent, workDir := c.resolveAgent(ctx, ch.AgentID)
		if err := c.eng.AttachChannel(runCtx, ch, agent, workDir); err != nil {
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
	agent, workDir := c.resolveAgent(ctx, ch.AgentID)
	return c.eng.AttachChannel(c.baseCtx(), *ch, agent, workDir)
}

// ChannelStatuses reports the live state of attached channels.
func (c *ConnectService) ChannelStatuses() []ChannelStatus {
	return c.eng.ChannelStatuses()
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
	fallbackAgent, fallbackWorkDir := c.resolveAgent(runCtx, tr.AgentID)
	_, err = c.eng.ExecuteTrigger(runCtx, *tr, fallbackAgent, fallbackWorkDir, input)
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
func (c *ConnectService) resolveAgent(ctx context.Context, agentID string) (Agent, string) {
	if agentID == "" {
		return nil, ""
	}
	inst, err := c.store.GetAgentInstance(ctx, agentID)
	if err != nil || inst == nil {
		c.log.Warn("agent instance not found", "agent_id", agentID, "err", err)
		return nil, ""
	}
	cfg := map[string]any{
		"work_dir":      inst.WorkDir,
		"system_prompt": inst.SystemPrompt,
	}
	if len(inst.Env) > 0 {
		cfg["env"] = inst.Env
	}
	agent, err := CreateAgent(inst.RuntimeID, cfg)
	if err != nil {
		c.log.Error("create agent runtime", "runtime", inst.RuntimeID, "err", err)
		return nil, ""
	}
	return agent, inst.WorkDir
}
