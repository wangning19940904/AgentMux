package server

import (
	"log/slog"

	"github.com/agentnexus/agentnexus/config"
	"github.com/agentnexus/agentnexus/core"
)

// BuildEngine constructs the Engine with config.toml hooks and projects.
// Shared by `anx serve`, `anx web` and the desktop shell so the channel &
// trigger runtime behaves identically everywhere.
func BuildEngine(log *slog.Logger, cfg *config.Config) (*core.Engine, error) {
	var hookList []core.Hook
	for _, h := range cfg.Hooks {
		hookList = append(hookList, core.Hook{
			Event: core.HookEvent(h.Event), Type: h.Type,
			Command: h.Command, URL: h.URL,
		})
	}
	hooks := core.NewHookRunner(log, hookList)
	eng := core.NewEngine(log, hooks)

	for _, p := range cfg.Projects {
		ag, err := core.CreateAgent(p.Agent, map[string]any{
			"work_dir": p.WorkDir, "system_prompt": p.SystemPrompt, "env": p.Env,
		})
		if err != nil {
			return nil, err
		}
		var plats []core.Platform
		for _, pc := range p.Platforms {
			typ, _ := pc["type"].(string)
			plat, err := core.CreatePlatform(typ, pc)
			if err != nil {
				return nil, err
			}
			plats = append(plats, plat)
		}
		eng.AddProject(p.Name, p.WorkDir, ag, plats)
	}
	return eng, nil
}
