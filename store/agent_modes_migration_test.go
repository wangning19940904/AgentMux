package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func TestSQLiteAgentConversationModesMigration(t *testing.T) {
	st, err := OpenLegacySQLite(filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	checkAgentConversationModesMigration(t, st, st.migrateSQLite)
}

func TestPostgresAgentConversationModesMigration(t *testing.T) {
	st := openPostgresIntegrationStore(t)
	if _, err := st.writer.Exec(`DELETE FROM schema_migrations WHERE version=16`); err != nil {
		t.Fatal(err)
	}
	checkAgentConversationModesMigration(t, st, func() error { return st.migratePostgres(context.Background()) })
}

func checkAgentConversationModesMigration(t *testing.T, st *Store, migrate func() error) {
	t.Helper()
	ctx := context.Background()
	agent := core.AgentInstance{ID: "legacy", Name: "Legacy", RuntimeID: "codex", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := st.UpsertAgentInstance(ctx, &agent); err != nil {
		t.Fatal(err)
	}
	channel := core.Channel{ID: "legacy-channel", Name: "Legacy", Type: "feishu", AgentID: agent.ID,
		Config:    map[string]string{core.ChannelConfigPrivateMode: "thread", core.ChannelConfigGroupMode: "new-topic"},
		CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := st.UpsertChannel(ctx, &channel); err != nil {
		t.Fatal(err)
	}
	// Recreate the schema immediately before Agent-owned defaults existed.
	for _, column := range []string{"private_chat_mode", "group_chat_mode"} {
		if _, err := st.writer.Exec(`ALTER TABLE agent_instances DROP COLUMN ` + column); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrate(); err != nil {
		t.Fatal(err)
	}
	if err := migrate(); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	got, err := st.GetAgentInstance(ctx, agent.ID)
	if err != nil || got == nil || got.PrivateChatMode != "" || got.GroupChatMode != "" {
		t.Fatalf("legacy modes = %+v, err = %v", got, err)
	}
	storedChannel, err := st.GetChannel(ctx, channel.ID)
	if err != nil || storedChannel == nil || storedChannel.Config[core.ChannelConfigPrivateMode] != "thread" || storedChannel.Config[core.ChannelConfigGroupMode] != "new-topic" {
		t.Fatalf("legacy channel changed: %+v, err = %v", storedChannel, err)
	}
	got.PrivateChatMode, got.GroupChatMode = "group", "chat"
	if err := st.UpsertAgentInstance(ctx, got); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetAgentInstance(ctx, agent.ID)
	if err != nil || got == nil || got.PrivateChatMode != "group" || got.GroupChatMode != "chat" {
		t.Fatalf("migrated Agent cannot save modes: %+v, err = %v", got, err)
	}
}
