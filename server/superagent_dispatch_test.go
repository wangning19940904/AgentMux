package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
	linkpkg "github.com/wangning19940904/AgentMux/link"
	"github.com/wangning19940904/AgentMux/superagent"
)

// noTickets is a Super Agent store that holds no Ticket.
type noTickets struct{ superagent.Store }

func (noTickets) GetSuperAgentTicket(context.Context, string) (*core.SuperAgentTicket, error) {
	return nil, nil
}

func TestDispatchRoutesTakeTheTicketTokenInsteadOfTheBridgeToken(t *testing.T) {
	s, _ := newTestServer(t)
	s.cfg.Bridge.Enabled, s.cfg.Bridge.Token = true, "bridge-secret"
	s.SetSuperAgents(superagent.New(superagent.Options{Store: noTickets{}}), nil)
	handler := s.withAuth(s.mux)
	post := func(path, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	// No bridge token, yet the call reaches the handler, which checks the
	// Ticket token itself.
	got := post(superagent.DispatchReadPath, `{"token":"amd1..tkt_none.secret","id":"hlp_1"}`)
	if got.Code != http.StatusUnauthorized || !strings.Contains(got.Body.String(), "names no Ticket") {
		t.Fatalf("read with an unknown token = %d %s", got.Code, got.Body)
	}
	got = post(superagent.DispatchStartPath, `{"token":"not a token"}`)
	if got.Code != http.StatusUnauthorized || !strings.Contains(got.Body.String(), "not a dispatch token") {
		t.Fatalf("start with no token = %d %s", got.Code, got.Body)
	}
	got = post(superagent.DispatchStartPath, `{`)
	if got.Code != http.StatusBadRequest {
		t.Fatalf("bad JSON = %d", got.Code)
	}
	// A Ticket on another machine needs a hub to reach it.
	got = post(superagent.DispatchStartPath, `{"token":"amd1.dev_agent.tkt_far.secret","worker":"w","instructions":"x"}`)
	if got.Code != http.StatusBadGateway || !strings.Contains(got.Body.String(), "no hub") {
		t.Fatalf("start for another machine without a hub = %d %s", got.Code, got.Body)
	}
	// Every other Super Agent route still needs the bridge token.
	got = post("/api/v1/super-agents/tickets/stop", `{"id":"tkt_x"}`)
	if got.Code != http.StatusUnauthorized {
		t.Fatalf("stop without credentials = %d", got.Code)
	}
}

func TestDispatchUplinkAnswersOnlyForThisMachineOverADeviceLink(t *testing.T) {
	s, _ := newTestServer(t)
	s.SetSuperAgents(superagent.New(superagent.Options{Store: noTickets{}}), nil)
	mux := http.NewServeMux()
	s.registerDispatchUplink(mux)
	body := `{"token":"amd1.dev_other.tkt_far.secret","id":"hlp_1"}`

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, superagent.DispatchUplinkReadPath, strings.NewReader(body)))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("without a device link = %d", recorder.Code)
	}

	request := httptest.NewRequest(http.MethodPost, superagent.DispatchUplinkReadPath, strings.NewReader(body))
	request = request.WithContext(linkpkg.WithDeviceID(request.Context(), "dev_worker"))
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	// The hub does not pass a call it cannot answer on again.
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("a Ticket the hub does not hold = %d %s", recorder.Code, recorder.Body)
	}
}

func TestDispatchHomeNamesTheHub(t *testing.T) {
	s, _ := newTestServer(t)
	if home := s.dispatchHome(); home != "" {
		t.Fatalf("a machine on its own = %q", home)
	}
	s.cfg.Hub.Addr = "0.0.0.0:8443"
	if home := s.dispatchHome(); home != superagent.DispatchHomeHub {
		t.Fatalf("a hub = %q", home)
	}
}
