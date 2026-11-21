package server

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/historysearch"
)

// scopeRecordingIndex answers nothing and keeps the scope of the last query.
type scopeRecordingIndex struct {
	scopes []core.HistoryScope
}

func (i *scopeRecordingIndex) PutHistoryDocs(context.Context, []core.HistoryDoc) error { return nil }
func (i *scopeRecordingIndex) DeleteHistoryDocs(context.Context, []string) error       { return nil }
func (i *scopeRecordingIndex) QueryHistoryDocs(_ context.Context, query core.HistoryDocQuery) ([]core.HistoryDocHit, error) {
	i.scopes = append(i.scopes, query.Scope)
	return nil, nil
}
func (i *scopeRecordingIndex) StaleHistoryMemory(context.Context) ([]*core.MemoryEntry, []string, error) {
	return nil, nil, nil
}
func (i *scopeRecordingIndex) HistoryTurnBackfill(context.Context, time.Time, int) ([]core.ObservationEnvelope, error) {
	return nil, nil
}
func (i *scopeRecordingIndex) CleanupHistoryDocs(context.Context, time.Time) (int64, error) {
	return 0, nil
}

type fixedTurns map[string]core.ActiveTurnIdentity

func (t fixedTurns) ActiveTurnIdentity(channelID, conversationKey string) (core.ActiveTurnIdentity, error) {
	if turn, ok := t[channelID+"|"+conversationKey]; ok {
		return turn, nil
	}
	return core.ActiveTurnIdentity{}, errors.New("conversation has no active turn")
}

func TestHandleHistorySearchScopesToTheRunningTurn(t *testing.T) {
	turns := fixedTurns{
		"ch|p2p":   {AgentID: "a1", ChatType: "p2p", UserID: "alice", ChannelID: "ch"},
		"ch|group": {AgentID: "a1", MemoryScope: "team", ChatType: "group", UserID: "alice", ChannelID: "ch"},
	}
	cases := []struct {
		name   string
		body   string
		remote string
		member bool
		status int
		scope  core.HistoryScope
	}{
		{name: "private chat sees the person's private chats",
			body: `{"channel_id":"ch","conversation_key":"p2p","queries":["迁移"]}`, status: http.StatusOK,
			scope: core.HistoryScope{AgentID: "a1", MemoryScopes: []string{"global", "agent:a1"}, PersonID: "alice"}},
		{name: "group chat sees no one's private chats, and the body cannot name another agent",
			body: `{"channel_id":"ch","conversation_key":"group","agent_id":"a2","queries":["迁移"]}`, status: http.StatusOK,
			scope: core.HistoryScope{AgentID: "a1", MemoryScopes: []string{"global", "team"}}},
		{name: "no running turn",
			body: `{"channel_id":"ch","conversation_key":"old","queries":["迁移"]}`, status: http.StatusConflict},
		{name: "not loopback", remote: "192.0.2.7:4000",
			body: `{"channel_id":"ch","conversation_key":"p2p","queries":["迁移"]}`, status: http.StatusForbidden},
		{name: "console administrator, one agent",
			body: `{"agent_id":"a2","queries":["迁移"]}`, status: http.StatusOK,
			scope: core.HistoryScope{Admin: true, AgentID: "a2", MemoryScopes: []string{"global", "agent:a2"}}},
		{name: "console member", member: true,
			body: `{"queries":["迁移"]}`, status: http.StatusForbidden},
		{name: "no usable query",
			body: `{"channel_id":"ch","conversation_key":"p2p","queries":["  ", "the"]}`, status: http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			index := &scopeRecordingIndex{}
			server := &Server{history: historysearch.New(historysearch.Options{Index: index}), turns: turns}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/history/search", bytes.NewReader([]byte(c.body)))
			req.RemoteAddr = "127.0.0.1:4000"
			if c.remote != "" {
				req.RemoteAddr = c.remote
			}
			if c.member {
				req = req.WithContext(withPrincipal(req.Context(), &core.Principal{Account: &core.AccountIdentity{Login: "m"}}))
			}
			recorder := httptest.NewRecorder()
			server.handleHistorySearch(recorder, req)
			if recorder.Code != c.status {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, c.status, recorder.Body.String())
			}
			if c.status != http.StatusOK {
				if len(index.scopes) != 0 {
					t.Fatalf("a refused search queried the index: %+v", index.scopes)
				}
				return
			}
			if len(index.scopes) == 0 {
				t.Fatal("the index was not queried")
			}
			for _, scope := range index.scopes {
				if scope.Admin != c.scope.Admin || scope.AgentID != c.scope.AgentID || scope.PersonID != c.scope.PersonID ||
					!slices.Equal(scope.MemoryScopes, c.scope.MemoryScopes) {
					t.Fatalf("scope = %+v, want %+v", scope, c.scope)
				}
			}
		})
	}
}
