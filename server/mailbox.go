package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	linkpkg "github.com/wangning19940904/AgentMux/link"
	"github.com/wangning19940904/AgentMux/mailbox"
	pushpkg "github.com/wangning19940904/AgentMux/push"
)

// The mailbox: direct messages between agents, and between people and
// agents. This file gives the mailbox what only the server knows (who
// exists and who owns what, the links to other machines, who is signed in)
// and serves it to the Console and to the machines on either end of a link.

const (
	mailDeliverPath         = "/api/v1/mailbox/deliver"
	mailUplinkMessagesPath  = "/uplink/v1/mail/messages"
	mailUplinkDirectoryPath = "/uplink/v1/mail/directory"
	mailAdminLogin          = "admin"
	maxMailBodyBytes        = 1 << 20
)

// setupMailbox creates this machine's mailbox when there is a store to keep
// it in.
func (s *Server) setupMailbox() {
	if s.st == nil {
		return
	}
	s.mail = mailbox.New(mailbox.Options{
		Store: s.st, Directory: mailDirectory{server: s}, Hub: mailHub{server: s}, Log: s.log,
		Notify: s.notifyMail,
	})
	if s.linkHub != nil {
		s.mail.SetDevices(mailDevices{server: s})
	}
}

// Mailbox returns this machine's mailbox, or nil without a store.
func (s *Server) Mailbox() *mailbox.Service { return s.mail }

func (s *Server) registerMailboxRoutes() {
	s.mux.HandleFunc("GET /api/v1/mailbox", s.handleMailboxOverview)
	s.mux.HandleFunc("GET /api/v1/mailbox/messages", s.handleMailboxMessages)
	s.mux.HandleFunc("POST /api/v1/mailbox/messages", s.handleMailboxSend)
	s.mux.HandleFunc("GET /api/v1/mailbox/directory", s.handleMailboxDirectory)
	s.mux.HandleFunc("GET /api/v1/mailbox/contacts", s.handleMailboxContacts)
	s.mux.HandleFunc("POST /api/v1/mailbox/contacts", s.handleMailboxContactAdd)
	s.mux.HandleFunc("POST /api/v1/mailbox/contacts/accept", s.handleMailboxContactAccept)
	s.mux.HandleFunc("DELETE /api/v1/mailbox/contacts", s.handleMailboxContactRemove)
	s.mux.HandleFunc("POST "+mailDeliverPath, s.handleMailboxDeliver)
}

func (s *Server) requireMailbox(w http.ResponseWriter) bool {
	if s.mail == nil {
		serviceUnavailable(w, "direct messages")
		return false
	}
	return true
}

// mailViewer is the person behind a request. A caller without an account,
// such as the bridge token, is the administrator.
func mailViewer(r *http.Request) mailbox.Viewer {
	principal := requestPrincipal(r)
	if principal == nil || principal.Account == nil {
		return mailbox.Viewer{Address: personAddress(mailAdminLogin), Name: "Administrator", Admin: true}
	}
	account := principal.Account
	viewer := mailbox.Viewer{
		Address: personAddress(account.Login), Name: firstNonEmptyString(account.DisplayName, account.Login),
		Admin: !principal.IsTenant(),
	}
	if !viewer.Admin {
		viewer.Owner = account.TenantID
	}
	return viewer
}

func personAddress(login string) string { return core.MailPartyPerson + ":" + login }

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func writeMailError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, mailbox.ErrForbidden):
		writeErr(w, http.StatusForbidden, "you cannot do this for parties you do not own")
	case mailbox.IsRefusal(err):
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

type mailOverview struct {
	Me            core.MailParty          `json:"me"`
	Admin         bool                    `json:"admin"`
	Conversations []core.MailConversation `json:"conversations"`
	// AgentConversations are what the viewer's agents wrote to each other
	// and to other people.
	AgentConversations []core.MailConversation `json:"agent_conversations"`
	Unread             int                     `json:"unread"`
	// Controlled are the addresses the viewer answers for.
	Controlled []string `json:"controlled"`
}

func (s *Server) handleMailboxOverview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.requireMailbox(w) {
		return
	}
	viewer := mailViewer(r)
	mine, err := s.mail.Conversations(r.Context(), viewer, mailbox.ScopeMine)
	if err != nil {
		writeMailError(w, err)
		return
	}
	agents, err := s.mail.Conversations(r.Context(), viewer, mailbox.ScopeAgents)
	if err != nil {
		writeMailError(w, err)
		return
	}
	controlled, err := s.mail.Controlled(r.Context(), viewer)
	if err != nil {
		writeMailError(w, err)
		return
	}
	overview := mailOverview{
		Me:    core.MailParty{Address: viewer.Address, Kind: core.MailPartyPerson, Name: viewer.Name, Online: true},
		Admin: viewer.Admin, Conversations: mine, AgentConversations: agents, Controlled: controlled,
	}
	for _, conversation := range mine {
		overview.Unread += conversation.Unread
	}
	writeJSON(w, http.StatusOK, overview)
}

func (s *Server) handleMailboxMessages(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.requireMailbox(w) {
		return
	}
	viewer := mailViewer(r)
	a, b := strings.TrimSpace(r.URL.Query().Get("with")), viewer.Address
	if other := strings.TrimSpace(r.URL.Query().Get("and")); other != "" {
		b = other
	}
	if a == "" {
		writeErr(w, http.StatusBadRequest, "with is required")
		return
	}
	messages, err := s.mail.Messages(r.Context(), viewer, a, b)
	if err != nil {
		writeMailError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, messages)
}

func (s *Server) handleMailboxSend(w http.ResponseWriter, r *http.Request) {
	if !s.requireMailbox(w) {
		return
	}
	body, ok := decodeJSON[struct {
		To   string `json:"to"`
		Text string `json:"text"`
	}](w, r)
	if !ok {
		return
	}
	message, err := s.mail.SendAs(r.Context(), mailViewer(r), body.To, body.Text)
	if err != nil {
		writeMailError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, message)
}

func (s *Server) handleMailboxDirectory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.requireMailbox(w) {
		return
	}
	parties, reachable, err := s.mail.Directory(r.Context(), mailViewer(r))
	if err != nil {
		writeMailError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"parties": parties, "reachable": reachable})
}

func (s *Server) handleMailboxContacts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.requireMailbox(w) {
		return
	}
	contacts, err := s.mail.Contacts(r.Context(), mailViewer(r))
	if err != nil {
		writeMailError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, contacts)
}

func (s *Server) handleMailboxContactAdd(w http.ResponseWriter, r *http.Request) {
	if !s.requireMailbox(w) {
		return
	}
	body, ok := decodeJSON[struct {
		A string `json:"a"`
		B string `json:"b"`
	}](w, r)
	if !ok {
		return
	}
	contact, err := s.mail.AddContact(r.Context(), mailViewer(r), strings.TrimSpace(body.A), strings.TrimSpace(body.B))
	if err != nil {
		writeMailError(w, err)
		return
	}
	s.mail.Wake()
	writeJSON(w, http.StatusOK, contact)
}

func (s *Server) handleMailboxContactAccept(w http.ResponseWriter, r *http.Request) {
	if !s.requireMailbox(w) {
		return
	}
	body, ok := decodeJSON[struct {
		ID string `json:"id"`
	}](w, r)
	if !ok {
		return
	}
	contact, err := s.mail.AcceptContact(r.Context(), mailViewer(r), body.ID)
	if err != nil {
		writeMailError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, contact)
}

func (s *Server) handleMailboxContactRemove(w http.ResponseWriter, r *http.Request) {
	if !s.requireMailbox(w) {
		return
	}
	if err := s.mail.RemoveContact(r.Context(), mailViewer(r), r.URL.Query().Get("id")); err != nil {
		writeMailError(w, err)
		return
	}
	writeOK(w)
}

// handleMailboxDeliver takes a message this machine's hub passed down the
// link. The hub decided who may write to whom, so the route is the
// administrator's alone: the link answers as the administrator.
func (s *Server) handleMailboxDeliver(w http.ResponseWriter, r *http.Request) {
	if !s.requireMailbox(w) {
		return
	}
	var message core.MailMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxMailBodyBytes)).Decode(&message); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid message")
		return
	}
	if err := s.mail.ReceiveFromHub(r.Context(), message); err != nil {
		writeMailError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"delivered": true})
}

// mailUplink answers what linked machines send up their links: reports, and
// the messages and directory questions of their agents.
func (s *Server) mailUplink(reports http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", reports)
	mux.HandleFunc("POST "+mailUplinkMessagesPath, func(w http.ResponseWriter, r *http.Request) {
		deviceID := linkpkg.DeviceID(r.Context())
		if deviceID == "" || s.mail == nil {
			writeErr(w, http.StatusUnauthorized, "messages are accepted only over a device link")
			return
		}
		var message core.MailMessage
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxMailBodyBytes)).Decode(&message); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid message")
			return
		}
		if err := s.mail.AcceptFromDevice(r.Context(), deviceID, message); err != nil {
			writeMailError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"accepted": true})
	})
	mux.HandleFunc("GET "+mailUplinkDirectoryPath, func(w http.ResponseWriter, r *http.Request) {
		deviceID := linkpkg.DeviceID(r.Context())
		if deviceID == "" || s.mail == nil {
			writeErr(w, http.StatusUnauthorized, "the directory is read only over a device link")
			return
		}
		parties, err := s.mail.ReachableFromDevice(r.Context(), deviceID, strings.TrimSpace(r.URL.Query().Get("agent")))
		if err != nil {
			writeMailError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"parties": parties})
	})
	return mux
}

// notifyMail tells a person's devices that a message arrived for them.
func (s *Server) notifyMail(ctx context.Context, login string, message core.MailMessage) {
	if s.push == nil {
		return
	}
	subscriber := pushpkg.SubscriberAdmin
	if account, err := s.st.GetAccountByLogin(ctx, login); err == nil && account != nil && !account.IsAdmin() {
		subscriber = "tenant:" + account.TenantID
	}
	s.push.Notify(subscriber, pushpkg.MessageEvent(message.ID, message.From, message.FromName, time.Now().UTC()))
}

// mailDirectory lists the parties this machine knows.
type mailDirectory struct{ server *Server }

func (d mailDirectory) Parties(ctx context.Context) ([]core.MailParty, error) {
	s := d.server
	accounts, err := s.st.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	// Administrators are one owner: what any of them owns, all of them own.
	adminTenants, ownerNames := map[string]bool{}, map[string]string{}
	for _, account := range accounts {
		if account.IsAdmin() {
			adminTenants[account.TenantID] = true
		} else {
			ownerNames[account.TenantID] = firstNonEmptyString(account.DisplayName, account.Login)
		}
	}
	owner := func(tenantID string) (string, string) {
		if tenantID == "" || adminTenants[tenantID] {
			return "", ""
		}
		return tenantID, ownerNames[tenantID]
	}
	parties := []core.MailParty{}

	var agents []core.SuperAgent
	if s.superAgents != nil {
		if agents, err = s.superAgents.Stored(ctx); err != nil {
			return nil, err
		}
	}
	instances, err := s.st.ListAgentInstances(ctx)
	if err != nil {
		return nil, err
	}
	brainOwner := map[string]string{}
	for _, instance := range instances {
		brainOwner[instance.ID] = instance.OwnerTenantID
	}
	for _, agent := range agents {
		if !agent.Enabled {
			continue
		}
		party := core.MailParty{
			Address: core.MailPartyAgent + ":" + agent.ID, Kind: core.MailPartyAgent,
			Name: firstNonEmptyString(agent.Name, agent.ID), Description: agent.Description, Online: true,
		}
		party.Owner, party.OwnerName = owner(brainOwner[agent.RouterAgentID])
		parties = append(parties, party)
	}

	hasAdminLogin, hasAdmin := false, false
	for _, account := range accounts {
		if !account.Active() {
			continue
		}
		hasAdmin = hasAdmin || account.IsAdmin()
		party := core.MailParty{
			Address: personAddress(account.Login), Kind: core.MailPartyPerson,
			Name: firstNonEmptyString(account.DisplayName, account.Login), Admin: account.IsAdmin(), Online: true,
		}
		if !party.Admin {
			party.Owner, party.OwnerName = account.TenantID, party.Name
		}
		hasAdminLogin = hasAdminLogin || account.Login == mailAdminLogin
		parties = append(parties, party)
	}
	// Whoever uses this machine without an account, with its token. Once
	// administrators have accounts, that caller is listed only after it
	// wrote to someone, so there is somewhere to answer.
	tokenUser := !hasAdminLogin && !hasAdmin
	if !hasAdminLogin && hasAdmin {
		if tokenUser, err = s.st.HasMailSender(ctx, personAddress(mailAdminLogin)); err != nil {
			return nil, err
		}
	}
	if tokenUser {
		parties = append(parties, core.MailParty{
			Address: personAddress(mailAdminLogin), Kind: core.MailPartyPerson, Name: "Administrator", Admin: true, Online: true,
		})
	}

	if s.linkHub == nil {
		return parties, nil
	}
	devices, err := s.st.ListDevices(ctx)
	if err != nil {
		return nil, err
	}
	for _, device := range devices {
		payload, _, ok, err := s.st.DeviceReportedResponse(ctx, device.ID, "/api/v1/super-agents")
		if err != nil || !ok {
			continue
		}
		var reported []core.SuperAgent
		if json.Unmarshal(payload, &reported) != nil {
			continue
		}
		_, online := s.linkHub.Connection(device.ID)
		for _, agent := range reported {
			if !agent.Enabled || agent.ID == "" {
				continue
			}
			party := core.MailParty{
				Address: core.MailAddress{Kind: core.MailPartyAgent, ID: agent.ID, Machine: device.ID}.String(),
				Kind:    core.MailPartyAgent, Name: firstNonEmptyString(agent.Name, agent.ID), Description: agent.Description,
				Machine: firstNonEmptyString(device.Name, device.ID), Online: online,
			}
			party.Owner, party.OwnerName = owner(device.OwnerTenantID)
			parties = append(parties, party)
		}
	}
	return parties, nil
}

func (d mailDirectory) Device(ctx context.Context, deviceID string) (string, string, bool) {
	s := d.server
	if s.linkHub == nil {
		return "", "", false
	}
	device, err := s.st.GetDevice(ctx, deviceID)
	if err != nil || device == nil {
		return "", "", false
	}
	owner := device.OwnerTenantID
	if owner != "" {
		if accounts, err := s.st.ListAccounts(ctx); err == nil {
			for _, account := range accounts {
				if account.TenantID == owner && account.IsAdmin() {
					owner = ""
				}
			}
		}
	}
	return owner, firstNonEmptyString(device.Name, device.ID), true
}

// mailDevices passes messages down the links of this hub's machines.
type mailDevices struct{ server *Server }

func (d mailDevices) Deliver(ctx context.Context, deviceID string, message core.MailMessage) error {
	hub := d.server.linkHub
	client := &http.Client{Transport: &http.Transport{
		Proxy: nil, DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return hub.Dial(ctx, deviceID) },
	}}
	return postMail(ctx, client, "http://device"+mailDeliverPath, message, "that machine's AgentMux")
}

// mailHub is the hub this machine joined, reached up its link.
type mailHub struct{ server *Server }

func (h mailHub) client() *linkpkg.Client {
	node := h.server.node
	if node == nil {
		return nil
	}
	node.mu.Lock()
	defer node.mu.Unlock()
	return node.client
}

func (h mailHub) Joined() bool { return h.client() != nil }

func (h mailHub) Send(ctx context.Context, message core.MailMessage) error {
	client := h.client()
	if client == nil {
		return errors.New("this machine has not joined a hub")
	}
	return postMail(ctx, client.Uplink(), "http://hub"+mailUplinkMessagesPath, message, "the hub's AgentMux")
}

func (h mailHub) Reachable(ctx context.Context, agentID string) ([]core.MailParty, error) {
	client := h.client()
	if client == nil {
		return nil, errors.New("this machine has not joined a hub")
	}
	target := "http://hub" + mailUplinkDirectoryPath + "?agent=" + url.QueryEscape(agentID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Uplink().Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the hub answered %d", response.StatusCode)
	}
	var answer struct {
		Parties []core.MailParty `json:"parties"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&answer); err != nil {
		return nil, err
	}
	return answer.Parties, nil
}

// postMail sends a message to the machine at the other end of a link. A
// machine that says no gives a Refusal; a link that is down, or a machine
// that failed, gives an ordinary error and the message is tried again.
func postMail(ctx context.Context, client *http.Client, target string, message core.MailMessage, peer string) error {
	body, err := json.Marshal(message)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	var answer struct {
		Error     string `json:"error"`
		Delivered bool   `json:"delivered"`
		Accepted  bool   `json:"accepted"`
	}
	decoded := json.Unmarshal(payload, &answer) == nil
	switch {
	case response.StatusCode == http.StatusOK && decoded && (answer.Delivered || answer.Accepted):
		return nil
	case response.StatusCode == http.StatusUnprocessableEntity && decoded && answer.Error != "":
		return mailbox.Refuse("%s", answer.Error)
	case response.StatusCode == http.StatusOK || response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed:
		// An AgentMux from before direct messages answers with its Console
		// page or does not know the path.
		return mailbox.Refuse("%s is too old for direct messages; update it", peer)
	case decoded && answer.Error != "":
		return fmt.Errorf("%s: %s", peer, answer.Error)
	}
	return fmt.Errorf("%s answered %d", peer, response.StatusCode)
}
