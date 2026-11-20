package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/historysearch"
)

// historySearchRequest is POST /api/v1/history/search. An agent names the
// channel conversation of its running turn (amux history search does);
// the Console names an agent, or none for every agent.
type historySearchRequest struct {
	ChannelID       string                 `json:"channel_id,omitempty"`
	ConversationKey string                 `json:"conversation_key,omitempty"`
	AgentID         string                 `json:"agent_id,omitempty"`
	Queries         []string               `json:"queries"`
	Around          historysearch.TimeHint `json:"around,omitzero"`
	Limit           int                    `json:"limit,omitempty"`
}

// handleHistorySearch searches memory and past turns. The scope never comes
// from the body: for an agent it is its running turn's agent and person, and
// a conversation without a running turn is refused, as channel delivery is.
func (s *Server) handleHistorySearch(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		serviceUnavailable(w, "history search")
		return
	}
	var req historySearchRequest
	if !decodeJSONInto(w, r, &req) {
		return
	}
	scope, ok := s.historySearchScope(w, r, req)
	if !ok {
		return
	}
	result, err := s.history.Search(r.Context(), historysearch.Request{
		Queries: req.Queries, Around: req.Around, Limit: req.Limit, Scope: scope,
	})
	if errors.Is(err, historysearch.ErrNoQuery) {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) historySearchScope(w http.ResponseWriter, r *http.Request, req historySearchRequest) (core.HistoryScope, bool) {
	channelID, conversationKey := strings.TrimSpace(req.ChannelID), strings.TrimSpace(req.ConversationKey)
	if channelID != "" || conversationKey != "" {
		// An agent's call. Like channel delivery, it is local only and
		// acts for the turn running in that conversation now.
		if !isLoopbackRequest(r) {
			writeErr(w, http.StatusForbidden, "an agent's history search is only available on loopback")
			return core.HistoryScope{}, false
		}
		if s.turns == nil {
			serviceUnavailable(w, "channel turns")
			return core.HistoryScope{}, false
		}
		turn, err := s.turns.ActiveTurnIdentity(channelID, conversationKey)
		if err != nil {
			writeErr(w, http.StatusConflict, err.Error())
			return core.HistoryScope{}, false
		}
		return agentHistoryScope(turn), true
	}
	// The Console: an administrator, for one agent or all of them.
	if !s.requireAdmin(w, r) {
		return core.HistoryScope{}, false
	}
	scope := core.HistoryScope{Admin: true, AgentID: strings.TrimSpace(req.AgentID)}
	if scope.AgentID != "" {
		configured := ""
		if s.st != nil {
			agent, err := s.st.GetAgentInstance(r.Context(), scope.AgentID)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return core.HistoryScope{}, false
			}
			if agent != nil {
				configured = agent.MemoryScope
			}
		}
		scope.MemoryScopes = []string{"global", core.AgentMemoryScope(scope.AgentID, configured)}
	}
	return scope, true
}

// agentHistoryScope is what a running turn may search: the agent's own turns
// and memory. A one-to-one chat also sees that person's earlier private
// chats with the agent; a group chat sees no one's private chats, since
// everyone in the group would read the answer.
func agentHistoryScope(turn core.ActiveTurnIdentity) core.HistoryScope {
	scope := core.HistoryScope{AgentID: turn.AgentID, MemoryScopes: []string{"global"}}
	if own := core.AgentMemoryScope(turn.AgentID, turn.MemoryScope); own != "" && own != "global" {
		scope.MemoryScopes = append(scope.MemoryScopes, own)
	}
	if core.PrivateChatType(turn.ChatType, turn.UserID) {
		scope.PersonID = turn.UserID
	}
	return scope
}
