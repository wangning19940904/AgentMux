package configimport

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

func sampleConfig() *config.Config {
	return &config.Config{
		Projects: []config.ProjectConfig{{
			Name: "demo", Agent: "codex", WorkDir: "/tmp/demo",
			Platforms: []map[string]any{{"type": "slack", "bot_token": "secret"}},
		}},
		Hooks: []config.HookConfig{{Event: "error", Type: "http", URL: "https://example.com/hook"}},
	}
}

func TestBuildIsDeterministic(t *testing.T) {
	now := time.Unix(100, 0)
	first, err := Build(sampleConfig(), now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(sampleConfig(), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if first.Agents[0].ID != second.Agents[0].ID || first.Channels[0].ID != second.Channels[0].ID || first.Triggers[0].ID != second.Triggers[0].ID {
		t.Fatalf("resource ids are not deterministic: first=%+v second=%+v", first, second)
	}
}

func TestImportIsIdempotentAndRefusesConflicts(t *testing.T) {
	st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "import.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	first, err := Import(ctx, st, sampleConfig(), false)
	if err != nil || !first.Applied || first.Agents.Create != 1 || first.Channels.Create != 1 || first.Triggers.Create != 1 {
		t.Fatalf("first import = %+v, err=%v", first, err)
	}
	second, err := Import(ctx, st, sampleConfig(), false)
	if err != nil || !second.Applied || second.Agents.Unchanged != 1 || second.Channels.Unchanged != 1 || second.Triggers.Unchanged != 1 {
		t.Fatalf("second import = %+v, err=%v", second, err)
	}

	built, err := Build(sampleConfig(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	conflict := built.Agents[0]
	conflict.Name = "Console-owned replacement"
	conflict.Source = "console"
	if err := st.UpsertAgentInstance(ctx, &conflict); err != nil {
		t.Fatal(err)
	}
	report, err := Import(ctx, st, sampleConfig(), false)
	if err == nil || len(report.Conflicts) != 1 {
		t.Fatalf("conflict import = %+v, err=%v", report, err)
	}
	channels, listErr := st.ListChannels(ctx)
	if listErr != nil || len(channels) != 1 || channels[0].AgentID != built.Agents[0].ID {
		t.Fatalf("channels changed after conflict: %+v err=%v", channels, listErr)
	}
}

func TestBuildRejectsDuplicateProjects(t *testing.T) {
	cfg := sampleConfig()
	cfg.Projects = append(cfg.Projects, config.ProjectConfig{Name: "Demo", Agent: "claude"})
	if _, err := Build(cfg, time.Now()); err == nil {
		t.Fatal("expected duplicate project error")
	}
}

func TestBuildMapsShellHook(t *testing.T) {
	cfg := &config.Config{Hooks: []config.HookConfig{{Event: "message.sent", Type: core.ActionShell, Command: "echo done"}}}
	resources, err := Build(cfg, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(resources.Triggers) != 1 || resources.Triggers[0].ActionTarget != "echo done" {
		t.Fatalf("triggers = %+v", resources.Triggers)
	}
}
