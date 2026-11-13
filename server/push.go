package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	pushpkg "github.com/wangning19940904/AgentMux/push"
)

// Web Push for the Console: the hub's public key, this person's subscribed
// devices, and a test notification. What is notified is wired in setupPush
// (machines and their reports) and by bootstrap (agents, Super Agents).

// setupPush creates the push service when there is a store to keep keys and
// subscriptions in. Without one, the endpoints answer 503.
func (s *Server) setupPush(sender pushpkg.Sender) {
	if s.st == nil {
		return
	}
	service, err := pushpkg.New(context.Background(), pushpkg.Options{
		Store: s.st, Sender: sender, Subject: s.pushSubject(), Log: s.log,
		DeviceName: func(ctx context.Context, id string) string { return s.pushDeviceName(ctx, id) },
	})
	if err != nil {
		if s.log != nil {
			s.log.Warn("push notifications unavailable", "err", err)
		}
		return
	}
	s.push = service
}

// Push is the push service, for wiring event sources; nil without a store.
func (s *Server) Push() *pushpkg.Service { return s.push }

// pushSubject is the contact push services see: the hub's public HTTPS
// address when it has one, otherwise the project's.
func (s *Server) pushSubject() string {
	if s.cfg != nil {
		if parsed, err := url.Parse(strings.TrimSpace(s.cfg.Hub.PublicURL)); err == nil && parsed.Scheme == "https" && parsed.Host != "" {
			return "https://" + parsed.Host
		}
	}
	return pushpkg.DefaultSubject
}

func (s *Server) pushDeviceName(ctx context.Context, id string) string {
	if s.st == nil {
		return ""
	}
	device, err := s.st.GetDevice(ctx, id)
	if err != nil || device == nil {
		return ""
	}
	return device.Name
}

// pushDeviceNames reads names from the hub's copy of a node's data.
type pushDeviceNames struct{ server *Server }

func (n pushDeviceNames) DeviceName(ctx context.Context, deviceID string) string {
	return n.server.pushDeviceName(ctx, deviceID)
}

func (n pushDeviceNames) DeviceAgentName(ctx context.Context, deviceID, channelID string) string {
	if channelID == "" || n.server.st == nil {
		return ""
	}
	var channels []core.Channel
	if !n.reported(ctx, deviceID, "/api/v1/channels", &channels) {
		return ""
	}
	for _, channel := range channels {
		if channel.ID != channelID {
			continue
		}
		var agents []core.AgentInstance
		if channel.AgentID != "" && n.reported(ctx, deviceID, "/api/v1/agent-instances", &agents) {
			for _, agent := range agents {
				if agent.ID == channel.AgentID && agent.Name != "" {
					return agent.Name
				}
			}
		}
		return channel.Name
	}
	return ""
}

func (n pushDeviceNames) reported(ctx context.Context, deviceID, path string, into any) bool {
	payload, _, ok, err := n.server.st.DeviceReportedResponse(ctx, deviceID, path)
	return err == nil && ok && json.Unmarshal(payload, into) == nil
}

func (s *Server) registerPushRoutes() {
	s.mux.HandleFunc("GET /api/v1/push", s.handlePushStatus)
	s.mux.HandleFunc("POST /api/v1/push/subscriptions", s.handlePushSubscribe)
	s.mux.HandleFunc("DELETE /api/v1/push/subscriptions", s.handlePushUnsubscribe)
	s.mux.HandleFunc("POST /api/v1/push/test", s.handlePushTest)
}

type pushStatus struct {
	PublicKey     string                  `json:"public_key"`
	Subscriber    string                  `json:"subscriber"`
	Kinds         []string                `json:"kinds"`
	Subscriptions []core.PushSubscription `json:"subscriptions"`
}

func (s *Server) handlePushStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.push == nil {
		serviceUnavailable(w, "push notifications")
		return
	}
	subscriber := pushpkg.SubscriberFor(requestPrincipal(r))
	subs, err := s.push.Subscriptions(r.Context(), subscriber)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, pushStatus{
		PublicKey: s.push.PublicKey(), Subscriber: subscriber, Kinds: core.PushKinds(), Subscriptions: subs,
	})
}

func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	if s.push == nil {
		serviceUnavailable(w, "push notifications")
		return
	}
	var req pushpkg.SubscribeRequest
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if !decodeJSONInto(w, r, &req) {
		return
	}
	sub, err := s.push.Subscribe(r.Context(), pushpkg.SubscriberFor(requestPrincipal(r)), req)
	if errors.Is(err, pushpkg.ErrInvalidSubscription) {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sub)
}

func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	if s.push == nil {
		serviceUnavailable(w, "push notifications")
		return
	}
	id, ok := requireQuery(w, r, "id")
	if !ok {
		return
	}
	if _, err := s.push.Unsubscribe(r.Context(), pushpkg.SubscriberFor(requestPrincipal(r)), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Removing a subscription that is already gone is not an error: the
	// browser only needs to know the server will not use it.
	writeOK(w)
}

func (s *Server) handlePushTest(w http.ResponseWriter, r *http.Request) {
	if s.push == nil {
		serviceUnavailable(w, "push notifications")
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if r.ContentLength != 0 && !decodeJSONInto(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	results := s.push.SendTest(ctx, pushpkg.SubscriberFor(requestPrincipal(r)), strings.TrimSpace(req.ID))
	if results == nil {
		results = []pushpkg.Delivery{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}
