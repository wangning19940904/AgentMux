package link

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// memorySink is a hub's copy of device reports, in memory.
type memorySink struct {
	mu      sync.Mutex
	docs    map[string]map[string]string // device/collection -> id -> data
	order   map[string][]string
	shape   map[string]string
	posts   map[string]int
	applied []core.DeviceReport
}

func newMemorySink() *memorySink {
	return &memorySink{docs: map[string]map[string]string{}, order: map[string][]string{}, shape: map[string]string{}, posts: map[string]int{}}
}

func (m *memorySink) DeviceReportState(_ context.Context, deviceID, collection string) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := map[string]string{}
	for id, data := range m.docs[deviceID+"/"+collection] {
		state[id] = core.DeviceReportHash([]byte(data))
	}
	return state, nil
}

func (m *memorySink) ApplyDeviceReport(_ context.Context, deviceID string, report core.DeviceReport, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := deviceID + "/" + report.Collection
	if m.docs[key] == nil {
		m.docs[key] = map[string]string{}
	}
	for _, id := range report.Deletes {
		delete(m.docs[key], id)
	}
	for _, doc := range report.Upserts {
		m.docs[key][doc.ID] = string(doc.Data)
	}
	if report.Order != nil {
		m.order[key] = report.Order
	}
	m.shape[key] = report.Shape
	m.posts[key]++
	m.applied = append(m.applied, report)
	return nil
}

// list returns the hub's copy of a list collection, in reported order.
func (m *memorySink) list(key string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var parts []string
	for _, id := range m.order[key] {
		if data, ok := m.docs[key][id]; ok {
			parts = append(parts, data)
		}
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func (m *memorySink) lastReport() core.DeviceReport {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.applied) == 0 {
		return core.DeviceReport{}
	}
	return m.applied[len(m.applied)-1]
}

func (m *memorySink) postCount(key string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.posts[key]
}

// nodeAPI is a node's own API, whose answers the test changes.
type nodeAPI struct {
	mu      sync.Mutex
	answers map[string]string
}

func (n *nodeAPI) set(path, body string) {
	n.mu.Lock()
	n.answers[path] = body
	n.mu.Unlock()
}

func (n *nodeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	body, ok := n.answers[r.URL.RequestURI()]
	n.mu.Unlock()
	if !ok {
		// What an AgentMux without the endpoint does: its Console page.
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<!doctype html>")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, body)
}

func TestNodeReportsOnlyWhatChanged(t *testing.T) {
	registry := newFakeRegistry("CODE1234")
	hub, join := startHub(t, registry)
	sink := newMemorySink()
	hub.SetUplink(NewReportHandler(sink, nil))
	join.Code = "CODE1234"
	state, err := Pair(context.Background(), join, core.DeviceInfo{Name: "node"})
	if err != nil {
		t.Fatal(err)
	}
	api := &nodeAPI{answers: map[string]string{
		"/api/v1/agents?x=1": `[ {"id":"a","name":"one"}, {"id":"b","name":"two"} ]`,
		"/api/v1/status":     `{"ok":true,"version":"1"}`,
		"/api/v1/names":      `["x","y"]`,
	}}
	client := &Client{
		State: state, Info: core.DeviceInfo{Name: "node"}, Handler: api, ReportInterval: 20 * time.Millisecond,
		Reports: []Report{{"agents", "/api/v1/agents?x=1"}, {"status", "/api/v1/status"}, {"names", "/api/v1/names"}, {"missing", "/api/v1/missing"}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	agents, status, names := state.DeviceID+"/agents", state.DeviceID+"/status", state.DeviceID+"/names"
	waitFor(t, "the first report", func() bool {
		return sink.list(agents) == `[{"id":"a","name":"one"},{"id":"b","name":"two"}]` && sink.postCount(names) > 0
	})
	sink.mu.Lock()
	if sink.docs[status]["_"] != `{"ok":true,"version":"1"}` || sink.shape[status] != core.DeviceReportValue {
		t.Fatalf("status = %q shape %q", sink.docs[status]["_"], sink.shape[status])
	}
	// A list of plain values has no element identity, so it is one value.
	if sink.docs[names]["_"] != `["x","y"]` || sink.shape[names] != core.DeviceReportValue {
		t.Fatalf("names = %q shape %q", sink.docs[names]["_"], sink.shape[names])
	}
	if _, reported := sink.shape[state.DeviceID+"/missing"]; reported {
		t.Fatal("an endpoint the node does not have was reported")
	}
	sink.mu.Unlock()

	// Nothing changed: no further writes reach the hub.
	before := sink.postCount(agents)
	time.Sleep(150 * time.Millisecond)
	if after := sink.postCount(agents); after != before {
		t.Fatalf("an unchanged collection was sent again: %d -> %d", before, after)
	}

	// One element changes, one is added, one goes: only those travel.
	api.set("/api/v1/agents?x=1", `[{"id":"c","name":"three"},{"id":"a","name":"ONE"}]`)
	waitFor(t, "the change to arrive", func() bool {
		return sink.list(agents) == `[{"id":"c","name":"three"},{"id":"a","name":"ONE"}]`
	})
	report := sink.lastReport()
	var upserted []string
	for _, doc := range report.Upserts {
		upserted = append(upserted, doc.ID)
	}
	sort.Strings(upserted)
	if strings.Join(upserted, ",") != "a,c" || strings.Join(report.Deletes, ",") != "b" || strings.Join(report.Order, ",") != "c,a" {
		t.Fatalf("change report = upserts %v deletes %v order %v", upserted, report.Deletes, report.Order)
	}

	// The hub loses its copy; after a reconnect the node sends it all again.
	sink.mu.Lock()
	delete(sink.docs, agents)
	sink.mu.Unlock()
	hub.Disconnect(state.DeviceID)
	waitFor(t, "the copy to be rebuilt after a reconnect", func() bool {
		return sink.list(agents) == `[{"id":"c","name":"three"},{"id":"a","name":"ONE"}]`
	})

	var statuses []string
	for _, item := range client.Status().Reports {
		statuses = append(statuses, fmt.Sprintf("%s:%d:%v:%v", item.Collection, item.Items, item.ReportedAt != nil, item.Error != ""))
	}
	if got := strings.Join(statuses, " "); got != "agents:2:true:false status:1:true:false names:1:true:false missing:0:false:true" {
		t.Fatalf("report status = %s", got)
	}
}

func TestReportsAreRefusedOutsideALink(t *testing.T) {
	handler := NewReportHandler(newMemorySink(), nil)
	for _, request := range []struct{ method, path, body string }{
		{http.MethodGet, reportsPath + "agents", ""},
		{http.MethodPost, reportsPath + "agents", `{"path":"/api/v1/x","shape":"list"}`},
	} {
		recorder := &localResponse{header: http.Header{}}
		req, _ := http.NewRequest(request.method, "http://hub"+request.path, strings.NewReader(request.body))
		handler.ServeHTTP(recorder, req)
		if recorder.status != http.StatusUnauthorized {
			t.Fatalf("%s without a device = %d", request.method, recorder.status)
		}
	}
	withDevice := func(method, path, body string) int {
		recorder := &localResponse{header: http.Header{}}
		req, _ := http.NewRequest(method, "http://hub"+path, strings.NewReader(body))
		handler.ServeHTTP(recorder, req.WithContext(context.WithValue(req.Context(), deviceContextKey{}, "dev_1")))
		return recorder.status
	}
	for body, want := range map[string]int{
		`{"path":"/api/v1/x","shape":"list","upserts":[{"id":"a","data":{"k":1}}]}`: http.StatusNoContent,
		`{"path":"/etc/passwd","shape":"list"}`:                                     http.StatusBadRequest,
		`{"path":"/api/v1/x","shape":"tree"}`:                                       http.StatusBadRequest,
		`{"path":"/api/v1/x","shape":"list","upserts":[{"id":"","data":1}]}`:        http.StatusBadRequest,
		`not json`: http.StatusBadRequest,
	} {
		if got := withDevice(http.MethodPost, reportsPath+"agents", body); got != want {
			t.Fatalf("POST %s = %d, want %d", body, got, want)
		}
	}
	if got := withDevice(http.MethodPost, reportsPath+"Bad Name", `{"path":"/api/v1/x","shape":"list"}`); got != http.StatusBadRequest {
		t.Fatalf("a bad collection name = %d", got)
	}
}

func TestSplitReport(t *testing.T) {
	shape, docs, order, err := splitReport([]byte(`[{"id":"a"},{"name":"n"},{"id":"a","v":2},{"id":7},{}]`))
	if err != nil || shape != core.DeviceReportList {
		t.Fatalf("shape %q, %v", shape, err)
	}
	// A repeated or missing id falls back to the position, so nothing is lost.
	if got := strings.Join(order, ","); got != "a,n,#2,7,#4" || len(docs) != 5 {
		t.Fatalf("order = %s, %d docs", got, len(docs))
	}
	for _, payload := range []string{`{"a":1}`, `[]`, `[1,2]`, `"text"`, `null`} {
		shape, docs, _, err := splitReport([]byte(payload))
		want := core.DeviceReportValue
		if payload == `[]` {
			want = core.DeviceReportList
		}
		if err != nil || shape != want {
			t.Fatalf("%s: shape %q, %v", payload, shape, err)
		}
		if want == core.DeviceReportValue {
			var roundTrip any
			if len(docs) != 1 || json.Unmarshal(docs[0].Data, &roundTrip) != nil {
				t.Fatalf("%s: docs = %+v", payload, docs)
			}
		}
	}
	if _, _, _, err := splitReport([]byte(`<html>`)); err == nil {
		t.Fatal("a non-JSON answer was reported")
	}
}
