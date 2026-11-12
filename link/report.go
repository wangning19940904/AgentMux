package link

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/wangning19940904/AgentMux/core"
)

// Reporting.
//
// A node keeps its own database. So that the hub can show a node's data while
// the node is offline, and one place can answer for every node, the node also
// sends the hub the answers its own API gives to a fixed set of read-only
// requests. It sends only what changed since the hub's copy, down the link it
// already holds: the hub opens streams to reach the node, the node opens
// streams to report.

const (
	reportsPath = "/uplink/v1/reports/"

	// ReportInterval is how often a node looks for changes to report.
	ReportInterval = 30 * time.Second
	// A collection with no changes is still confirmed this often, so the hub
	// can tell "unchanged" from "the node stopped reporting".
	reportConfirmInterval = 2 * time.Minute

	maxReportBytes = 8 << 20
	maxReportDocs  = 5000
	valueDocID     = "_"
)

var collectionName = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

// Report names one request to the node's own API whose answer the hub keeps.
type Report struct {
	Collection string
	Path       string
}

// DefaultReports are the lists the Console shows for every machine. Each is
// what the node's API returns to its administrator, with secrets already
// redacted the way that API redacts them for display.
var DefaultReports = []Report{
	{"status", "/api/v1/status"},
	{"agents", "/api/v1/agent-instances"},
	{"channels", "/api/v1/channels"},
	{"triggers", "/api/v1/triggers"},
	{"super_agents", "/api/v1/super-agents"},
	{"orchestrations_active", "/api/v1/orchestrations?active=true"},
	{"orchestrations", "/api/v1/orchestrations?active=false"},
	{"skills", "/api/v1/skills"},
	{"mcp", "/api/v1/mcp"},
	{"guard_policies", "/api/v1/guard/policies"},
	{"providers", "/api/v1/providers"},
	{"provider_routes", "/api/v1/providers/active"},
	{"platforms", "/api/v1/platforms"},
	{"runtimes", "/api/v1/agents"},
	{"tenants", "/api/v1/tenancy/tenants"},
	// Agents waiting for a person; the hub notifies from its copy.
	{"interactions_pending", "/api/v1/channel-interactions?pending=true"},
}

// ReportSink is where the hub keeps what devices report.
type ReportSink interface {
	// DeviceReportState returns the hash of each document the hub holds.
	DeviceReportState(ctx context.Context, deviceID, collection string) (map[string]string, error)
	ApplyDeviceReport(ctx context.Context, deviceID string, report core.DeviceReport, at time.Time) error
}

type deviceContextKey struct{}

// DeviceID returns the device whose link a request arrived on. The link
// authenticated it, so a handler needs no further credential.
func DeviceID(ctx context.Context) string {
	id, _ := ctx.Value(deviceContextKey{}).(string)
	return id
}

// NewReportHandler serves the requests nodes send up their links.
func NewReportHandler(sink ReportSink, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	mux := http.NewServeMux()
	collection := func(w http.ResponseWriter, r *http.Request) (deviceID, name string, ok bool) {
		deviceID, name = DeviceID(r.Context()), r.PathValue("collection")
		if deviceID == "" {
			writeLinkError(w, http.StatusUnauthorized, "reports are accepted only over a device link")
			return "", "", false
		}
		if !collectionName.MatchString(name) {
			writeLinkError(w, http.StatusBadRequest, "invalid collection name")
			return "", "", false
		}
		return deviceID, name, true
	}
	mux.HandleFunc("GET "+reportsPath+"{collection}", func(w http.ResponseWriter, r *http.Request) {
		deviceID, name, ok := collection(w, r)
		if !ok {
			return
		}
		items, err := sink.DeviceReportState(r.Context(), deviceID, name)
		if err != nil {
			log.Warn("read device report state failed", "device", deviceID, "collection", name, "err", err)
			writeLinkError(w, http.StatusInternalServerError, "the hub could not read its copy")
			return
		}
		if items == nil {
			items = map[string]string{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
	})
	mux.HandleFunc("POST "+reportsPath+"{collection}", func(w http.ResponseWriter, r *http.Request) {
		deviceID, name, ok := collection(w, r)
		if !ok {
			return
		}
		var report core.DeviceReport
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxReportBytes+(1<<20))).Decode(&report); err != nil {
			writeLinkError(w, http.StatusBadRequest, "invalid report")
			return
		}
		report.Collection = name
		if err := validateReport(report); err != nil {
			writeLinkError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := sink.ApplyDeviceReport(r.Context(), deviceID, report, time.Now()); err != nil {
			log.Warn("store device report failed", "device", deviceID, "collection", name, "err", err)
			writeLinkError(w, http.StatusInternalServerError, "the hub could not store the report")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func validateReport(report core.DeviceReport) error {
	if !strings.HasPrefix(report.Path, "/api/") || len(report.Path) > 512 {
		return errors.New("a report names an /api/ path")
	}
	if report.Shape != core.DeviceReportList && report.Shape != core.DeviceReportValue {
		return errors.New("unknown report shape")
	}
	if len(report.Upserts) > maxReportDocs || len(report.Deletes) > maxReportDocs || len(report.Order) > maxReportDocs {
		return fmt.Errorf("a collection holds at most %d documents", maxReportDocs)
	}
	for _, doc := range report.Upserts {
		if doc.ID == "" || len(doc.ID) > 256 || !json.Valid(doc.Data) {
			return errors.New("a document needs an id and JSON data")
		}
	}
	return nil
}

// ReportStatus is a node's account of one collection it reports.
type ReportStatus struct {
	Collection string     `json:"collection"`
	Items      int        `json:"items"`
	ReportedAt *time.Time `json:"reported_at,omitempty"`
	Error      string     `json:"error,omitempty"`
}

// reportState is what the node believes the hub holds of one collection.
// known is nil until the node has asked, and again after any failed send, so
// the next round starts from the hub's real copy rather than a guess.
type reportState struct {
	known  map[string]string
	order  []string
	shape  string
	posted time.Time
}

// reporter sends one node's reports for the life of one connection.
type reporter struct {
	client   *Client
	uplink   *http.Client
	interval time.Duration
	states   map[string]*reportState
}

func (c *Client) reports() []Report {
	if c.Reports == nil {
		return DefaultReports
	}
	return c.Reports
}

// report runs until ctx ends, which is when the connection does.
func (c *Client) report(ctx context.Context, session *yamux.Session) {
	reports := c.reports()
	if len(reports) == 0 {
		return
	}
	interval := c.ReportInterval
	if interval <= 0 {
		interval = ReportInterval
	}
	r := &reporter{
		client: c, interval: interval, states: map[string]*reportState{},
		uplink: &http.Client{Timeout: time.Minute, Transport: &http.Transport{
			Proxy: nil, DisableKeepAlives: true,
			DialContext: func(context.Context, string, string) (net.Conn, error) { return session.Open() },
		}},
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		for _, report := range reports {
			if ctx.Err() != nil {
				return
			}
			r.sync(ctx, report)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *reporter) sync(ctx context.Context, report Report) {
	status := ReportStatus{Collection: report.Collection}
	items, at, err := r.syncOnce(ctx, report)
	status.Items = items
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		status.Error = err.Error()
		// The hub's copy is no longer known; ask again next round.
		delete(r.states, report.Collection)
		r.client.Log.Debug("report not sent", "collection", report.Collection, "err", err)
	}
	if !at.IsZero() {
		status.ReportedAt = &at
	}
	r.client.setReportStatus(status)
}

func (r *reporter) syncOnce(ctx context.Context, report Report) (int, time.Time, error) {
	payload, err := r.client.fetchLocal(ctx, report.Path)
	if err != nil {
		return 0, time.Time{}, err
	}
	shape, docs, order, err := splitReport(payload)
	if err != nil {
		return 0, time.Time{}, err
	}
	state := r.states[report.Collection]
	first := state == nil
	if first {
		known, err := r.hubState(ctx, report.Collection)
		if err != nil {
			return len(docs), time.Time{}, err
		}
		state = &reportState{known: known}
	}
	batch := core.DeviceReport{Path: report.Path, Shape: shape}
	current := make(map[string]string, len(docs))
	for _, doc := range docs {
		hash := core.DeviceReportHash(doc.Data)
		current[doc.ID] = hash
		if state.known[doc.ID] != hash {
			batch.Upserts = append(batch.Upserts, doc)
		}
	}
	for id := range state.known {
		if _, ok := current[id]; !ok {
			batch.Deletes = append(batch.Deletes, id)
		}
	}
	if first || shape != state.shape || !slices.Equal(order, state.order) {
		batch.Order = order
		if batch.Order == nil {
			batch.Order = []string{}
		}
	}
	changed := len(batch.Upserts) > 0 || len(batch.Deletes) > 0 || batch.Order != nil
	if !changed && time.Since(state.posted) < max(reportConfirmInterval, r.interval) {
		return len(docs), state.posted, nil
	}
	if err := r.post(ctx, report.Collection, batch); err != nil {
		return len(docs), time.Time{}, err
	}
	state.known, state.order, state.shape, state.posted = current, order, shape, time.Now()
	r.states[report.Collection] = state
	return len(docs), state.posted, nil
}

func (r *reporter) hubState(ctx context.Context, collection string) (map[string]string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://hub"+reportsPath+collection, nil)
	if err != nil {
		return nil, err
	}
	response, err := r.uplink.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, maxReportBytes))
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the hub answered %d: %s", response.StatusCode, linkErrorMessage(data, response.StatusCode))
	}
	var state struct {
		Items map[string]string `json:"items"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	if state.Items == nil {
		state.Items = map[string]string{}
	}
	return state.Items, nil
}

func (r *reporter) post(ctx context.Context, collection string, batch core.DeviceReport) error {
	body, err := json.Marshal(batch)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://hub"+reportsPath+collection, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := r.uplink.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		data, _ := io.ReadAll(io.LimitReader(response.Body, pairBodyLimit))
		return fmt.Errorf("the hub answered %d: %s", response.StatusCode, linkErrorMessage(data, response.StatusCode))
	}
	return nil
}

// splitReport turns an API answer into documents. An array of objects becomes
// one document per element, named by its "id" (or "name") so an element keeps
// its identity when the list is reordered; anything else is one value.
func splitReport(payload []byte) (shape string, docs []core.DeviceReportDoc, order []string, err error) {
	payload = bytes.TrimSpace(payload)
	if !json.Valid(payload) {
		return "", nil, nil, errors.New("the API did not answer with JSON")
	}
	value := func() (string, []core.DeviceReportDoc, []string, error) {
		return core.DeviceReportValue, []core.DeviceReportDoc{{ID: valueDocID, Data: payload}}, nil, nil
	}
	if len(payload) == 0 || payload[0] != '[' {
		return value()
	}
	var elements []json.RawMessage
	if err := json.Unmarshal(payload, &elements); err != nil {
		return "", nil, nil, err
	}
	if len(elements) > maxReportDocs {
		return "", nil, nil, fmt.Errorf("the list has %d items; at most %d are reported", len(elements), maxReportDocs)
	}
	seen := make(map[string]bool, len(elements))
	for index, element := range elements {
		var keys struct {
			ID   json.RawMessage `json:"id"`
			Name json.RawMessage `json:"name"`
		}
		trimmed := bytes.TrimSpace(element)
		if len(trimmed) == 0 || trimmed[0] != '{' || json.Unmarshal(trimmed, &keys) != nil {
			// A list of plain values has no element identity to keep.
			return value()
		}
		id := scalarID(keys.ID)
		if id == "" {
			id = scalarID(keys.Name)
		}
		if id == "" || seen[id] {
			id = fmt.Sprintf("#%d", index)
		}
		seen[id] = true
		var compact bytes.Buffer
		if err := json.Compact(&compact, trimmed); err != nil {
			return "", nil, nil, err
		}
		docs = append(docs, core.DeviceReportDoc{ID: id, Data: compact.Bytes()})
		order = append(order, id)
	}
	return core.DeviceReportList, docs, order, nil
}

func scalarID(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if len(text) > 200 {
			return ""
		}
		return text
	}
	var number json.Number
	if len(raw) > 0 && json.Unmarshal(raw, &number) == nil {
		return number.String()
	}
	return ""
}

// fetchLocal asks this node's own API, the way the hub's requests reach it.
func (c *Client) fetchLocal(ctx context.Context, path string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://node"+path, nil)
	if err != nil {
		return nil, err
	}
	recorder := &localResponse{header: http.Header{}}
	c.Handler.ServeHTTP(recorder, request)
	if recorder.status != http.StatusOK && recorder.status != 0 {
		return nil, fmt.Errorf("this machine's API answered %d", recorder.status)
	}
	if recorder.overflow {
		return nil, fmt.Errorf("the answer exceeds %d MiB", maxReportBytes>>20)
	}
	if kind := recorder.header.Get("Content-Type"); !strings.HasPrefix(kind, "application/json") {
		// An AgentMux from before this endpoint existed answers with its
		// Console page instead.
		return nil, errors.New("this machine's AgentMux does not offer this yet")
	}
	return recorder.body.Bytes(), nil
}

// localResponse collects a handler's answer in memory, up to the report limit.
type localResponse struct {
	header   http.Header
	status   int
	body     bytes.Buffer
	overflow bool
}

func (l *localResponse) Header() http.Header { return l.header }
func (l *localResponse) WriteHeader(status int) {
	if l.status == 0 {
		l.status = status
	}
}
func (l *localResponse) Write(data []byte) (int, error) {
	if l.status == 0 {
		l.status = http.StatusOK
	}
	if l.body.Len()+len(data) > maxReportBytes {
		l.overflow = true
		return len(data), nil
	}
	return l.body.Write(data)
}
func (l *localResponse) Flush() {}

// reportStatuses guards the node's account of its reports.
type reportStatuses struct {
	mu    sync.Mutex
	items map[string]ReportStatus
}

func (c *Client) setReportStatus(status ReportStatus) {
	c.reportStatus.mu.Lock()
	defer c.reportStatus.mu.Unlock()
	if c.reportStatus.items == nil {
		c.reportStatus.items = map[string]ReportStatus{}
	}
	// An error keeps the time of the last report that did get through.
	if previous, ok := c.reportStatus.items[status.Collection]; ok && status.ReportedAt == nil {
		status.ReportedAt = previous.ReportedAt
	}
	c.reportStatus.items[status.Collection] = status
}

func (c *Client) reportStatusList() []ReportStatus {
	c.reportStatus.mu.Lock()
	defer c.reportStatus.mu.Unlock()
	out := make([]ReportStatus, 0, len(c.reportStatus.items))
	for _, report := range c.reports() {
		if status, ok := c.reportStatus.items[report.Collection]; ok {
			out = append(out, status)
		}
	}
	return out
}
