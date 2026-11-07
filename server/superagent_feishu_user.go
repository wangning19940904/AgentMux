package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/superagent"
)

var errFeishuUserUnavailable = errors.New("this Feishu lookup is unavailable")

// feishuUserAuthSession is an authorization the Console started and is
// polling. The device code stays here; the Console only holds the id.
type feishuUserAuthSession struct {
	channelID  string
	deviceCode string
	expiresAt  time.Time
}

// registerFeishuUserRoutes serves what the Console does as the person who
// authorized a channel's app: who that is, their groups, adding the bot to
// one, and the people they know. All go through handleFeishuDirectory, so
// the channel is checked first.
func (s *Server) registerFeishuUserRoutes() {
	user := func(lookup func(*http.Request, string, superagent.FeishuUserDirectory) (any, error)) http.HandlerFunc {
		return s.handleFeishuDirectory(func(r *http.Request, channelID string) (any, error) {
			if s.feishuUser == nil {
				return nil, errFeishuUserUnavailable
			}
			return lookup(r, channelID, s.feishuUser)
		})
	}
	s.mux.HandleFunc("GET /api/v1/super-agents/feishu/user-auth", user(func(r *http.Request, channelID string, directory superagent.FeishuUserDirectory) (any, error) {
		auth, err := directory.UserAuth(r.Context(), channelID)
		return map[string]any{"user": auth}, err
	}))
	s.mux.HandleFunc("DELETE /api/v1/super-agents/feishu/user-auth", user(func(r *http.Request, channelID string, directory superagent.FeishuUserDirectory) (any, error) {
		return map[string]bool{"ok": true}, directory.ForgetUserAuth(r.Context(), channelID)
	}))
	s.mux.HandleFunc("POST /api/v1/super-agents/feishu/user-auth/start", user(func(r *http.Request, channelID string, directory superagent.FeishuUserDirectory) (any, error) {
		start, err := directory.StartUserAuth(r.Context(), channelID)
		if err != nil {
			return nil, err
		}
		id := "feishu-user-auth-" + randHex(12)
		s.feishuUserAuthMu.Lock()
		now := time.Now()
		for key, session := range s.feishuUserAuths {
			if now.After(session.expiresAt) {
				delete(s.feishuUserAuths, key)
			}
		}
		s.feishuUserAuths[id] = feishuUserAuthSession{channelID: channelID, deviceCode: start.DeviceCode, expiresAt: now.Add(time.Duration(start.ExpiresIn) * time.Second)}
		s.feishuUserAuthMu.Unlock()
		return map[string]any{"session_id": id, "verification_url": start.VerificationURL, "user_code": start.UserCode, "expires_in": start.ExpiresIn, "interval": start.Interval}, nil
	}))
	s.mux.HandleFunc("POST /api/v1/super-agents/feishu/user-auth/poll", user(func(r *http.Request, channelID string, directory superagent.FeishuUserDirectory) (any, error) {
		var body struct {
			SessionID string `json:"session_id"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
			return nil, errBadDirectoryRequest
		}
		id := strings.TrimSpace(body.SessionID)
		s.feishuUserAuthMu.Lock()
		session, ok := s.feishuUserAuths[id]
		s.feishuUserAuthMu.Unlock()
		if !ok || session.channelID != channelID || time.Now().After(session.expiresAt) {
			return superagent.FeishuUserAuthPoll{Status: superagent.FeishuAuthExpired}, nil
		}
		poll, err := directory.PollUserAuth(r.Context(), channelID, session.deviceCode)
		if err == nil && poll.Status != superagent.FeishuAuthPending && poll.Status != superagent.FeishuAuthSlowDown {
			s.feishuUserAuthMu.Lock()
			delete(s.feishuUserAuths, id)
			s.feishuUserAuthMu.Unlock()
		}
		return poll, err
	}))
	s.mux.HandleFunc("GET /api/v1/super-agents/feishu/my-chats", user(func(r *http.Request, channelID string, directory superagent.FeishuUserDirectory) (any, error) {
		return directory.UserChats(r.Context(), channelID)
	}))
	// POST keeps the group names being searched for out of URLs and logs.
	s.mux.HandleFunc("POST /api/v1/super-agents/feishu/my-chats/find", user(func(r *http.Request, channelID string, directory superagent.FeishuUserDirectory) (any, error) {
		var body struct {
			Query   string   `json:"query"`
			ChatIDs []string `json:"chat_ids"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			return nil, errBadDirectoryRequest
		}
		return directory.FindUserChats(r.Context(), channelID, body.Query, body.ChatIDs)
	}))
	// Which scopes the bot's app still needs, for the app and for the people
	// who authorize it.
	s.mux.HandleFunc("GET /api/v1/super-agents/feishu/scopes", s.handleFeishuDirectory(func(r *http.Request, channelID string) (any, error) {
		checker, ok := s.feishuDirectory.(superagent.FeishuScopeChecker)
		if !ok {
			return nil, errFeishuUserUnavailable
		}
		return checker.ScopeStatus(r.Context(), channelID, superAgentFeishuScopes(r.URL.Query().Get("documents") == "true"), superagent.FeishuUserScopes)
	}))
	s.mux.HandleFunc("POST /api/v1/super-agents/feishu/my-chats/add-bot", user(func(r *http.Request, channelID string, directory superagent.FeishuUserDirectory) (any, error) {
		var body struct {
			ChatID string `json:"chat_id"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil || strings.TrimSpace(body.ChatID) == "" {
			return nil, errBadDirectoryRequest
		}
		return directory.AddBotToChat(r.Context(), channelID, body.ChatID)
	}))
	// POST keeps names and emails being searched for out of URLs and logs.
	s.mux.HandleFunc("POST /api/v1/super-agents/feishu/contacts", user(func(r *http.Request, channelID string, directory superagent.FeishuUserDirectory) (any, error) {
		var body struct {
			Query   string   `json:"query"`
			OpenIDs []string `json:"open_ids"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			return nil, errBadDirectoryRequest
		}
		return directory.UserContacts(r.Context(), channelID, body.Query, body.OpenIDs)
	}))
}

// superAgentFeishuScopes is what a Super Agent trigger's bot needs from its
// app: what the second scan enables, less the meeting scopes only AgentMux's
// meeting features use. A document trigger also needs the document scopes.
func superAgentFeishuScopes(documents bool) []string {
	var scopes []string
	for _, scope := range agentMuxFeishuScopes {
		if !strings.HasPrefix(scope, "vc:meeting") {
			scopes = append(scopes, scope)
		}
	}
	if documents {
		scopes = append(scopes, agentMuxFeishuDocumentScopes...)
	}
	return scopes
}
