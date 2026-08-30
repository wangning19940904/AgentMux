package usage

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

type cursorRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn cursorRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestFetchCursorDashboardPaginatesAndKeepsAgentEventsOnly(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	client := &http.Client{Transport: cursorRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != cursorDashboardEventsURL || request.Header.Get("Origin") != cursorDashboardOrigin {
			t.Fatalf("request = %s headers=%v", request.URL, request.Header)
		}
		var body struct {
			Page int `json:"page"`
		}
		_ = json.NewDecoder(request.Body).Decode(&body)
		var events []map[string]any
		hasNext := body.Page == 1
		if body.Page == 1 {
			events = []map[string]any{
				{"id": "local-request", "timestamp": now.UnixMilli(), "model": "claude-sonnet", "tokenUsage": map[string]any{"inputTokens": 10, "outputTokens": 2, "totalCents": 1.5}},
				{"id": "tab-request", "timestamp": now.UnixMilli(), "model": "cursor-tab", "inputTokens": 3},
			}
		} else {
			events = []map[string]any{{"id": "cloud-agent", "conversationId": "conversation-2", "timestamp": now.Add(time.Second).UnixMilli(), "model": "composer", "inputTokens": 4, "outputTokens": 1, "chargedCents": 2.0}}
		}
		payload, _ := json.Marshal(map[string]any{
			"totalUsageEventsCount": 3, "usageEventsDisplay": events,
			"pagination": map[string]any{"numPages": 2, "currentPage": body.Page, "hasNextPage": hasNext},
		})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(payload))), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
	})}
	local := map[string]core.UsageRecord{
		"local-request": {Source: "cursor", RequestID: "local-request", SessionID: "conversation-1", Project: "/tmp/project", Model: "cursor"},
	}
	auth := cursorAuth{SessionToken: "session"}
	result, err := fetchCursorDashboard(context.Background(), client, auth, now.Add(-time.Hour), now.Add(time.Hour), 1, local)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || result.Matched != 1 || result.Ignored != 1 || len(result.Records) != 2 {
		t.Fatalf("result = %+v", result)
	}
	byID := map[string]core.UsageRecord{}
	for _, record := range result.Records {
		byID[record.RequestID] = record
	}
	if record := byID["local-request"]; record.SessionID != "conversation-1" || record.Project != "/tmp/project" || record.CostKind != core.UsageCostKindRecorded || record.CostUSD != 0.015 {
		t.Fatalf("matched record = %+v", record)
	}
	if record := byID["cloud-agent"]; record.SessionID != "conversation-2" || record.Provenance != cursorProvenanceDashboard {
		t.Fatalf("cloud record = %+v", record)
	}
}

func TestCursorDoJSONRejectsUnexpectedHost(t *testing.T) {
	request, _ := http.NewRequest(http.MethodGet, "https://example.com/usage", nil)
	err := cursorDoJSON(&http.Client{}, request, &map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "refusing unexpected") {
		t.Fatalf("err = %v", err)
	}
}
