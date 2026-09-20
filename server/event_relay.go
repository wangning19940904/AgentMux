package server

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/eventrelay"
)

func (s *Server) registerEventRelayRoutes() {
	s.mux.HandleFunc("GET /api/v1/event-sources", s.handleEventSources)
	s.mux.HandleFunc("GET /api/v1/event-subscriptions", s.handleEventSubscriptions)
	s.mux.HandleFunc("POST /api/v1/event-subscriptions", s.handleEventSubscriptionSave)
	s.mux.HandleFunc("DELETE /api/v1/event-subscriptions", s.handleEventSubscriptionDelete)
	s.mux.HandleFunc("POST /api/v1/event-subscriptions/test", s.handleEventSubscriptionTest)
	s.mux.HandleFunc("POST /api/v1/event-subscriptions/rotate-secret", s.handleEventSecretRotate)
	s.mux.HandleFunc("GET /api/v1/event-deliveries", s.handleEventDeliveries)
	s.mux.HandleFunc("POST /api/v1/event-deliveries/retry", s.handleEventDeliveryRetry)
}
func (s *Server) eventStoreAvailable(w http.ResponseWriter) bool {
	if s.st == nil {
		writeErr(w, 503, "event store unavailable")
		return false
	}
	return true
}
func eventOwner(r *http.Request) *string {
	p := requestPrincipal(r)
	if p.IsTenant() {
		v := p.TenantID
		return &v
	}
	return nil
}
func (s *Server) ownedEventSubscription(w http.ResponseWriter, r *http.Request, id string) (*core.EventSubscription, bool) {
	if !s.eventStoreAvailable(w) {
		return nil, false
	}
	sub, err := s.st.GetEventSubscription(r.Context(), id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return nil, false
	}
	if sub == nil || (requestPrincipal(r).IsTenant() && sub.OwnerTenantID != requestPrincipal(r).TenantID) {
		writeErr(w, 404, "subscription not found")
		return nil, false
	}
	return sub, true
}
func (s *Server) validateEventAccess(r *http.Request, sub core.EventSubscription) error {
	for _, ref := range sub.SourceRefs {
		ok, err := s.st.EventSourceAllowed(r.Context(), sub.OwnerTenantID, ref)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("event source permission required for %s", ref)
		}
		if sub.Source != "agentmux" {
			ch, err := s.st.GetChannel(r.Context(), strings.TrimPrefix(ref, "channel:"))
			if err != nil {
				return err
			}
			if ch == nil || ch.Type != sub.Source {
				return fmt.Errorf("source does not match channel platform")
			}
		}
	}
	return nil
}
func (s *Server) handleEventSubscriptions(w http.ResponseWriter, r *http.Request) {
	if !s.eventStoreAvailable(w) {
		return
	}
	subs, err := s.st.ListEventSubscriptions(r.Context(), eventOwner(r))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for i := range subs {
		if err := s.st.EventSubscriptionStats(r.Context(), &subs[i]); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	writeJSON(w, 200, subs)
}
func (s *Server) handleEventSubscriptionSave(w http.ResponseWriter, r *http.Request) {
	if !s.eventStoreAvailable(w) {
		return
	}
	sub, ok := decodeJSON[core.EventSubscription](w, r)
	if !ok {
		return
	}
	if requestPrincipal(r).IsTenant() {
		sub.OwnerTenantID = requestPrincipal(r).TenantID
	}
	if err := eventrelay.ValidateSubscription(&sub); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if sub.ID != "" {
		old, ok := s.ownedEventSubscription(w, r, sub.ID)
		if !ok {
			return
		}
		if old.Deleted {
			writeErr(w, 409, "subscription is deleted")
			return
		}
	}
	if err := s.validateEventAccess(r, sub); err != nil {
		writeErr(w, 403, err.Error())
		return
	}
	secret, err := eventrelay.NewSecret()
	if err != nil {
		writeErr(w, 500, "cannot generate signing key")
		return
	}
	sub.Secret = secret
	sub.Deleted = false
	sub.Pending = 0
	sub.Dead = 0
	sub.LastSuccessAt = nil
	sub.LastError = ""
	created, err := s.st.SaveEventSubscription(r.Context(), &sub)
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	response := map[string]any{"subscription": sub}
	if created {
		response["signing_secret"] = sub.Secret
	}
	writeJSON(w, 200, response)
}
func (s *Server) handleEventSubscriptionDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := requireQuery(w, r, "id")
	if !ok {
		return
	}
	if _, ok = s.ownedEventSubscription(w, r, id); !ok {
		return
	}
	if err := s.st.DeleteEventSubscription(r.Context(), id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeOK(w)
}
func (s *Server) eventActionSubscription(w http.ResponseWriter, r *http.Request) (*core.EventSubscription, bool) {
	body, ok := decodeJSON[struct {
		ID string `json:"id"`
	}](w, r)
	if !ok {
		return nil, false
	}
	sub, ok := s.ownedEventSubscription(w, r, body.ID)
	if !ok {
		return nil, false
	}
	if sub.Deleted {
		writeErr(w, 409, "subscription is deleted")
		return nil, false
	}
	if err := s.validateEventAccess(r, *sub); err != nil {
		writeErr(w, 403, err.Error())
		return nil, false
	}
	return sub, true
}
func (s *Server) handleEventSubscriptionTest(w http.ResponseWriter, r *http.Request) {
	sub, ok := s.eventActionSubscription(w, r)
	if !ok {
		return
	}
	if s.eventRelay == nil {
		writeErr(w, 503, "event relay is not running")
		return
	}
	if sub.Paused {
		writeErr(w, 409, "resume subscription before testing delivery")
		return
	}
	if err := s.eventRelay.Test(r.Context(), *sub); err != nil {
		writeErr(w, 503, "test event could not be persisted")
		return
	}
	writeJSON(w, 202, map[string]any{"ok": true, "queued": true})
}
func (s *Server) handleEventSecretRotate(w http.ResponseWriter, r *http.Request) {
	sub, ok := s.eventActionSubscription(w, r)
	if !ok {
		return
	}
	secret, err := eventrelay.NewSecret()
	if err != nil {
		writeErr(w, 500, "cannot generate signing key")
		return
	}
	if err := s.st.RotateEventSecret(r.Context(), sub.ID, secret); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"signing_secret": secret})
}
func (s *Server) ownedEventDelivery(w http.ResponseWriter, r *http.Request, id string) (*core.EventDelivery, bool) {
	if !s.eventStoreAvailable(w) {
		return nil, false
	}
	d, err := s.st.GetEventDelivery(r.Context(), id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return nil, false
	}
	if d == nil {
		writeErr(w, 404, "delivery not found")
		return nil, false
	}
	sub, ok := s.ownedEventSubscription(w, r, d.SubscriptionID)
	if !ok {
		return nil, false
	}
	tenant := ""
	if requestPrincipal(r).IsTenant() {
		tenant = sub.OwnerTenantID
	}
	allowed, err := s.st.EventSourceAllowed(r.Context(), tenant, d.Event.SourceRef)
	if err != nil || !allowed {
		writeErr(w, 403, "event source permission unavailable")
		return nil, false
	}
	return d, true
}
func (s *Server) handleEventDeliveries(w http.ResponseWriter, r *http.Request) {
	if !s.eventStoreAvailable(w) {
		return
	}
	if id := r.URL.Query().Get("id"); id != "" {
		d, ok := s.ownedEventDelivery(w, r, id)
		if ok {
			writeJSON(w, 200, d)
		}
		return
	}
	limit := 50
	offset := 0
	for key, dest := range map[string]*int{"limit": &limit, "offset": &offset} {
		if value := r.URL.Query().Get(key); value != "" {
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 {
				writeErr(w, 400, "invalid pagination")
				return
			}
			*dest = n
		}
	}
	if limit < 1 || limit > 100 {
		writeErr(w, 400, "limit must be 1–100")
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && !slices.Contains([]string{"pending", "retry", "delivering", "sent", "dead", "blocked", "cancelled"}, status) {
		writeErr(w, 400, "invalid delivery status")
		return
	}
	items, err := s.st.ListEventDeliveries(r.Context(), eventOwner(r), r.URL.Query().Get("subscription_id"), status, limit+1, offset)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	// Status remains visible after revocation, but its payload does not.
	for i := range items {
		allowed, err := s.st.EventSourceAllowed(r.Context(), requestPrincipal(r).TenantID, items[i].Event.SourceRef)
		if err != nil || !allowed {
			items[i].Event = nil
		}
	}
	writeJSON(w, 200, map[string]any{"items": items, "limit": limit, "offset": offset, "has_more": more})
}
func (s *Server) handleEventDeliveryRetry(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeJSON[struct {
		ID string `json:"id"`
	}](w, r)
	if !ok {
		return
	}
	d, ok := s.ownedEventDelivery(w, r, body.ID)
	if !ok {
		return
	}
	sub, ok := s.ownedEventSubscription(w, r, d.SubscriptionID)
	if !ok {
		return
	}
	if sub.Deleted {
		writeErr(w, 409, "subscription is deleted")
		return
	}
	allowed, err := s.st.EventSourceAllowed(r.Context(), sub.OwnerTenantID, d.Event.SourceRef)
	if err != nil || !allowed {
		writeErr(w, 403, "subscription owner no longer has event permission")
		return
	}
	if err := s.st.RetryEventDelivery(r.Context(), d.ID); err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	writeOK(w)
}
func (s *Server) handleEventSources(w http.ResponseWriter, r *http.Request) {
	if !s.eventStoreAvailable(w) {
		return
	}
	out := []core.EventSource{}
	tenant := requestPrincipal(r).TenantID
	add := func(ref, name string, sources, types []string, connected bool, state string) error {
		ok, err := s.st.EventSourceAllowed(r.Context(), tenant, ref)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		item := core.EventSource{Ref: ref, Name: name, Sources: sources, EventTypes: types, Connected: connected, State: state}
		if s.eventRelay != nil {
			item.Ingestion = s.eventRelay.Health(ref)
		}
		out = append(out, item)
		return nil
	}
	statuses := map[string]core.ChannelStatus{}
	if s.connect != nil {
		for _, st := range s.connect.ChannelStatuses() {
			statuses[st.ChannelID] = st
		}
	}
	channels, err := s.st.ListChannels(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for _, ch := range channels {
		sources := []string{"agentmux"}
		types := core.LifecycleEventTypes()
		if ch.Type == "feishu" || ch.Type == "lark" {
			sources = append(sources, ch.Type)
			types = append(types, "im.message.receive_v1", "card.action.trigger", "drive.file.bitable_record_changed_v1", "drive.file.bitable_field_changed_v1", "drive.file.edit_v1")
			if s.eventRelay != nil {
				types = append(types, s.eventRelay.EventTypes("channel:"+ch.ID, ch.Type)...)
			}
			slices.Sort(types)
			types = slices.Compact(types)
		}
		state := "stopped"
		if ch.Enabled {
			state = "pending"
		}
		st := statuses[ch.ID]
		if st.State != "" {
			state = st.State
		}
		if err = add("channel:"+ch.ID, ch.Name, sources, types, st.Connected, state); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	agents, err := s.st.ListAgentInstances(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for _, a := range agents {
		if err = add("agent:"+a.ID, a.Name, []string{"agentmux"}, core.LifecycleEventTypes(), s.eventRelay != nil, "local"); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	triggers, err := s.st.ListTriggers(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for _, tr := range triggers {
		if err = add("trigger:"+tr.ID, tr.Name, []string{"agentmux"}, core.LifecycleEventTypes(), s.eventRelay != nil, "local"); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if err = add("system", "AgentMux system", []string{"agentmux"}, core.LifecycleEventTypes(), s.eventRelay != nil, "local"); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, out)
}
