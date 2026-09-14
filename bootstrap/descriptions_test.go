package bootstrap

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
	"github.com/wangning19940904/AgentMux/tools"
)

func TestCLIDescriptionPersistsAndFeedsPrompt(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "descriptions.db")
	st, err := store.OpenLegacySQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := tools.LookupCLI("github-cli")
	if got := cliNotes(ctx, st, []string{spec.ID}); len(got) != 1 || got[0].Note != spec.Note {
		t.Fatalf("default description = %+v", got)
	}
	description := "查询仓库与 PR。\n需要更多信息时运行 gh --help。"
	if err := tools.SetCLIDescription(ctx, st, spec.ID, description); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.OpenLegacySQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	notes := cliNotes(ctx, st, []string{spec.ID, "unknown"})
	if len(notes) != 1 || notes[0].Note != description {
		t.Fatalf("reloaded notes = %+v", notes)
	}
	prompt := core.ComposeSystemPrompt("base", nil, notes)
	if !strings.Contains(prompt, description) || strings.Contains(prompt, spec.Note) {
		t.Fatalf("prompt = %q", prompt)
	}
	if err := tools.SetCLIDescription(ctx, st, spec.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got := cliNotes(ctx, st, []string{spec.ID}); len(got) != 1 || got[0].Note != "" {
		t.Fatalf("cleared description restored default: %+v", got)
	}
}
