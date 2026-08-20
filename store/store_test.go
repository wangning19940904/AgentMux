package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func TestReadsRemainAvailableDuringWriteTransaction(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "concurrent-read.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.UpsertAgentInstance(ctx, &core.AgentInstance{
		ID: "agent-readable", Name: "Readable", RuntimeID: "codex", Enabled: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if st.writer.Stats().MaxOpenConnections != 1 {
		t.Fatalf("writer connections = %d, want 1", st.writer.Stats().MaxOpenConnections)
	}
	if st.db.Stats().MaxOpenConnections <= 1 {
		t.Fatalf("reader connections = %d, want a concurrent pool", st.db.Stats().MaxOpenConnections)
	}
	tx, err := st.writer.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES('held-write','1')`); err != nil {
		t.Fatal(err)
	}

	readCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	items, err := st.ListAgentInstances(readCtx)
	if err != nil {
		t.Fatalf("read blocked behind write transaction: %v", err)
	}
	if len(items) != 1 || items[0].ID != "agent-readable" {
		t.Fatalf("agents = %+v", items)
	}
}

func TestConversationCreationSurvivesConcurrentStoreWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared-writers.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	stores := []*Store{first, second}
	const callers = 24
	ids := make(chan string, callers)
	errs := make(chan error, callers)
	var group sync.WaitGroup
	for index := 0; index < callers; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			conversation, _, err := stores[index%len(stores)].GetOrCreateConversation(context.Background(), core.Conversation{
				Scope: "channel:shared", ChatID: "chat-shared", ChatType: "group", AgentID: "agent-shared",
			})
			if err != nil {
				errs <- err
				return
			}
			ids <- conversation.ID
		}(index)
	}
	group.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent conversation creation: %v", err)
	}
	var expected string
	for id := range ids {
		if expected == "" {
			expected = id
		}
		if id != expected {
			t.Fatalf("conversation IDs diverged: got %q want %q", id, expected)
		}
	}
}

func TestProviderCRUDAndActive(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	p := &core.Provider{ID: "p1", Name: "P1", Category: "official", BaseURL: "http://x",
		SettingsConfig: map[string]any{"claude_config_dir": "/tmp/claude"},
		Meta:           core.ProviderMeta{APIFormat: "anthropic"},
		CreatedAt:      time.Now(), UpdatedAt: time.Now()}
	if err := st.UpsertProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetProvider(ctx, "p1")
	if err != nil || got == nil || got.Name != "P1" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if got.Category != "official" || got.Meta.APIFormat != "anthropic" || got.SettingsConfig["claude_config_dir"] != "/tmp/claude" {
		t.Fatalf("provider metadata = %+v", got)
	}
	if err := st.SetActiveProvider(ctx, "claudecode", "p1"); err != nil {
		t.Fatal(err)
	}
	id, ok, err := st.ActiveProviderID(ctx, "claudecode")
	if err != nil || !ok || id != "p1" {
		t.Fatalf("active = %q,%v,%v", id, ok, err)
	}
	routes, err := st.ActiveProviderRoutes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].Tool != "claudecode" || routes[0].ProviderID != "p1" || routes[0].APIFormat != "anthropic" {
		t.Fatalf("routes = %+v", routes)
	}
	if routes[0].Meta.ClaudeAuthScheme != "" {
		t.Fatalf("unexpected route meta = %+v", routes[0].Meta)
	}
	if err := st.SetActiveProviderRoute(ctx, core.ProviderRoute{
		Tool:       "claudecode",
		ProviderID: "p1",
		Meta: core.ProviderMeta{
			ClaudeAuthScheme:  "api_key",
			ClaudeSonnetModel: "relay-sonnet",
		},
	}); err != nil {
		t.Fatal(err)
	}
	route, ok, err := st.ActiveProviderRoute(ctx, "claudecode")
	if err != nil || !ok {
		t.Fatalf("active route = %+v,%v,%v", route, ok, err)
	}
	if route.Meta.ClaudeAuthScheme != "api_key" || route.Meta.ClaudeSonnetModel != "relay-sonnet" {
		t.Fatalf("route meta = %+v", route.Meta)
	}
	p2 := &core.Provider{ID: "p2", Name: "P2", BaseURL: "http://y",
		CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := st.UpsertProvider(ctx, p2); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActiveProvider(ctx, "claudecode", "p2"); err != nil {
		t.Fatal(err)
	}
	old, err := st.GetProvider(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if old.Enabled {
		t.Fatalf("previous provider remained enabled: %+v", old)
	}
	if err := st.ClearActiveProvider(ctx, "claudecode"); err != nil {
		t.Fatal(err)
	}
	_, ok, err = st.ActiveProviderID(ctx, "claudecode")
	if err != nil || ok {
		t.Fatalf("cleared active provider still routed: ok=%v err=%v", ok, err)
	}
	cleared, err := st.GetProvider(ctx, "p2")
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Enabled {
		t.Fatalf("cleared provider remained enabled: %+v", cleared)
	}
	if err := st.SetActiveProvider(ctx, "claudecode", "p2"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteProvider(ctx, "p2"); err != nil {
		t.Fatal(err)
	}
	_, ok, err = st.ActiveProviderID(ctx, "claudecode")
	if err != nil || ok {
		t.Fatalf("deleted active provider still routed: ok=%v err=%v", ok, err)
	}
}

func TestUsageUpsertDedupAndQuery(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rec := core.UsageRecord{Source: "claude", SessionID: "s", Timestamp: ts, InputTokens: 5}
	// Insert twice; primary key should dedup.
	if err := st.UpsertUsage(ctx, []core.UsageRecord{rec, rec}); err != nil {
		t.Fatal(err)
	}
	got, err := st.QueryUsage(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("rows = %d, want 1 (dedup)", len(got))
	}
	// since filter excludes earlier rows.
	future, _ := st.QueryUsage(ctx, ts.Add(time.Hour))
	if len(future) != 0 {
		t.Fatalf("since filter rows = %d, want 0", len(future))
	}
}

func TestProxyTraceInsertAndQuery(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	base := time.Date(2026, 7, 6, 8, 0, 0, 0, time.UTC)
	traces := []core.ProxyTrace{
		{ID: "t1", Timestamp: base, Tool: "claudecode", ProviderID: "relay", ClientProtocol: "anthropic", UpstreamProtocol: "anthropic", ClientModel: "claude-sonnet-4-8", UpstreamModel: "relay-sonnet", Success: true, SessionID: "s1"},
		{ID: "t2", Timestamp: base.Add(time.Minute), Tool: "codex", ProviderID: "openai", ClientProtocol: "openai_chat", UpstreamProtocol: "openai_chat", ClientModel: "gpt-5", UpstreamModel: "gpt-5", Success: true, SessionID: "s2"},
		{ID: "t3", Timestamp: base.Add(2 * time.Minute), Tool: "claudecode", ProviderID: "relay", ClientProtocol: "anthropic", UpstreamProtocol: "gemini", ClientModel: "claude-haiku-4-5", UpstreamModel: "gemini-2.5-pro", StatusCode: 200, Success: true, SessionID: "s1"},
	}
	for _, trace := range traces {
		if err := st.InsertProxyTrace(ctx, trace); err != nil {
			t.Fatal(err)
		}
	}

	got, err := st.QueryProxyTraces(ctx, "claudecode", "s1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "t3" || got[0].UpstreamModel != "gemini-2.5-pro" {
		t.Fatalf("filtered traces = %+v", got)
	}
	recent, err := st.QueryProxyTraces(ctx, "", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 || recent[0].ID != "t3" || recent[1].ID != "t2" {
		t.Fatalf("recent traces = %+v", recent)
	}
}

func TestAgentInstanceCRUD(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	now := time.Now()
	agent := &core.AgentInstance{
		ID:                     "agent-test",
		Name:                   "Research Codex",
		RuntimeID:              "codex",
		WorkDir:                "/tmp/work",
		WorkspaceMode:          "worktree",
		WorktreeBaseRef:        "main",
		SessionBackend:         "tmux",
		ProviderTool:           "codex",
		ProviderID:             "openai",
		DefaultModel:           "gpt-5",
		DefaultReasoningEffort: "high",
		DefaultServiceTier:     "priority",
		DefaultApprovalMode:    core.ApprovalModeAutoEdit,
		MemoryScope:            "agent:agent-test",
		Env:                    map[string]string{"CODEX_HOME": "/tmp/codex"},
		ChannelBindings: []core.AgentChannelBinding{{
			ID:     "channel-1",
			Type:   "telegram",
			Name:   "ops",
			ChatID: "123",
			Status: "configured",
		}},
		Schedules: []core.AgentSchedule{{
			ID:      "schedule-1",
			Name:    "morning",
			Cron:    "0 9 * * *",
			Prompt:  "summarize",
			Enabled: true,
		}},
		MCPServers: []string{"filesystem"},
		Skills:     []string{"code-review"},
		Enabled:    true,
		Source:     "console",
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := st.UpsertAgentInstance(ctx, agent); err != nil {
		t.Fatal(err)
	}
	items, err := st.ListAgentInstances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("agents = %d, want 1", len(items))
	}
	got, err := st.GetAgentInstance(ctx, "agent-test")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Name != "Research Codex" || got.RuntimeID != "codex" || got.WorkspaceMode != "worktree" || got.WorktreeBaseRef != "main" || got.SessionBackend != "tmux" || got.DefaultModel != "gpt-5" || got.DefaultReasoningEffort != "high" || got.DefaultServiceTier != "priority" || got.DefaultApprovalMode != core.ApprovalModeAutoEdit {
		t.Fatalf("agent = %+v", got)
	}
	if err := st.UpdateAgentRuntimeSettings(ctx, "agent-test", core.RuntimeSettings{Model: "gpt-5-mini", ReasoningEffort: "xhigh", ServiceTier: "default", ApprovalMode: core.ApprovalModeYolo}); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetAgentInstance(ctx, "agent-test")
	if err != nil || got == nil || got.DefaultModel != "gpt-5-mini" || got.DefaultReasoningEffort != "xhigh" || got.DefaultServiceTier != "default" || got.DefaultApprovalMode != core.ApprovalModeYolo {
		t.Fatalf("updated runtime settings = %+v, err=%v", got, err)
	}
	if len(got.ChannelBindings) != 1 || got.ChannelBindings[0].Type != "telegram" {
		t.Fatalf("channels = %+v", got.ChannelBindings)
	}
	if len(got.Schedules) != 1 || got.Schedules[0].Cron != "0 9 * * *" {
		t.Fatalf("schedules = %+v", got.Schedules)
	}
	if len(got.MCPServers) != 1 || got.MCPServers[0] != "filesystem" || len(got.Skills) != 1 {
		t.Fatalf("tools = mcp:%v skills:%v", got.MCPServers, got.Skills)
	}
	if err := st.DeleteAgentInstance(ctx, "agent-test"); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetAgentInstance(ctx, "agent-test")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("deleted agent still present: %+v", got)
	}
}

func TestConversationLifecycle(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	seed := core.Conversation{Scope: "channel:c1", ChatID: "chatA", ChatType: "group", AgentID: "agent-x", WorkDir: "/tmp/a"}
	conv, created, err := st.GetOrCreateConversation(ctx, seed)
	if err != nil || !created || conv == nil {
		t.Fatalf("create = %+v, created=%v, err=%v", conv, created, err)
	}
	firstID := conv.ID

	// Same (scope, chatID) reuses the active conversation.
	again, created, err := st.GetOrCreateConversation(ctx, seed)
	if err != nil || created || again.ID != firstID {
		t.Fatalf("reuse = %+v, created=%v, err=%v", again, created, err)
	}

	// A different chat in the same scope is a distinct conversation.
	other, created, err := st.GetOrCreateConversation(ctx, core.Conversation{Scope: "channel:c1", ChatID: "chatB", AgentID: "agent-x"})
	if err != nil || !created || other.ID == firstID {
		t.Fatalf("distinct chat = %+v, created=%v, err=%v", other, created, err)
	}

	if err := st.UpdateConversationSession(ctx, firstID, "native-123", "/tmp/a/cwd"); err != nil {
		t.Fatal(err)
	}
	if err := st.TouchConversation(ctx, firstID); err != nil {
		t.Fatal(err)
	}
	reloaded, _, err := st.GetOrCreateConversation(ctx, seed)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.NativeSessionID != "native-123" || reloaded.WorkDir != "/tmp/a/cwd" || reloaded.MessageCount != 1 {
		t.Fatalf("after update/touch = %+v", reloaded)
	}

	// Ending soft-deletes: the next get-or-create starts a fresh conversation.
	if err := st.EndConversation(ctx, firstID); err != nil {
		t.Fatal(err)
	}
	fresh, created, err := st.GetOrCreateConversation(ctx, seed)
	if err != nil || !created || fresh.ID == firstID {
		t.Fatalf("after end = %+v, created=%v, err=%v", fresh, created, err)
	}

	active, err := st.ListConversations(ctx, "channel:c1", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 { // chatB + fresh chatA (ended one excluded)
		t.Fatalf("active conversations = %d, want 2 (%+v)", len(active), active)
	}
	all, err := st.ListConversations(ctx, "channel:c1", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("all conversations = %d, want 3", len(all))
	}
}

func TestConversationIdentityUsesConversationKeyWithinOneChat(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "conversation-key.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	first, created, err := st.GetOrCreateConversation(ctx, core.Conversation{
		Scope: "channel:c1", ConversationKey: "thread:one", ChatID: "same-chat",
	})
	if err != nil || !created {
		t.Fatalf("first = %+v created=%t err=%v", first, created, err)
	}
	second, created, err := st.GetOrCreateConversation(ctx, core.Conversation{
		Scope: "channel:c1", ConversationKey: "thread:two", ChatID: "same-chat",
	})
	if err != nil || !created || second.ID == first.ID {
		t.Fatalf("second = %+v created=%t err=%v", second, created, err)
	}
	reused, created, err := st.GetOrCreateConversation(ctx, core.Conversation{
		Scope: "channel:c1", ConversationKey: "thread:one", ChatID: "same-chat",
	})
	if err != nil || created || reused.ID != first.ID {
		t.Fatalf("reused = %+v created=%t err=%v", reused, created, err)
	}
}

func TestConversationMigrationBackfillsConversationKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-conversations.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE conversations (
		id TEXT PRIMARY KEY, scope TEXT NOT NULL, chat_id TEXT NOT NULL, chat_type TEXT,
		agent_id TEXT, work_dir TEXT, native_session_id TEXT, title TEXT,
		message_count INTEGER DEFAULT 0, created_at TEXT, updated_at TEXT,
		last_active_at TEXT, ended_at TEXT
	);
	CREATE UNIQUE INDEX idx_conversations_active
		ON conversations(scope, chat_id) WHERE ended_at IS NULL OR ended_at='';
	INSERT INTO conversations (id,scope,chat_id,ended_at) VALUES ('legacy-1','channel:c1','oc_legacy','');`)
	if err != nil {
		_ = legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	conversations, err := st.ListConversations(context.Background(), "channel:c1", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(conversations) != 1 || conversations[0].ConversationKey != "chat:oc_legacy" {
		t.Fatalf("migrated conversations = %+v", conversations)
	}
}
