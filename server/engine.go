package server

import (
	"log/slog"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
)

// BuildEngine constructs the Engine. Runtime resources now come exclusively
// from PostgreSQL; legacy config.toml projects and hooks are handled only by
// `amux database import-config`.
// Shared by `amux serve`, `amux web` and the desktop shell so the channel &
// trigger runtime behaves identically everywhere.
func BuildEngine(log *slog.Logger, cfg *config.Config, initializer ...core.WorkspaceInitializer) (*core.Engine, error) {
	_ = cfg // retained until the unified runtime constructor replaces this API
	hooks := core.NewHookRunner(log, nil)
	eng := core.NewEngine(log, hooks)
	eng.SetMessageLogger(core.NewMessageLogger(""))
	if len(initializer) > 0 {
		eng.SetWorkspaceInitializer(initializer[0])
	}
	return eng, nil
}
