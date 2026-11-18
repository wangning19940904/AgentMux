package server

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/store"
)

// Phones. The Android and iPhone apps sign in with an account the way the
// Console does, so each phone is an account session. Its User-Agent names the
// app and the phone (AgentMux-Android/0.2.0 (Pixel 8; Android 15)), which is
// how the Devices page tells a phone from a browser.
//
// Administrators see every account's phones; a member sees their own. Ending
// a phone's session signs that phone out.

// phoneActiveWindow is how recently a phone must have used its session to
// count as active. Sessions record their use at most every five minutes.
const phoneActiveWindow = 10 * time.Minute

type phoneView struct {
	ID          string     `json:"id"`
	Platform    string     `json:"platform"`
	Name        string     `json:"name,omitempty"`
	OS          string     `json:"os,omitempty"`
	AppVersion  string     `json:"app_version,omitempty"`
	AccountID   string     `json:"account_id"`
	AccountName string     `json:"account_name,omitempty"`
	SignedInAt  time.Time  `json:"signed_in_at"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
	RemoteAddr  string     `json:"remote_addr,omitempty"`
	Active      bool       `json:"active"`
	// Current is the phone asking.
	Current bool `json:"current,omitempty"`
}

// phoneApp is what a session's User-Agent says about the phone app.
type phoneApp struct {
	Platform, Name, OS, Version string
}

var phoneAgentPattern = regexp.MustCompile(`^AgentMux-(Android|iOS)/(\S+)(?:\s+\(([^)]*)\))?`)

// parsePhoneAgent recognizes the phone apps' User-Agent. Apps from before it
// named themselves are recognized by their HTTP library's default: OkHttp on
// Android, CFNetwork without a browser on iOS.
func parsePhoneAgent(agent string) (phoneApp, bool) {
	agent = strings.TrimSpace(agent)
	if match := phoneAgentPattern.FindStringSubmatch(agent); match != nil {
		app := phoneApp{Platform: strings.ToLower(match[1]), Version: unescapeAgentPart(match[2])}
		if details := strings.Split(match[3], ";"); match[3] != "" {
			app.Name = unescapeAgentPart(details[0])
			if len(details) > 1 {
				app.OS = unescapeAgentPart(details[1])
			}
		}
		return app, true
	}
	switch {
	case strings.HasPrefix(agent, "okhttp/"):
		return phoneApp{Platform: "android"}, true
	case strings.Contains(agent, "CFNetwork/") && !strings.Contains(agent, "Mozilla/"):
		return phoneApp{Platform: "ios"}, true
	}
	return phoneApp{}, false
}

// unescapeAgentPart decodes the percent-encoding the apps use for a phone's
// name that is not plain ASCII ("王宁的手机").
func unescapeAgentPart(part string) string {
	part = strings.TrimSpace(part)
	if decoded, err := url.PathUnescape(part); err == nil {
		return strings.TrimSpace(decoded)
	}
	return part
}

func (s *Server) registerPhoneRoutes() {
	s.mux.HandleFunc("GET /api/v1/devices/phones", s.handlePhonesList)
	s.mux.HandleFunc("DELETE /api/v1/devices/phones", s.handlePhoneSignOut)
}

func (s *Server) handlePhonesList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.st == nil {
		serviceUnavailable(w, "accounts")
		return
	}
	ctx := r.Context()
	principal := requestPrincipal(r)
	scope := ""
	if principal.IsMember() {
		scope = principal.Account.ID
	}
	current := ""
	if cookie, err := r.Cookie(consoleSessionCookie); err == nil {
		current = cookie.Value
	}
	sessions, err := s.st.ListAccountSessions(ctx, scope, current)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	names := map[string]string{}
	if accounts, err := s.st.ListAccounts(ctx); err == nil {
		for _, account := range accounts {
			names[account.ID] = account.Name()
		}
	}
	phones := phoneViews(sessions, names, time.Now())
	writeJSON(w, http.StatusOK, map[string]any{"phones": phones})
}

func phoneViews(sessions []store.AccountSessionInfo, names map[string]string, now time.Time) []phoneView {
	phones := make([]phoneView, 0, len(sessions))
	for _, session := range sessions {
		app, ok := parsePhoneAgent(session.UserAgent)
		if !ok {
			continue
		}
		phones = append(phones, phoneView{
			ID: session.ID, Platform: app.Platform, Name: app.Name, OS: app.OS, AppVersion: app.Version,
			AccountID: session.AccountID, AccountName: names[session.AccountID],
			SignedInAt: session.CreatedAt, LastSeenAt: session.LastSeenAt, RemoteAddr: session.RemoteAddr,
			Active:  now.Sub(session.LastActive()) <= phoneActiveWindow,
			Current: session.Current,
		})
	}
	return phones
}

// handlePhoneSignOut ends one phone's session. The accounts' authority does
// it, so a standby forwards it to the primary hub.
func (s *Server) handlePhoneSignOut(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	op := accountOp{Op: accountOpEndSession, SessionID: id}
	if principal := requestPrincipal(r); principal.IsMember() {
		op.AccountID = principal.Account.ID
	}
	if _, err := s.runAccountOp(r.Context(), op); err != nil {
		writeAccountOpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
