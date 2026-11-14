package server

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/superagent"
	toolpkg "github.com/wangning19940904/AgentMux/tools"
)

const maxGitHubWebhookBytes = 25 << 20 // GitHub's documented payload cap.

// SetSuperAgents attaches the Super Agent runtime and the Feishu directory its
// Source form reads groups and people from.
func (s *Server) SetSuperAgents(service *superagent.Service, directory superagent.FeishuDirectory) {
	s.superAgents = service
	s.feishuDirectory = directory
	s.feishuUser, _ = directory.(superagent.FeishuUserDirectory)
	if s.mail != nil && service != nil {
		s.mail.SetAgents(service)
		service.SetMailbox(s.mail)
	}
}

// RemoteHosts exposes the SSH host manager to the Super Agent worker fleet. It
// returns an untyped nil when remote control is unavailable.
func (s *Server) RemoteHosts() superagent.HostDialer {
	if s.remote == nil {
		return nil
	}
	return s.remote
}

// AgentRuntimeAvailable reports whether an Agent can run on this framework here.
func AgentRuntimeAvailable(id string) bool {
	return knownAgentRuntime(id) && agentRuntimeAvailable(id)
}

func (s *Server) registerSuperAgentRoutes() {
	s.mux.HandleFunc("GET /api/v1/super-agents", s.handleSuperAgentsList)
	s.mux.HandleFunc("POST /api/v1/super-agents", s.handleSuperAgentUpsert)
	s.mux.HandleFunc("DELETE /api/v1/super-agents", s.handleSuperAgentDelete)
	s.mux.HandleFunc("GET /api/v1/super-agents/prompts", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, superagent.PromptParts())
	})
	s.mux.HandleFunc("GET /api/v1/super-agents/routers", s.handleSuperAgentRoutersList)
	s.mux.HandleFunc("POST /api/v1/super-agents/routers", s.handleSuperAgentRouterUpsert)
	s.mux.HandleFunc("DELETE /api/v1/super-agents/routers", s.handleSuperAgentRouterDelete)
	s.mux.HandleFunc("GET /api/v1/super-agents/activity", s.handleSuperAgentActivity)
	s.mux.HandleFunc("GET /api/v1/super-agents/tickets", s.handleSuperAgentTickets)
	// Workers this machine keeps for agents that live on another machine.
	s.mux.HandleFunc("POST /api/v1/super-agents/workers/launch", s.handleHostedWorkerLaunch)
	s.mux.HandleFunc("POST /api/v1/super-agents/workers/release", s.handleHostedWorkerRelease)
	s.mux.HandleFunc("GET /api/v1/super-agents/workers/tools", func(w http.ResponseWriter, _ *http.Request) {
		if !s.requireSuperAgents(w) {
			return
		}
		writeJSON(w, http.StatusOK, s.superAgents.HostTools(availableAgentRuntimes()))
	})
	s.mux.HandleFunc("POST /api/v1/super-agents/setup", s.handleSuperAgentSetup)
	s.mux.HandleFunc("POST /api/v1/super-agents/trigger", s.handleSuperAgentTrigger)
	s.mux.HandleFunc("POST /api/v1/super-agents/tickets/stop", s.handleSuperAgentTicketAction(func(r *http.Request, id string) error {
		return s.superAgents.StopTicket(r.Context(), id)
	}))
	s.mux.HandleFunc("POST /api/v1/super-agents/tickets/retry", s.handleSuperAgentTicketAction(func(r *http.Request, id string) error {
		return s.superAgents.RetryTicket(r.Context(), id)
	}))
	s.mux.HandleFunc("POST /api/v1/super-agents/timers/cancel", s.handleSuperAgentTicketAction(func(r *http.Request, id string) error {
		return s.superAgents.CancelTimer(r.Context(), id)
	}))
	s.mux.HandleFunc("GET /api/v1/super-agents/feishu/chats", s.handleFeishuDirectory(func(r *http.Request, channelID string) (any, error) {
		return s.feishuDirectory.Chats(r.Context(), channelID)
	}))
	s.mux.HandleFunc("GET /api/v1/super-agents/feishu/members", s.handleFeishuDirectory(func(r *http.Request, channelID string) (any, error) {
		return s.feishuDirectory.Members(r.Context(), channelID, splitQueryList(r.URL.Query().Get("chat_ids")))
	}))
	// POST keeps the email addresses being looked up out of URLs and access logs.
	s.mux.HandleFunc("POST /api/v1/super-agents/feishu/users", s.handleFeishuDirectory(func(r *http.Request, channelID string) (any, error) {
		var body struct {
			OpenIDs []string `json:"open_ids"`
			Emails  []string `json:"emails"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			return nil, errBadDirectoryRequest
		}
		return s.feishuDirectory.Users(r.Context(), channelID, body.OpenIDs, body.Emails)
	}))
	s.registerFeishuUserRoutes()
	s.mux.HandleFunc("GET /api/v1/super-agents/github/setup", s.handleSuperAgentGitHubSetup)
	s.mux.HandleFunc("POST /api/v1/super-agents/github/account", s.handleSuperAgentGitHubAccount)
	s.mux.HandleFunc("GET /api/v1/super-agents/github/operator", s.handleSuperAgentGitHubOperator)
	s.mux.HandleFunc("POST /api/v1/super-agents/github/login", s.handleSuperAgentGitHubLogin)
	s.mux.HandleFunc("POST /api/v1/super-agents/github/login/poll", s.handleSuperAgentGitHubLoginPoll)
	s.mux.HandleFunc("POST /api/v1/super-agents/github/login/cancel", s.handleSuperAgentGitHubLoginCancel)
	s.mux.HandleFunc("POST /api/v1/super-agents/github/invite", s.handleSuperAgentGitHubInvite)
	// Outside /api: GitHub cannot present a bridge token. Each delivery proves
	// itself with the HMAC signature of its Source's webhook secret instead.
	s.mux.HandleFunc("POST /hook/super-agent/github/{source}", s.handleSuperAgentGitHubHook)
}

func (s *Server) requireSuperAgents(w http.ResponseWriter) bool {
	if s.superAgents == nil {
		writeErr(w, http.StatusServiceUnavailable, "super agent runtime unavailable")
		return false
	}
	return true
}

func (s *Server) handleSuperAgentsList(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgents(w) {
		return
	}
	items, err := s.superAgents.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleSuperAgentUpsert(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgents(w) {
		return
	}
	agent, ok := decodeJSON[core.SuperAgent](w, r)
	if !ok {
		return
	}
	saved, err := s.superAgents.Save(r.Context(), agent)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// An agent's own brain follows its name and enabled state, which the
	// channels bound to it read when they start.
	if saved.RouterAgentID == saved.ID && s.connect != nil {
		if err := s.connect.RestartChannelsForAgent(r.Context(), saved.ID); err != nil {
			writeErr(w, http.StatusInternalServerError, "agent saved, but bound channels failed to restart: "+err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleSuperAgentDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgents(w) {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := s.superAgents.Delete(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.connect != nil {
		if err := s.connect.RestartChannelsForAgent(r.Context(), id); err != nil {
			writeErr(w, http.StatusInternalServerError, "agent deleted, but bound channels failed to restart: "+err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleSuperAgentRoutersList(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgents(w) {
		return
	}
	list, err := s.superAgents.ListRouters(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleSuperAgentRouterUpsert(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgents(w) {
		return
	}
	router, ok := decodeJSON[superagent.Router](w, r)
	if !ok {
		return
	}
	saved, err := s.superAgents.SaveRouter(r.Context(), router)
	if err != nil {
		writeSuperAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleSuperAgentRouterDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgents(w) {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := s.superAgents.DeleteRouter(r.Context(), id); err != nil {
		writeSuperAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleSuperAgentActivity(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgents(w) {
		return
	}
	activity, err := s.superAgents.Activity(r.Context(), strings.TrimSpace(r.URL.Query().Get("id")))
	if err != nil {
		writeSuperAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, activity)
}

func (s *Server) handleSuperAgentSetup(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgents(w) {
		return
	}
	body, ok := decodeJSON[superagent.SetupRequest](w, r)
	if !ok {
		return
	}
	result, err := s.superAgents.Setup(r.Context(), body)
	if err != nil {
		writeSuperAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleSuperAgentTrigger(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgents(w) {
		return
	}
	body, ok := decodeJSON[struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}](w, r)
	if !ok {
		return
	}
	trigger, err := s.superAgents.TriggerManually(r.Context(), body.ID, body.Text)
	if err != nil {
		writeSuperAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, trigger)
}

func (s *Server) handleSuperAgentTicketAction(action func(*http.Request, string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.requireSuperAgents(w) {
			return
		}
		body, ok := decodeJSON[struct {
			ID string `json:"id"`
		}](w, r)
		if !ok {
			return
		}
		if err := action(r, strings.TrimSpace(body.ID)); err != nil {
			writeSuperAgentError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func (s *Server) handleSuperAgentGitHubHook(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgents(w) {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxGitHubWebhookBytes))
	if err != nil {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	err = s.superAgents.HandleGitHubWebhook(r.Context(), r.PathValue("source"), superagent.GitHubDelivery{
		Event:      r.Header.Get("X-GitHub-Event"),
		DeliveryID: r.Header.Get("X-GitHub-Delivery"),
		Signature:  r.Header.Get("X-Hub-Signature-256"),
		Body:       body,
	})
	switch {
	case errors.Is(err, superagent.ErrNotFound):
		http.Error(w, "webhook source not found", http.StatusNotFound)
	case errors.Is(err, superagent.ErrUnauthorized):
		http.Error(w, "invalid signature", http.StatusUnauthorized)
	case err != nil:
		if s.log != nil {
			s.log.Error("super agent webhook failed", "err", err)
		}
		http.Error(w, "webhook processing failed", http.StatusInternalServerError)
	default:
		writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
	}
}

// handleSuperAgentGitHubSetup tells the setup guide where GitHub deliveries
// reach this daemon, so it can write a forwarding command for a local install,
// and whether the API asks for credentials, which decides whether exposing the
// whole port to the internet would also expose the Console.
func (s *Server) handleSuperAgentGitHubSetup(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgents(w) {
		return
	}
	addr, protected := "", false
	if s.cfg != nil {
		addr, protected = s.cfg.Server.Addr, s.cfg.Bridge.Enabled
	}
	writeJSON(w, http.StatusOK, map[string]any{"local_url": loopbackURL(addr), "api_protected": protected})
}

// loopbackURL is how a process on this machine reaches a listen address.
func loopbackURL(addr string) string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil || port == "" {
		return "http://127.0.0.1:8765"
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// handleSuperAgentGitHubAccount says whose a token is and which repositories it
// reaches: one the form holds, or the saved one of a Source. The token travels
// in the body, never in a URL.
func (s *Server) handleSuperAgentGitHubAccount(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgents(w) {
		return
	}
	var input superagent.GitHubAccountRequest
	if !decodeJSONInto(w, r, &input) {
		return
	}
	client := &http.Client{Timeout: 15 * time.Second}
	account, err := s.superAgents.CheckGitHubAccount(r.Context(), client, input)
	switch {
	case errors.Is(err, superagent.ErrNotFound):
		writeErr(w, http.StatusNotFound, "trigger not found; paste the token again")
	case errors.Is(err, superagent.ErrGitHubTokenRejected), errors.Is(err, superagent.ErrGitHubTokenMissing):
		writeErr(w, http.StatusBadRequest, err.Error())
	case err != nil:
		writeErr(w, http.StatusBadGateway, err.Error())
	default:
		writeJSON(w, http.StatusOK, account)
	}
}

// The routes below run this computer's GitHub CLI, signed in as the person at
// the keyboard, so they answer only the local Console.
func (s *Server) requireLocalGitHubCLI(w http.ResponseWriter, r *http.Request) bool {
	if !s.requireSuperAgents(w) {
		return false
	}
	if !isLoopbackRequest(r) {
		writeErr(w, http.StatusForbidden, "GitHub CLI setup is available only from the local Console")
		return false
	}
	return true
}

func githubHostOrError(w http.ResponseWriter, apiBase string) (string, bool) {
	host, err := superagent.GitHubHost(apiBase)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return "", false
	}
	return host, true
}

// handleSuperAgentGitHubOperator says who is signed in to gh here and which
// repositories they administer, so the guide can invite the agent and suggest
// the operator as its first allowed sender.
func (s *Server) handleSuperAgentGitHubOperator(w http.ResponseWriter, r *http.Request) {
	if !s.requireLocalGitHubCLI(w, r) {
		return
	}
	host, ok := githubHostOrError(w, r.URL.Query().Get("api_base"))
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, toolpkg.LookupGitHubOperator(r.Context(), host))
}

// handleSuperAgentGitHubLogin starts signing the agent's account in with a
// one-time code, in a gh profile of its own.
func (s *Server) handleSuperAgentGitHubLogin(w http.ResponseWriter, r *http.Request) {
	if !s.requireLocalGitHubCLI(w, r) {
		return
	}
	var input struct {
		APIBase string `json:"api_base"`
	}
	if !decodeJSONInto(w, r, &input) {
		return
	}
	host, ok := githubHostOrError(w, input.APIBase)
	if !ok {
		return
	}
	login, err := toolpkg.StartGitHubAgentLogin(host)
	switch {
	case errors.Is(err, toolpkg.ErrGitHubCLIMissing):
		writeErr(w, http.StatusNotFound, err.Error())
		return
	case err != nil:
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if login.LoginURL == "" {
		login.LoginURL = "https://" + host + "/login/device"
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, login)
}

// handleSuperAgentGitHubLoginPoll reports a sign-in, and hands over its token
// once it succeeds, for the form to save with the trigger.
func (s *Server) handleSuperAgentGitHubLoginPoll(w http.ResponseWriter, r *http.Request) {
	if !s.requireLocalGitHubCLI(w, r) {
		return
	}
	var input struct {
		SessionID string `json:"session_id"`
	}
	if !decodeJSONInto(w, r, &input) {
		return
	}
	login, ok := toolpkg.GetGitHubAgentLogin(input.SessionID)
	if !ok {
		writeErr(w, http.StatusNotFound, "GitHub sign-in expired; start again")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, login)
}

func (s *Server) handleSuperAgentGitHubLoginCancel(w http.ResponseWriter, r *http.Request) {
	if !s.requireLocalGitHubCLI(w, r) {
		return
	}
	var input struct {
		SessionID string `json:"session_id"`
	}
	if !decodeJSONInto(w, r, &input) {
		return
	}
	if err := toolpkg.CancelGitHubAgentLogin(input.SessionID); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleSuperAgentGitHubInvite gives the agent write access to a repository:
// the operator invites it with their own gh login, unless they already did by
// hand, and the agent accepts with its token.
func (s *Server) handleSuperAgentGitHubInvite(w http.ResponseWriter, r *http.Request) {
	if !s.requireLocalGitHubCLI(w, r) {
		return
	}
	var input struct {
		superagent.GitHubAccountRequest
		Repo       string `json:"repo"`
		AgentLogin string `json:"agent_login"`
		Invite     bool   `json:"invite"`
	}
	if !decodeJSONInto(w, r, &input) {
		return
	}
	host, ok := githubHostOrError(w, input.APIBase)
	if !ok {
		return
	}
	if input.Invite {
		if err := toolpkg.InviteGitHubCollaborator(r.Context(), host, strings.TrimSpace(input.Repo), strings.TrimSpace(input.AgentLogin)); err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	client := &http.Client{Timeout: 15 * time.Second}
	accepted, err := s.superAgents.AcceptGitHubInvitation(r.Context(), client, input.GitHubAccountRequest, strings.TrimSpace(input.Repo))
	switch {
	case errors.Is(err, superagent.ErrNotFound):
		writeErr(w, http.StatusNotFound, "trigger not found; sign the agent in again")
	case errors.Is(err, superagent.ErrGitHubTokenRejected), errors.Is(err, superagent.ErrGitHubTokenMissing):
		writeErr(w, http.StatusBadRequest, err.Error())
	case err != nil:
		writeErr(w, http.StatusBadGateway, err.Error())
	default:
		writeJSON(w, http.StatusOK, map[string]bool{"invited": input.Invite, "accepted": accepted})
	}
}

var errBadDirectoryRequest = errors.New("invalid JSON body")

// handleFeishuDirectory checks the channel before asking Feishu, so a wrong id
// is a 4xx here and only Feishu's own refusals, such as a missing app scope,
// come back as 502 with Feishu's message. A missing scope also carries the
// scopes that would do and the console link to request them.
func (s *Server) handleFeishuDirectory(lookup func(*http.Request, string) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.feishuDirectory == nil || s.st == nil {
			writeErr(w, http.StatusServiceUnavailable, "feishu directory unavailable")
			return
		}
		channelID := strings.TrimSpace(r.URL.Query().Get("channel_id"))
		if channelID == "" {
			writeErr(w, http.StatusBadRequest, "channel_id is required")
			return
		}
		channel, err := s.st.GetChannel(r.Context(), channelID)
		switch {
		case err != nil:
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		case channel == nil:
			writeErr(w, http.StatusNotFound, "channel not found")
			return
		case channel.Type != "feishu" && channel.Type != "lark":
			writeErr(w, http.StatusBadRequest, "channel is not a Feishu channel")
			return
		}
		result, err := lookup(r, channelID)
		var denied *superagent.FeishuScopeError
		switch {
		case errors.Is(err, errBadDirectoryRequest):
			writeErr(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, errFeishuUserUnavailable):
			writeErr(w, http.StatusServiceUnavailable, err.Error())
		case errors.Is(err, superagent.ErrFeishuUserAuthRequired):
			// The Console asks the person to authorize, then retries.
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "user_auth_required": true})
		case errors.As(err, &denied):
			// The Console offers the link so the operator can request the scope.
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "scopes": denied.Scopes, "token_type": denied.TokenType, "apply_url": denied.ApplyURL})
		case err != nil:
			writeErr(w, http.StatusBadGateway, err.Error())
		default:
			writeJSON(w, http.StatusOK, result)
		}
	}
}

func splitQueryList(value string) []string {
	var items []string
	for _, item := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' }) {
		if !slices.Contains(items, item) {
			items = append(items, item)
		}
	}
	return items
}

// handleSuperAgentTickets lists the newest Tickets of every agent here, for
// views that span agents and machines.
func (s *Server) handleSuperAgentTickets(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgents(w) {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	tickets, err := s.superAgents.Tickets(r.Context(), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, tickets)
}

// handleHostedWorkerLaunch keeps a worker here for an agent that lives on
// another machine, which reaches this API as an administrator: over this
// machine's link to its hub, or with this machine's token over SSH.
func (s *Server) handleHostedWorkerLaunch(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgents(w) {
		return
	}
	request, ok := decodeJSON[core.HostedWorkerRequest](w, r)
	if !ok {
		return
	}
	hosted, err := s.superAgents.HostWorker(r.Context(), request)
	if err != nil {
		writeHostedWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, hosted)
}

func (s *Server) handleHostedWorkerRelease(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgents(w) {
		return
	}
	body, ok := decodeJSON[struct {
		OwnerID string `json:"owner_id"`
	}](w, r)
	if !ok {
		return
	}
	removed, err := s.superAgents.ReleaseHostedWorkers(r.Context(), body.OwnerID)
	if err != nil {
		writeHostedWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"removed": removed})
}

func writeHostedWorkerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, superagent.ErrInvalid):
		writeErr(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), superagent.ErrInvalid.Error()+": "))
	case errors.Is(err, superagent.ErrConflict):
		writeErr(w, http.StatusConflict, err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

func writeSuperAgentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, superagent.ErrNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, superagent.ErrConflict):
		writeErr(w, http.StatusConflict, err.Error())
	default:
		writeErr(w, http.StatusBadRequest, err.Error())
	}
}
