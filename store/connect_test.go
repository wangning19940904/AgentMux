package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func TestChannelCRUD(t *testing.T) {
	st, err := OpenLegacySQLite(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	now := time.Now()
	ch := &core.Channel{
		ID:      "channel-abc",
		Name:    "ops feishu",
		Type:    "feishu",
		AgentID: "agent-1",
		Config:  map[string]string{"app_id": "cli_x", "app_secret": "s3cret"},
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := st.UpsertChannel(ctx, ch); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetChannel(ctx, "channel-abc")
	if err != nil || got == nil {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if got.Type != "feishu" || got.Config["app_secret"] != "s3cret" || !got.Enabled {
		t.Fatalf("channel = %+v", got)
	}
	ch.Enabled = false
	ch.Name = "renamed"
	if err := st.UpsertChannel(ctx, ch); err != nil {
		t.Fatal(err)
	}
	items, err := st.ListChannels(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("list = %+v, %v", items, err)
	}
	if items[0].Name != "renamed" || items[0].Enabled {
		t.Fatalf("updated channel = %+v", items[0])
	}
	if err := st.DeleteChannel(ctx, "channel-abc"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetChannel(ctx, "channel-abc"); got != nil {
		t.Fatalf("deleted channel still present: %+v", got)
	}
}

func TestTriggerCRUDAndRunUpdate(t *testing.T) {
	st, err := OpenLegacySQLite(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	now := time.Now()
	tr := &core.Trigger{
		ID: "trigger-1", Name: "daily", Kind: core.TriggerCron,
		AgentID: "agent-1", ChannelID: "channel-1", ChatID: "oc_1",
		CronExpr: "0 9 * * *", Prompt: "summarize inbox",
		SessionMode: core.SessionModeReuse, Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := st.UpsertTrigger(ctx, tr); err != nil {
		t.Fatal(err)
	}
	wh := &core.Trigger{
		ID: "trigger-2", Name: "ci hook", Kind: core.TriggerWebhook,
		AgentID: "agent-1", Token: "tok", Prompt: "review the payload",
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := st.UpsertTrigger(ctx, wh); err != nil {
		t.Fatal(err)
	}
	items, err := st.ListTriggers(ctx)
	if err != nil || len(items) != 2 {
		t.Fatalf("list = %d items, %v", len(items), err)
	}

	runAt := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
	if err := st.UpdateTriggerRun(ctx, "trigger-1", runAt, "ok", ""); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetTrigger(ctx, "trigger-1")
	if err != nil || got == nil {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if !got.LastRun.Equal(runAt) || got.LastStatus != "ok" || got.LastError != "" {
		t.Fatalf("run bookkeeping = %+v", got)
	}

	// Definition updates must not clobber run bookkeeping.
	got.Prompt = "updated"
	if err := st.UpsertTrigger(ctx, got); err != nil {
		t.Fatal(err)
	}
	again, _ := st.GetTrigger(ctx, "trigger-1")
	if again.Prompt != "updated" || !again.LastRun.Equal(runAt) || again.LastStatus != "ok" {
		t.Fatalf("after upsert = %+v", again)
	}

	if err := st.DeleteTrigger(ctx, "trigger-1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetTrigger(ctx, "trigger-1"); got != nil {
		t.Fatalf("deleted trigger still present: %+v", got)
	}
}

func TestMigrateAgentBindings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.db")
	st, err := OpenLegacySQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	now := time.Now()
	agent := &core.AgentInstance{
		ID: "agent-legacy", Name: "Legacy", RuntimeID: "codex",
		ChannelBindings: []core.AgentChannelBinding{{
			ID: "channel-1", Type: "telegram", Name: "ops", ChatID: "123",
			Status: "configured", Config: map[string]string{"token": "tg-token"},
		}},
		Schedules: []core.AgentSchedule{{
			ID: "schedule-1", Name: "morning", Cron: "0 9 * * *",
			Prompt: "summarize", Enabled: true,
		}},
		Enabled: true, Source: "console", CreatedAt: now, UpdatedAt: now,
	}
	if err := st.UpsertAgentInstance(ctx, agent); err != nil {
		t.Fatal(err)
	}
	// Reset the marker so the already-run migration executes again over the
	// legacy rows (Open ran it on the empty database).
	if _, err := st.db.ExecContext(ctx, `DELETE FROM settings WHERE key=?`, bindingsMigratedKey); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st, err = OpenLegacySQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	channels, err := st.ListChannels(ctx)
	if err != nil || len(channels) != 1 {
		t.Fatalf("channels = %+v, %v", channels, err)
	}
	ch := channels[0]
	if ch.Type != "telegram" || ch.AgentID != "agent-legacy" || ch.Config["token"] != "tg-token" {
		t.Fatalf("migrated channel = %+v", ch)
	}
	if ch.Enabled {
		t.Fatalf("migrated channel must start disabled: %+v", ch)
	}
	triggers, err := st.ListTriggers(ctx)
	if err != nil || len(triggers) != 1 {
		t.Fatalf("triggers = %+v, %v", triggers, err)
	}
	tr := triggers[0]
	if tr.Kind != core.TriggerCron || tr.CronExpr != "0 9 * * *" || !tr.Enabled {
		t.Fatalf("migrated trigger = %+v", tr)
	}
	if tr.ChannelID != ch.ID || tr.ChatID != "123" {
		t.Fatalf("trigger channel anchor = %+v", tr)
	}

	// Re-open again: marker must prevent duplicates.
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = OpenLegacySQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	channels, _ = st.ListChannels(ctx)
	triggers, _ = st.ListTriggers(ctx)
	if len(channels) != 1 || len(triggers) != 1 {
		t.Fatalf("idempotency broken: %d channels, %d triggers", len(channels), len(triggers))
	}
}
