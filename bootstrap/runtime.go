package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/eventrelay"
	"github.com/wangning19940904/AgentMux/framework"
	"github.com/wangning19940904/AgentMux/guard"
	"github.com/wangning19940904/AgentMux/mcp"
	"github.com/wangning19940904/AgentMux/memory"
	"github.com/wangning19940904/AgentMux/provider"
	"github.com/wangning19940904/AgentMux/server"
	"github.com/wangning19940904/AgentMux/skills"
	"github.com/wangning19940904/AgentMux/store"
	"github.com/wangning19940904/AgentMux/superagent"
	"github.com/wangning19940904/AgentMux/usage"
	"github.com/wangning19940904/AgentMux/workspace"
)

type runtimeResult struct {
	name string
	err  error
}

// Runtime is the shared daemon composition root used by both CLI and Desktop.
// It owns startup, cancellation, shutdown, and waiting for every long-running
// component assembled by bootstrap.
type Runtime struct {
	EventRelay  *eventrelay.Service
	SuperAgents *superagent.Service
	ctx         context.Context
	cancel      context.CancelFunc
	log         *slog.Logger
	Server      *server.Server
	Provider    *provider.Service
	Usage       *usage.Engine
	Engine      *core.Engine
	Connect     *core.ConnectService

	mu          sync.Mutex
	started     bool
	errCh       chan runtimeResult
	stop        sync.Once
	authRefresh func(context.Context)
	authWG      sync.WaitGroup
	listener    net.Listener
	proxyWG     sync.WaitGroup
}

// NewRuntime assembles the complete daemon under one cancellable lifecycle.
func NewRuntime(parent context.Context, log *slog.Logger, cfg *config.Config, st *store.Store, version string, strictObservability bool) (*Runtime, error) {
	if parent == nil {
		parent = context.Background()
	}
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithCancel(parent)
	if cfg == nil {
		cancel()
		return nil, fmt.Errorf("server config is required")
	}
	if err := cfg.ValidateListenSecurity(); err != nil {
		cancel()
		return nil, err
	}
	if len(cfg.Projects) > 0 || len(cfg.Hooks) > 0 {
		cancel()
		return nil, fmt.Errorf("config.toml projects/hooks are no longer runtime resources; run `amux database import-config --apply`, then remove those sections")
	}
	listener, err := waitForDaemonListener(ctx, log, cfg.Server.Addr, daemonRetryInterval)
	if err != nil {
		cancel()
		return nil, err
	}
	initialized := false
	defer func() {
		if !initialized {
			cancel()
			_ = listener.Close()
		}
	}()
	if keys, err := framework.InheritShellProxyEnvironment(ctx); err != nil {
		log.Warn("shell proxy settings unavailable; using service environment", "err", err)
	} else if len(keys) > 0 {
		log.Info("inherited shell proxy settings", "variables", keys)
	}
	framework.PrepareRuntimeEnvironment()
	skillManager := skills.NewPersistent(st)
	initializer := workspace.NewWithSkillManager(skillManager)
	engine, err := server.BuildEngine(log, cfg, initializer)
	if err != nil {
		cancel()
		return nil, err
	}
	engine.SetConversationStore(st)
	memoryStore := memory.New(st)
	guardGate := guard.New(st, core.GuardAsk)
	engine.SetMemoryStore(memoryStore)
	engine.SetGuard(guardGate)
	providerService := provider.NewService(log, st, cfg.Provider.ProxyAddr)
	usageEngine := usage.NewEngine(cfg, st, log)
	backfill := time.Duration(cfg.Observability.BackfillDays) * 24 * time.Hour
	go usageEngine.Start(ctx, backfill)
	relay := eventrelay.New(st, log, engine)
	connect := core.NewConnectService(log, engine, st)
	connect.SetCLINoteResolver(func(ids []string) []core.CLINote {
		return cliNotes(ctx, st, ids)
	})
	mcpRegistry := mcp.New(st)
	connect.SetMCPRegistry(mcpRegistry)
	reporter := func(ctx context.Context, period string, since, until time.Time, location *time.Location) (any, error) {
		return usageEngine.ReportRangeInLocation(ctx, period, since, until, location)
	}
	srv := server.New(server.Dependencies{
		Config: cfg, Version: version, Log: log, Store: st, EventRelay: relay,
		Provider: providerService, ProviderSvc: providerService,
		Usage: reporter, UsageSources: usageEngine, Sender: engine, Invoker: connect, Connect: connect,
		Presets: provider.Presets(), Memory: memoryStore, Skills: skillManager, MCP: mcpRegistry,
		Guard: guardGate, Workspace: initializer,
		ModuleRuntime: map[string]server.ModuleRuntimeState{
			"memory": {RuntimeActive: true},
			"skills": {RuntimeActive: true},
			"mcp":    {RuntimeActive: true},
			"guard":  {RuntimeActive: true, Enforced: true},
		},
	})
	feishuAPI := &superagent.FeishuAPI{Channels: st.GetChannel, Grants: st}
	// Push notifications: agents waiting for a person, and Tickets that
	// finished or failed. A nil push service passes both through unchanged.
	var superAgentStore superagent.Store = st
	if push := srv.Push(); push != nil {
		engine.SubscribeEventSink(push.EngineSink(st))
		superAgentStore = push.TicketStore(st)
	}
	superAgents := superagent.New(superagent.Options{
		Store: superAgentStore, Invoker: connect, Log: log,
		Workers:          &superagent.Fleet{Invoker: connect, Agents: st, Hosts: srv.RemoteHosts()},
		Replies:          &superagent.Replies{Chats: engine},
		Mail:             &superagent.IMAPClient{},
		GitHub:           &superagent.GitHubClient{},
		Docs:             feishuAPI,
		Directory:        feishuAPI,
		Scopes:           feishuAPI,
		RuntimeAvailable: server.AgentRuntimeAvailable,
		MCP:              mcpRegistry,
		ChannelHasAgent: func(ctx context.Context, id string) bool {
			channel, err := st.GetChannel(ctx, id)
			return err != nil || (channel != nil && channel.AgentID != "")
		},
	})
	engine.SetInboundInterceptor(superAgents.InterceptMessage)
	engine.SetBrainHook(superAgents)
	relay.SetLocalConsumer(superAgents)
	srv.SetSuperAgents(superAgents, feishuAPI)
	if cfg.Observability.Enabled {
		if obsErr := attachObservability(ctx, log, cfg, st, srv, engine, providerService, usageEngine); obsErr != nil {
			if strictObservability {
				cancel()
				return nil, obsErr
			}
			log.Error("observability unavailable; continuing without it", "err", obsErr)
		}
	}
	initialized = true
	return &Runtime{
		ctx: ctx, cancel: cancel, log: log, Server: srv, Provider: providerService, EventRelay: relay, SuperAgents: superAgents,
		Usage: usageEngine, Engine: engine, Connect: connect, listener: listener,
		authRefresh: func(ctx context.Context) { maintainFrameworkAuth(ctx, st, log) },
	}, nil
}

// Start launches the HTTP server and Engine and starts the channel runtime.
// It is safe to call once; a second call returns an error.
func (r *Runtime) Start() error {
	if r == nil {
		return fmt.Errorf("runtime is nil")
	}
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return fmt.Errorf("runtime is already started")
	}
	r.started = true
	r.errCh = make(chan runtimeResult, 2)
	if r.authRefresh != nil {
		r.authWG.Add(1)
		go func() {
			defer r.authWG.Done()
			r.authRefresh(r.ctx)
		}()
	}
	r.mu.Unlock()

	if r.EventRelay != nil {
		if err := r.EventRelay.Start(r.ctx); err != nil {
			r.cancel()
			return fmt.Errorf("start event relay: %w", err)
		}
	}
	// Before the HTTP server and channels: from their first request and message,
	// webhook Sources must resolve and claimed chats must not reach channel Agents.
	if err := r.SuperAgents.Start(r.ctx); err != nil {
		r.cancel()
		return fmt.Errorf("start super agents: %w", err)
	}
	r.startProxyRestore(daemonRetryInterval)
	go func() { r.errCh <- runtimeResult{name: "http", err: r.Server.Serve(r.ctx, r.listener)} }()
	go func() { r.errCh <- runtimeResult{name: "engine", err: r.Engine.Start(r.ctx)} }()
	if err := r.Connect.Start(r.ctx); err != nil {
		r.Stop()
		return fmt.Errorf("start connect runtime: %w", err)
	}
	return nil
}

// A transient proxy bind failure must not leave enabled tools disconnected for
// the lifetime of the daemon. Each retry reads current takeover settings so a
// tool disabled while waiting stays disabled.
func (r *Runtime) startProxyRestore(retryInterval time.Duration) {
	if err := r.Provider.RestoreProxyState(r.ctx); err == nil {
		return
	} else {
		r.log.Warn("local routing restore failed; will retry", "err", err, "retry_in", retryInterval)
	}
	r.proxyWG.Add(1)
	go func() {
		defer r.proxyWG.Done()
		ticker := time.NewTicker(retryInterval)
		defer ticker.Stop()
		for {
			select {
			case <-r.ctx.Done():
				return
			case <-ticker.C:
				if r.ctx.Err() != nil {
					return
				}
				if err := r.Provider.RestoreProxyState(r.ctx); err != nil {
					r.log.Warn("local routing restore failed; will retry", "err", err, "retry_in", retryInterval)
					continue
				}
				r.log.Info("local routing state restored")
				return
			}
		}
	}()
}

// Wait blocks until cancellation or one long-running component exits, then
// cancels and joins the remaining components before returning.
func (r *Runtime) Wait() error {
	if r == nil {
		return fmt.Errorf("runtime is nil")
	}
	r.mu.Lock()
	started, errCh := r.started, r.errCh
	r.mu.Unlock()
	if !started || errCh == nil {
		return fmt.Errorf("runtime is not started")
	}

	results := make([]runtimeResult, 0, 2)
	select {
	case result := <-errCh:
		results = append(results, result)
	case <-r.ctx.Done():
	}
	r.Stop()
	deadline := time.NewTimer(7 * time.Second)
	defer deadline.Stop()
	for len(results) < 2 {
		select {
		case result := <-errCh:
			results = append(results, result)
		case <-deadline.C:
			return fmt.Errorf("runtime shutdown timed out waiting for components")
		}
	}
	var joined error
	for _, result := range results {
		if result.err == nil || errors.Is(result.err, context.Canceled) || errors.Is(result.err, http.ErrServerClosed) {
			continue
		}
		joined = errors.Join(joined, fmt.Errorf("%s stopped: %w", result.name, result.err))
	}
	return joined
}

// Stop is idempotent and releases every lifecycle-owned component.
func (r *Runtime) Stop() {
	if r == nil {
		return
	}
	r.stop.Do(func() {
		r.cancel()
		r.authWG.Wait()
		r.proxyWG.Wait()
		if r.SuperAgents != nil {
			r.SuperAgents.Stop()
		}
		if r.EventRelay != nil {
			r.EventRelay.Stop()
		}
		if r.Connect != nil {
			r.Connect.Stop()
		}
		if r.Provider != nil && r.Provider.Proxy() != nil {
			_ = r.Provider.Proxy().Stop()
		}
		if r.listener != nil {
			_ = r.listener.Close()
		}
		if r.Server != nil {
			r.Server.Push().Close()
		}
	})
}

// Run is Start followed by Wait.
func (r *Runtime) Run() error {
	if err := r.Start(); err != nil {
		return err
	}
	return r.Wait()
}
