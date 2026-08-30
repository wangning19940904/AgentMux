package usage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func TestReadCursorAuthForHomeFallsBackToCLI(t *testing.T) {
	home := t.TempDir()
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"auth0|cli-user"}`))
	token := "e30." + payload + ".signature"
	path := cursorCLIAuthPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"accessToken": token, "refreshToken": "refresh"})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	auth, err := readCursorAuthForHome(context.Background(), home, filepath.Join(home, "missing-state.vscdb"))
	if err != nil {
		t.Fatal(err)
	}
	if auth.AccessToken != token || auth.RefreshToken != "refresh" || auth.SessionToken != "cli-user::"+token {
		t.Fatalf("auth = %+v", auth)
	}
}

func TestDiscoverCursorCLISessionsIndexesConversationAndProject(t *testing.T) {
	home := t.TempDir()
	now := time.Now().UTC().Truncate(time.Millisecond)
	writeCursorCLIMeta(t, home, "workspace", "session-a", cursorCLIMeta{
		CreatedAtMS: now.Add(-time.Minute).UnixMilli(), UpdatedAtMS: now.UnixMilli(), HasConversation: true, CWD: "/srv/project",
	})
	writeCursorCLIMeta(t, home, "workspace", "empty-session", cursorCLIMeta{
		CreatedAtMS: now.UnixMilli(), UpdatedAtMS: now.UnixMilli(), HasConversation: false,
	})
	sessions, err := discoverCursorCLISessions(context.Background(), home, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %+v", sessions)
	}
	record := sessions[cursorConversationIndexKey("session-a")]
	if record.SessionID != "session-a" || record.ConversationID != "session-a" || record.Project != "/srv/project" || !record.Timestamp.Equal(now) {
		t.Fatalf("record = %+v", record)
	}
}

func TestCursorDashboardScopedToLocalCLIConversations(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	local := map[string]core.UsageRecord{
		cursorConversationIndexKey("local-session"): {
			Source: "cursor", SessionID: "local-session", ConversationID: "local-session", Project: "/srv/project", Model: "cursor",
		},
	}
	matched, wasMatched, ok := cursorRecordFromDashboardEventScoped(cursorDashboardEvent{
		ConversationID: "local-session", Timestamp: json.RawMessage(strconv.FormatInt(now.UnixMilli(), 10)),
		Model: "composer", InputTokens: 10, OutputTokens: 2,
	}, local, true)
	if !ok || !wasMatched || matched.SessionID != "local-session" || matched.Project != "/srv/project" || !strings.HasPrefix(matched.RequestID, "cursor-") {
		t.Fatalf("matched = %+v wasMatched=%v ok=%v", matched, wasMatched, ok)
	}
	if _, wasMatched, ok := cursorRecordFromDashboardEventScoped(cursorDashboardEvent{
		ConversationID: "other-session", Timestamp: json.RawMessage(strconv.FormatInt(now.UnixMilli(), 10)), InputTokens: 5,
	}, local, true); ok || wasMatched {
		t.Fatalf("unmatched local-scope event was accepted: matched=%v ok=%v", wasMatched, ok)
	}
}

func writeCursorCLIMeta(t *testing.T, home, workspace, session string, meta cursorCLIMeta) {
	t.Helper()
	path := filepath.Join(cursorCLIChatsRoot(home), workspace, session, "meta.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(meta)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
