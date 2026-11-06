package server

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/internal/hostmetrics"
)

type sessionActivitySender struct {
	activity core.SessionActivity
}

func (sessionActivitySender) SendToChannel(context.Context, core.ChannelDelivery) error { return nil }

func (s sessionActivitySender) SessionActivity() core.SessionActivity { return s.activity }

func TestSystemMetricsReportsHostAndSessions(t *testing.T) {
	s, _ := newTestServer(t)
	s.version = "9.9.9"
	s.hostMetrics = hostmetrics.New(hostmetrics.Options{PrimeDelay: 20 * time.Millisecond})
	t.Cleanup(s.hostMetrics.Close)
	s.sender = sessionActivitySender{activity: core.SessionActivity{NeedsInput: 1, Working: 2, Idle: 3}}

	rec := doJSON(t, s, http.MethodGet, "/api/v1/system/metrics", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("metrics must not be cached")
	}
	var body struct {
		Supported       bool                     `json:"supported"`
		AgentMuxVersion string                   `json:"agentmux_version"`
		Host            hostmetrics.HostInfo     `json:"host"`
		Memory          *hostmetrics.MemoryStats `json:"memory"`
		History         []hostmetrics.Point      `json:"history"`
		Sessions        *core.SessionActivity    `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.AgentMuxVersion != "9.9.9" || body.Host.CPUCores != runtime.NumCPU() || body.Host.OS != runtime.GOOS {
		t.Fatalf("body = %+v", body)
	}
	if body.Sessions == nil || *body.Sessions != (core.SessionActivity{NeedsInput: 1, Working: 2, Idle: 3}) {
		t.Fatalf("sessions = %+v", body.Sessions)
	}
	if body.Supported && (body.Memory == nil || body.Memory.Total == 0 || len(body.History) != 1) {
		t.Fatalf("supported platform returned incomplete metrics: %s", rec.Body.String())
	}
}

func TestSystemMetricsOmitsSessionsWithoutEngine(t *testing.T) {
	s, _ := newTestServer(t)
	s.hostMetrics = hostmetrics.New(hostmetrics.Options{PrimeDelay: time.Millisecond})
	t.Cleanup(s.hostMetrics.Close)

	rec := doJSON(t, s, http.MethodGet, "/api/v1/system/metrics", nil)
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["sessions"]; ok {
		t.Fatalf("sessions reported without an engine: %s", rec.Body.String())
	}
}

func TestSystemMetricsIsAFleetReadableRoute(t *testing.T) {
	if !fleetOperationAllowed(http.MethodGet, "/api/v1/system/metrics", true) {
		t.Fatal("machine status page reads metrics through fleet query")
	}
	if tenantRouteAllowed(http.MethodGet, "/api/v1/system/metrics") {
		t.Fatal("host metrics describe the whole machine and stay admin-only")
	}
}
