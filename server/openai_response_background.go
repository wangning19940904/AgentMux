package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

type openAIStoredResponse struct {
	request  openAIResponseRequest
	identity openAIResponseIdentity
	response openAIResponseObject
	cancel   context.CancelFunc
	stream   bool
	events   []openAIRecordedEvent
	notify   chan struct{}
}

type openAIRecordedEvent struct {
	sequence  int
	eventType string
	payload   []byte
}

type openAIResponseRegistry struct {
	mu      sync.RWMutex
	records map[string]*openAIStoredResponse
	deleted map[string]bool
}

func newOpenAIResponseRegistry() *openAIResponseRegistry {
	return &openAIResponseRegistry{records: map[string]*openAIStoredResponse{}, deleted: map[string]bool{}}
}

func (r *openAIResponseRegistry) upsert(record openAIStoredResponse) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.deleted[record.identity.responseID] {
		r.mu.Unlock()
		return
	}
	if existing := r.records[record.identity.responseID]; existing != nil {
		statusChanged := existing.response.Status != record.response.Status
		if record.cancel == nil {
			record.cancel = existing.cancel
		}
		record.stream = record.stream || existing.stream
		record.events = existing.events
		record.notify = existing.notify
		if statusChanged && record.notify != nil {
			close(record.notify)
			record.notify = make(chan struct{})
		}
	}
	if record.notify == nil {
		record.notify = make(chan struct{})
	}
	copy := record
	r.records[record.identity.responseID] = &copy
	r.mu.Unlock()
}

func (r *openAIResponseRegistry) appendEvent(id string, event openAIRecordedEvent) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	record := r.records[id]
	if record == nil {
		r.mu.Unlock()
		return false
	}
	record.events = append(record.events, event)
	if record.notify != nil {
		close(record.notify)
	}
	record.notify = make(chan struct{})
	r.mu.Unlock()
	return true
}

func (r *openAIResponseRegistry) get(id string) (openAIStoredResponse, bool) {
	if r == nil {
		return openAIStoredResponse{}, false
	}
	r.mu.RLock()
	record := r.records[id]
	if record == nil {
		r.mu.RUnlock()
		return openAIStoredResponse{}, false
	}
	copy := *record
	r.mu.RUnlock()
	return copy, true
}

func (r *openAIResponseRegistry) delete(id string) (openAIStoredResponse, bool) {
	if r == nil {
		return openAIStoredResponse{}, false
	}
	r.mu.Lock()
	record := r.records[id]
	if record != nil {
		delete(r.records, id)
		r.deleted[id] = true
		if record.notify != nil {
			close(record.notify)
			record.notify = nil
		}
	}
	r.mu.Unlock()
	if record == nil {
		return openAIStoredResponse{}, false
	}
	return *record, true
}

func openAIRequestStoresResponse(req openAIResponseRequest) bool {
	return req.Background || req.Store == nil || *req.Store
}

func (s *Server) storeOpenAIResponse(req openAIResponseRequest, identity openAIResponseIdentity, response openAIResponseObject, cancel context.CancelFunc) {
	if s == nil || !openAIRequestStoresResponse(req) {
		return
	}
	if s.openAIResponses == nil {
		s.openAIResponses = newOpenAIResponseRegistry()
	}
	s.openAIResponses.upsert(openAIStoredResponse{
		request: req, identity: identity, response: response, cancel: cancel, stream: req.Background && req.Stream,
	})
}

func (s *Server) handleOpenAIBackgroundResponse(w http.ResponseWriter, _ *http.Request, req openAIResponseRequest, identity openAIResponseIdentity, invocation core.InvocationRequest) {
	ctx, cancel := context.WithCancel(context.Background())
	queued := buildOpenAIResponse(req, identity, "queued", "", nil, nil)
	s.storeOpenAIResponse(req, identity, queued, cancel)
	writeJSON(w, http.StatusOK, queued)

	go func() {
		inProgress := buildOpenAIResponse(req, identity, "in_progress", "", nil, nil)
		s.storeOpenAIResponse(req, identity, inProgress, cancel)
		result, err := invokeOpenAIResponse(ctx, s.invoker, invocation)
		if err != nil {
			status := "failed"
			code := "agent_error"
			if errors.Is(err, context.Canceled) {
				status = "cancelled"
				code = "response_cancelled"
			}
			detail := openAIErrorDetail{Message: err.Error(), Type: "server_error", Param: nil, Code: code}
			failed := buildOpenAIResponse(req, identity, status, "", result.Usage, detail)
			s.storeOpenAIResponse(req, identity, failed, nil)
			return
		}
		if err := prepareOpenAIFinalOutput(&req, &result); err != nil {
			detail := openAIErrorDetail{Message: err.Error(), Type: "server_error", Param: "text.format", Code: "output_validation_failed"}
			failed := buildOpenAIResponse(req, identity, "failed", "", result.Usage, detail)
			s.storeOpenAIResponse(req, identity, failed, nil)
			return
		}
		completed := buildOpenAIResponse(req, identity, "completed", result.Answer, result.Usage, nil)
		s.storeOpenAIResponse(req, identity, completed, nil)
	}()
}

func (s *Server) handleOpenAIBackgroundResponseStream(w http.ResponseWriter, r *http.Request, req openAIResponseRequest, identity openAIResponseIdentity, invocation core.InvocationRequest, startingAfter int) {
	streamer, ok := s.invoker.(core.StreamingInvoker)
	if !ok {
		writeOpenAIError(w, http.StatusServiceUnavailable, "streaming invocation runtime unavailable", "server_error", nil, "streaming_unavailable")
		return
	}
	if _, ok := w.(http.Flusher); !ok {
		writeOpenAIError(w, http.StatusInternalServerError, "streaming is unsupported by this HTTP server", "server_error", nil, "streaming_unavailable")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	queued := buildOpenAIResponse(req, identity, "queued", "", nil, nil)
	s.storeOpenAIResponse(req, identity, queued, cancel)
	go s.runOpenAIBackgroundStream(ctx, streamer, req, identity, invocation)
	s.serveOpenAIRecordedEvents(w, r, identity.responseID, startingAfter)
}

func (s *Server) runOpenAIBackgroundStream(ctx context.Context, streamer core.StreamingInvoker, req openAIResponseRequest, identity openAIResponseIdentity, invocation core.InvocationRequest) {
	inProgress := buildOpenAIResponse(req, identity, "in_progress", "", nil, nil)
	s.storeOpenAIResponse(req, identity, inProgress, nil)
	state := &openAIResponseStreamState{
		server: s, req: req, identity: identity,
		record: func(event openAIRecordedEvent) {
			s.openAIResponses.appendEvent(identity.responseID, event)
		},
	}
	if err := state.start(); err != nil {
		_ = state.fail(err)
		return
	}
	result, err := invokeOpenAIResponseStream(ctx, streamer, invocation, func(event core.InvocationStreamEvent) error {
		return state.consume(event)
	})
	if state.terminal {
		return
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			detail := openAIErrorDetail{Message: err.Error(), Type: "server_error", Param: nil, Code: "response_cancelled"}
			cancelled := buildOpenAIResponse(req, identity, "cancelled", "", result.Usage, detail)
			s.storeOpenAIResponse(req, identity, cancelled, nil)
			return
		}
		_ = state.fail(err)
		return
	}
	_ = state.complete(result)
}

func (s *Server) serveOpenAIRecordedEvents(w http.ResponseWriter, r *http.Request, responseID string, startingAfter int) {
	flusher := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	cursor := startingAfter
	for {
		record, ok := s.openAIResponses.get(responseID)
		if !ok {
			return
		}
		for _, event := range record.events {
			if event.sequence <= cursor {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.eventType, event.payload); err != nil {
				return
			}
			cursor = event.sequence
		}
		flusher.Flush()
		if openAIResponseTerminal(record.response.Status) && (len(record.events) == 0 || cursor >= record.events[len(record.events)-1].sequence) {
			return
		}
		notify := record.notify
		select {
		case <-notify:
		case <-keepalive.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func openAIResponseTerminal(status string) bool {
	switch status {
	case "completed", "failed", "cancelled", "incomplete":
		return true
	default:
		return false
	}
}

func (s *Server) handleOpenAIResponseGet(w http.ResponseWriter, r *http.Request) {
	record, ok := s.openAIResponses.get(r.PathValue("response_id"))
	if !ok {
		writeOpenAIError(w, http.StatusNotFound, "response not found", "invalid_request_error", "response_id", "response_not_found")
		return
	}
	if r.URL.Query().Get("stream") == "true" {
		if !record.stream {
			writeOpenAIError(w, http.StatusBadRequest, "response was not created with background=true and stream=true", "invalid_request_error", "stream", "stream_not_available")
			return
		}
		if _, ok := w.(http.Flusher); !ok {
			writeOpenAIError(w, http.StatusInternalServerError, "streaming is unsupported by this HTTP server", "server_error", nil, "streaming_unavailable")
			return
		}
		startingAfter := -1
		if value := r.URL.Query().Get("starting_after"); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 0 {
				writeOpenAIError(w, http.StatusBadRequest, "starting_after must be a non-negative sequence number", "invalid_request_error", "starting_after", "invalid_cursor")
				return
			}
			startingAfter = parsed
		}
		s.serveOpenAIRecordedEvents(w, r, record.identity.responseID, startingAfter)
		return
	}
	writeJSON(w, http.StatusOK, record.response)
}

func (s *Server) handleOpenAIResponseCancel(w http.ResponseWriter, r *http.Request) {
	record, ok := s.openAIResponses.get(r.PathValue("response_id"))
	if !ok {
		writeOpenAIError(w, http.StatusNotFound, "response not found", "invalid_request_error", "response_id", "response_not_found")
		return
	}
	if record.response.Status != "queued" && record.response.Status != "in_progress" {
		writeJSON(w, http.StatusOK, record.response)
		return
	}
	if record.cancel != nil {
		record.cancel()
	}
	detail := openAIErrorDetail{Message: "response cancelled", Type: "server_error", Param: nil, Code: "response_cancelled"}
	cancelled := buildOpenAIResponse(record.request, record.identity, "cancelled", "", nil, detail)
	s.storeOpenAIResponse(record.request, record.identity, cancelled, nil)
	writeJSON(w, http.StatusOK, cancelled)
}

func (s *Server) handleOpenAIResponseDelete(w http.ResponseWriter, r *http.Request) {
	record, ok := s.openAIResponses.delete(r.PathValue("response_id"))
	if !ok {
		writeOpenAIError(w, http.StatusNotFound, "response not found", "invalid_request_error", "response_id", "response_not_found")
		return
	}
	if record.cancel != nil && (record.response.Status == "queued" || record.response.Status == "in_progress") {
		record.cancel()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": record.identity.responseID, "object": "response.deleted", "deleted": true,
	})
}

func (s *Server) handleOpenAIResponseInputItems(w http.ResponseWriter, r *http.Request) {
	record, ok := s.openAIResponses.get(r.PathValue("response_id"))
	if !ok {
		writeOpenAIError(w, http.StatusNotFound, "response not found", "invalid_request_error", "response_id", "response_not_found")
		return
	}
	var data []any
	if len(record.request.Input) > 0 {
		var list []any
		if json.Unmarshal(record.request.Input, &list) == nil {
			data = list
		} else {
			var text string
			if json.Unmarshal(record.request.Input, &text) == nil {
				data = []any{map[string]any{
					"id": "msg_" + randHex(16), "type": "message", "role": "user",
					"status": "completed", "content": []any{map[string]any{"type": "input_text", "text": text}},
				}}
			}
		}
	}
	if data == nil {
		data = []any{}
	}
	var firstID, lastID any
	if len(data) > 0 {
		if first, ok := data[0].(map[string]any); ok {
			firstID = first["id"]
		}
		if last, ok := data[len(data)-1].(map[string]any); ok {
			lastID = last["id"]
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list", "data": data, "first_id": firstID, "last_id": lastID, "has_more": false,
	})
}
