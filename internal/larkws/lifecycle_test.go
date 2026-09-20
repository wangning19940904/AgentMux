package ws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestStartCancellationClosesConnection(t *testing.T) {
	opened := make(chan struct{}, 1)
	closed := make(chan struct{}, 1)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == GenEndpointURI {
			json.NewEncoder(w).Encode(&EndpointResp{Code: OK, Data: &Endpoint{URL: "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?device_id=d&service_id=1"}})
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		opened <- struct{}{}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				closed <- struct{}{}
				return
			}
		}
	}))
	defer server.Close()
	client := NewClient("app", "secret", WithDomain(server.URL))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Start(ctx) }()
	select {
	case <-opened:
	case <-time.After(3 * time.Second):
		t.Fatal("connection did not open")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Start leaked after cancellation")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("old socket still open")
	}
	if client.shouldReconnect(ctx) {
		t.Fatal("cancelled client reconnected")
	}
}
func TestReconnectWaitIsCancellable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if waitContext(ctx, time.Hour) {
		t.Fatal("cancelled wait completed normally")
	}
	client := NewClient("a", "b")
	if err := client.reconnect(ctx); err == nil {
		t.Fatal("reconnect ignored cancellation")
	}
}
