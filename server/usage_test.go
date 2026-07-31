package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHandleUsageParsesInclusiveLocalDateRange(t *testing.T) {
	previousLocal := time.Local
	time.Local = time.FixedZone("UTC+8", 8*60*60)
	t.Cleanup(func() { time.Local = previousLocal })

	var gotPeriod string
	var gotSince, gotUntil time.Time
	s := &Server{usageFn: func(_ context.Context, period string, since, until time.Time) (any, error) {
		gotPeriod, gotSince, gotUntil = period, since, until
		return map[string]bool{"ok": true}, nil
	}}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/usage?period=weekly&from=2026-07-01&to=2026-07-07", nil)
	response := httptest.NewRecorder()

	s.handleUsage(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if gotPeriod != "weekly" {
		t.Fatalf("period = %q", gotPeriod)
	}
	if want := time.Date(2026, 7, 1, 0, 0, 0, 0, time.Local); !gotSince.Equal(want) {
		t.Fatalf("since = %v, want %v", gotSince, want)
	}
	if want := time.Date(2026, 7, 8, 0, 0, 0, 0, time.Local); !gotUntil.Equal(want) {
		t.Fatalf("until = %v, want exclusive %v", gotUntil, want)
	}
}

func TestHandleUsageRejectsReversedDateRange(t *testing.T) {
	s := &Server{usageFn: func(_ context.Context, _ string, _, _ time.Time) (any, error) {
		t.Fatal("reporter should not be called")
		return nil, nil
	}}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/usage?from=2026-07-08&to=2026-07-07", nil)
	response := httptest.NewRecorder()

	s.handleUsage(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
}
