package sdkagent

import (
	"context"
	"os"

	"github.com/agentnexus/agentnexus/core"
	"github.com/agentnexus/agentnexus/framework"
)

// Register discovers installed SDK frameworks and registers a core.Agent for
// each so they show up as routable runtimes. It is idempotent per framework
// kind and safe to call once at startup. Uninstalled frameworks are skipped.
func Register() {
	pre := framework.DetectPrereqs()
	for _, spec := range framework.Catalog() {
		if spec.KindType != framework.KindSDK || !spec.Supported {
			continue
		}
		st := framework.Detect(spec, pre)
		if !st.Installed {
			continue
		}
		kind := spec.Kind
		if core.HasAgent(kind) {
			continue
		}
		core.RegisterAgent(kind, func(cfg map[string]any) (core.Agent, error) {
			return newAgent(kind, cfg), nil
		})
	}
}

// Agent is a core.Agent backed by an SDK framework hosted in the sidecar.
type Agent struct {
	kind         string
	systemPrompt string
	model        string
	env          map[string]string
}

func newAgent(kind string, cfg map[string]any) *Agent {
	a := &Agent{kind: kind}
	if v, ok := cfg["system_prompt"].(string); ok {
		a.systemPrompt = v
	}
	if v, ok := cfg["model"].(string); ok {
		a.model = v
	}
	if env, ok := cfg["env"].(map[string]string); ok {
		a.env = env
	}
	return a
}

// Name returns the framework kind.
func (a *Agent) Name() string { return a.kind }

// StartSession opens a turn-based session bound to workDir.
func (a *Agent) StartSession(ctx context.Context, workDir string) (core.AgentSession, error) {
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	c, err := getClient()
	if err != nil {
		return nil, err
	}
	return &session{agent: a, client: c, workDir: workDir, id: a.kind + "-" + newID()}, nil
}

// ListSessions returns no persistent sessions.
func (a *Agent) ListSessions(ctx context.Context) ([]string, error) { return nil, nil }

// Stop is a no-op; the sidecar process is shared and long-lived.
func (a *Agent) Stop(ctx context.Context) error { return nil }

type session struct {
	agent   *Agent
	client  *client
	workDir string
	id      string
}

func (s *session) ID() string { return s.id }

func (s *session) Send(ctx context.Context, text string) (<-chan *core.Event, error) {
	out := make(chan *core.Event, 16)
	req := request{
		Kind:         s.agent.kind,
		Prompt:       text,
		SystemPrompt: s.agent.systemPrompt,
		WorkDir:      s.workDir,
		Model:        s.agent.model,
		Env:          s.agent.env,
	}
	go s.client.run(ctx, req, out)
	return out, nil
}

func (s *session) RespondPermission(ctx context.Context, allow bool) error { return nil }
func (s *session) Close(ctx context.Context) error                         { return nil }
