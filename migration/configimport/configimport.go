// Package configimport converts legacy config.toml projects and hooks into the
// PostgreSQL-backed Agent, Channel, and Trigger resource model.
package configimport

import (
	"context"
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

var unsafeID = regexp.MustCompile(`[^a-z0-9]+`)

// Build normalizes legacy config resources without mutating either source or
// destination. IDs depend on logical identity rather than list ordering.
func Build(cfg *config.Config, now time.Time) (store.ResourceImportSet, error) {
	var resources store.ResourceImportSet
	if cfg == nil {
		return resources, fmt.Errorf("config is required")
	}
	now = now.UTC()
	seenProjects := map[string]bool{}
	for _, project := range cfg.Projects {
		name := strings.TrimSpace(project.Name)
		if name == "" {
			return resources, fmt.Errorf("project name is required")
		}
		key := strings.ToLower(name)
		if seenProjects[key] {
			return resources, fmt.Errorf("duplicate project name %q", name)
		}
		seenProjects[key] = true
		agentID := stableID("agent-config", name)
		workspaceMode := strings.ToLower(strings.TrimSpace(project.WorkspaceMode))
		if workspaceMode == "" {
			workspaceMode = "shared"
		}
		sessionBackend := strings.ToLower(strings.TrimSpace(project.SessionBackend))
		if sessionBackend == "" {
			sessionBackend = "structured"
		}
		env := make(map[string]string, len(project.Env))
		for key, value := range project.Env {
			env[key] = value
		}
		resources.Agents = append(resources.Agents, core.AgentInstance{
			ID: agentID, Name: name, RuntimeID: strings.TrimSpace(project.Agent),
			WorkDir: project.WorkDir, WorkspaceMode: workspaceMode,
			WorktreeBaseRef: project.WorktreeBaseRef, SessionBackend: sessionBackend,
			SystemPrompt: project.SystemPrompt, ProviderTool: strings.TrimSpace(project.Agent),
			DefaultModel: project.DefaultModel, MemoryScope: "agent:" + agentID,
			Env: env, Enabled: true, Source: "config-import", Visibility: core.VisibilityPrivate,
			CreatedAt: now, UpdatedAt: now,
		})
		for index, raw := range project.Platforms {
			typ, _ := raw["type"].(string)
			typ = strings.TrimSpace(typ)
			if typ == "" {
				return resources, fmt.Errorf("project %q platform %d has no type", name, index+1)
			}
			channelID := stableID("channel-config", name+"\x00"+typ+fmt.Sprintf("\x00%d", index))
			channelConfig := map[string]string{}
			for key, value := range raw {
				if key == "type" || value == nil {
					continue
				}
				channelConfig[key] = fmt.Sprint(value)
			}
			channelName := name + " " + typ
			if index > 0 {
				channelName += fmt.Sprintf(" %d", index+1)
			}
			resources.Channels = append(resources.Channels, core.Channel{
				ID: channelID, Name: channelName, Type: typ, AgentID: agentID,
				Config: channelConfig, Enabled: true, Visibility: core.VisibilityPrivate,
				CreatedAt: now, UpdatedAt: now,
			})
		}
	}
	for _, hook := range cfg.Hooks {
		target := strings.TrimSpace(hook.Command)
		if strings.EqualFold(hook.Type, core.ActionHTTP) {
			target = strings.TrimSpace(hook.URL)
		}
		identity := strings.Join([]string{hook.Event, hook.Type, target}, "\x00")
		resources.Triggers = append(resources.Triggers, core.Trigger{
			ID:   stableID("trigger-config", identity),
			Name: strings.TrimSpace(hook.Event) + " " + strings.TrimSpace(hook.Type),
			Kind: core.TriggerEvent, Event: strings.TrimSpace(hook.Event),
			ActionType: strings.TrimSpace(hook.Type), ActionTarget: target,
			Enabled: true, CreatedAt: now, UpdatedAt: now,
		})
	}
	return resources, nil
}

// Import builds and plans/applies one legacy config resource set.
func Import(ctx context.Context, st *store.Store, cfg *config.Config, dryRun bool) (store.ResourceImportReport, error) {
	if st == nil {
		return store.ResourceImportReport{DryRun: dryRun}, fmt.Errorf("store is required")
	}
	resources, err := Build(cfg, time.Now())
	if err != nil {
		return store.ResourceImportReport{DryRun: dryRun}, err
	}
	return st.ImportResources(ctx, resources, dryRun)
}

func stableID(prefix, identity string) string {
	slug := strings.Trim(unsafeID.ReplaceAllString(strings.ToLower(strings.TrimSpace(identity)), "-"), "-")
	if slug == "" {
		slug = "resource"
	}
	if len(slug) > 30 {
		slug = strings.Trim(slug[:30], "-")
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(prefix+"\x00"+identity)))[:10]
	return prefix + "-" + slug + "-" + digest
}
