package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

const (
	fleetLocalTargetID     = "local"
	fleetAllTargetID       = "all"
	fleetMaxTargets        = 64
	fleetMaxOperations     = 32
	fleetMaxRequestBytes   = 4 << 20
	fleetMaxResponseBytes  = 16 << 20
	fleetReadTargetTimeout = 20 * time.Second
)

type machineTarget struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Trusted bool   `json:"trusted"`
	Online  bool   `json:"online"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

type fleetOperation struct {
	Key    string          `json:"key"`
	Method string          `json:"method,omitempty"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body,omitempty"`
}

type fleetBatchRequest struct {
	TargetIDs []string         `json:"target_ids,omitempty"`
	Requests  []fleetOperation `json:"requests"`
}

type fleetOperationResult struct {
	Key        string          `json:"key"`
	Status     int             `json:"status"`
	OK         bool            `json:"ok"`
	Data       json.RawMessage `json:"data,omitempty"`
	Error      string          `json:"error,omitempty"`
	DurationMS int64           `json:"duration_ms"`
}

type fleetTargetResult struct {
	Target    machineTarget          `json:"target"`
	Responses []fleetOperationResult `json:"responses"`
}

type fleetBatchResponse struct {
	Targets []fleetTargetResult `json:"targets"`
}

func (s *Server) handleFleetTargets(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	targets := []machineTarget{{
		ID: fleetLocalTargetID, Name: "Local machine", Kind: fleetLocalTargetID,
		Trusted: true, Online: true, Version: s.version,
	}}
	if s.remote == nil {
		writeJSON(w, http.StatusOK, targets)
		return
	}

	views := s.remote.List()
	remoteTargets := make([]machineTarget, len(views))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for index, view := range views {
		index, view := index, view
		remoteTargets[index] = machineTarget{
			ID: view.ID, Name: view.Name, Kind: "ssh", Trusted: view.Trusted,
		}
		if !view.Trusted {
			remoteTargets[index].Error = "SSH host key is not trusted"
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			status, err := s.remote.Status(ctx, view.ID)
			if err != nil {
				remoteTargets[index].Error = err.Error()
				return
			}
			remoteTargets[index].Online = status.OK
			if status.Status != nil {
				if version, ok := status.Status["version"].(string); ok {
					remoteTargets[index].Version = version
				}
			}
		}()
	}
	wg.Wait()
	targets = append(targets, remoteTargets...)
	writeJSON(w, http.StatusOK, targets)
}

func (s *Server) handleFleetQuery(w http.ResponseWriter, r *http.Request) {
	s.handleFleetBatch(w, r, true)
}

func (s *Server) handleFleetExecute(w http.ResponseWriter, r *http.Request) {
	s.handleFleetBatch(w, r, false)
}

func (s *Server) handleFleetBatch(w http.ResponseWriter, r *http.Request, readOnly bool) {
	r.Body = http.MaxBytesReader(w, r.Body, fleetMaxRequestBytes)
	var input fleetBatchRequest
	if !decodeJSONInto(w, r, &input) {
		return
	}
	if len(input.Requests) == 0 || len(input.Requests) > fleetMaxOperations {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("requests must contain between 1 and %d operations", fleetMaxOperations))
		return
	}
	for index := range input.Requests {
		operation := &input.Requests[index]
		operation.Key = strings.TrimSpace(operation.Key)
		if operation.Key == "" {
			operation.Key = fmt.Sprintf("request-%d", index+1)
		}
		if operation.Method == "" {
			operation.Method = http.MethodGet
		}
		operation.Method = strings.ToUpper(strings.TrimSpace(operation.Method))
		if readOnly && operation.Method != http.MethodGet {
			writeErr(w, http.StatusBadRequest, "fleet query only accepts GET requests")
			return
		}
		if !fleetOperationAllowed(operation.Method, operation.Path, readOnly) {
			writeErr(w, http.StatusForbidden, "fleet operation is not allowed: "+operation.Method+" "+operation.Path)
			return
		}
	}

	targets, err := s.resolveFleetTargets(input.TargetIDs)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	results := make([]fleetTargetResult, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for index, target := range targets {
		index, target := index, target
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ctx := r.Context()
			cancel := func() {}
			if readOnly {
				ctx, cancel = context.WithTimeout(ctx, fleetReadTargetTimeout)
			}
			defer cancel()
			results[index] = s.runFleetOperations(ctx, target, input.Requests)
		}()
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, fleetBatchResponse{Targets: results})
}

func fleetOperationAllowed(method, rawPath string, readOnly bool) bool {
	parsed, err := url.ParseRequestURI(rawPath)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/api/v1/") {
		return false
	}
	blocked := []string{
		"/api/v1/remote/", "/api/v1/fleet-sync/", "/api/v1/console/",
		"/api/v1/observability/session", "/api/v1/observability/ingest",
		"/api/v1/observability/otlp/", "/api/v1/invocations",
	}
	for _, prefix := range blocked {
		if parsed.Path == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(parsed.Path, prefix) {
			return false
		}
	}
	if readOnly {
		return method == http.MethodGet
	}
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch:
		return true
	default:
		return false
	}
}

func (s *Server) resolveFleetTargets(ids []string) ([]machineTarget, error) {
	includeAll := len(ids) == 0
	for _, id := range ids {
		if strings.TrimSpace(id) == fleetAllTargetID {
			includeAll = true
			break
		}
	}
	requested := map[string]bool{}
	if !includeAll {
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if id != "" {
				requested[id] = true
			}
		}
	}
	targets := []machineTarget{}
	if includeAll || requested[fleetLocalTargetID] {
		targets = append(targets, machineTarget{
			ID: fleetLocalTargetID, Name: "Local machine", Kind: fleetLocalTargetID,
			Trusted: true, Online: true, Version: s.version,
		})
		delete(requested, fleetLocalTargetID)
	}
	if s.remote != nil {
		for _, host := range s.remote.List() {
			if !includeAll && !requested[host.ID] {
				continue
			}
			delete(requested, host.ID)
			if !host.Trusted {
				if includeAll {
					continue
				}
				return nil, fmt.Errorf("remote host %q is not trusted", host.Name)
			}
			targets = append(targets, machineTarget{ID: host.ID, Name: host.Name, Kind: "ssh", Trusted: true})
		}
	}
	if len(requested) > 0 {
		missing := make([]string, 0, len(requested))
		for id := range requested {
			missing = append(missing, id)
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("unknown fleet targets: %s", strings.Join(missing, ", "))
	}
	if len(targets) == 0 || len(targets) > fleetMaxTargets {
		return nil, fmt.Errorf("fleet target count must be between 1 and %d", fleetMaxTargets)
	}
	return targets, nil
}

func (s *Server) runFleetOperations(ctx context.Context, target machineTarget, operations []fleetOperation) fleetTargetResult {
	result := fleetTargetResult{Target: target, Responses: make([]fleetOperationResult, 0, len(operations))}
	for _, operation := range operations {
		if ctx.Err() != nil {
			result.Responses = append(result.Responses, fleetOperationResult{
				Key: operation.Key, Status: http.StatusGatewayTimeout, Error: ctx.Err().Error(),
			})
			continue
		}
		started := time.Now()
		status, payload, err := s.executeFleetOperation(ctx, target.ID, operation)
		item := fleetOperationResult{
			Key: operation.Key, Status: status, OK: err == nil && status >= 200 && status < 300,
			DurationMS: time.Since(started).Milliseconds(),
		}
		if err != nil {
			item.Error = err.Error()
		} else if item.OK {
			trimmed := bytes.TrimSpace(payload)
			if len(trimmed) == 0 || !json.Valid(trimmed) {
				item.OK = false
				item.Status = http.StatusBadGateway
				item.Error = "target returned an empty or invalid JSON response"
			} else {
				item.Data = json.RawMessage(trimmed)
			}
		} else {
			item.Error = fleetErrorMessage(payload, status)
		}
		result.Responses = append(result.Responses, item)
	}
	return result
}

func (s *Server) executeFleetOperation(ctx context.Context, targetID string, operation fleetOperation) (int, []byte, error) {
	if targetID == fleetLocalTargetID {
		return s.executeLocalFleetOperation(ctx, operation)
	}
	return s.executeRemoteFleetOperation(ctx, targetID, operation)
}

func (s *Server) executeLocalFleetOperation(ctx context.Context, operation fleetOperation) (int, []byte, error) {
	var body io.Reader
	if len(operation.Body) > 0 {
		body = bytes.NewReader(operation.Body)
	}
	request := httptest.NewRequestWithContext(ctx, operation.Method, operation.Path, body)
	request = request.WithContext(withPrincipal(request.Context(), core.AdminPrincipal()))
	request.Header.Set("X-AgentMux-Console", "1")
	if strings.HasPrefix(operation.Path, "/api/v1/fleet-sync/") {
		request.Header.Set("X-AgentMux-Fleet-Peer", "1")
	}
	if len(operation.Body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	s.mux.ServeHTTP(recorder, request)
	responseLimit := int64(fleetMaxResponseBytes)
	if strings.HasPrefix(operation.Path, "/api/v1/fleet-sync/") {
		responseLimit = fleetSyncMaxBodyBytes
	}
	payload, err := io.ReadAll(io.LimitReader(recorder.Result().Body, responseLimit+1))
	if err != nil {
		return 0, nil, err
	}
	if int64(len(payload)) > responseLimit {
		return 0, nil, fmt.Errorf("local fleet response exceeds %d MiB", responseLimit>>20)
	}
	return recorder.Code, payload, nil
}

func (s *Server) executeRemoteFleetOperation(ctx context.Context, targetID string, operation fleetOperation) (int, []byte, error) {
	if s.remote == nil {
		return 0, nil, fmt.Errorf("remote SSH control unavailable")
	}
	host, ok := s.remote.Get(targetID)
	if !ok {
		return 0, nil, fmt.Errorf("remote host not found")
	}
	var body io.Reader
	if len(operation.Body) > 0 {
		body = bytes.NewReader(operation.Body)
	}
	request, err := http.NewRequestWithContext(ctx, operation.Method, "http://"+host.RemoteAddr+operation.Path, body)
	if err != nil {
		return 0, nil, err
	}
	if len(operation.Body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	if host.APIToken != "" {
		request.Header.Set("Authorization", "Bearer "+host.APIToken)
	}
	request.Header.Set("X-AgentMux-SSH-Host", host.Name)
	if strings.HasPrefix(operation.Path, "/api/v1/fleet-sync/") {
		request.Header.Set("X-AgentMux-Fleet-Peer", "1")
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return s.remote.DialContext(ctx, targetID, network)
		},
		DisableKeepAlives: true,
	}
	defer transport.CloseIdleConnections()
	response, err := transport.RoundTrip(request)
	if err != nil {
		return 0, nil, fmt.Errorf("remote %s: %w", host.Name, err)
	}
	defer response.Body.Close()
	responseLimit := int64(fleetMaxResponseBytes)
	if strings.HasPrefix(operation.Path, "/api/v1/fleet-sync/") {
		responseLimit = fleetSyncMaxBodyBytes
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if err != nil {
		return 0, nil, err
	}
	if int64(len(payload)) > responseLimit {
		return 0, nil, fmt.Errorf("remote %s response exceeds %d MiB", host.Name, responseLimit>>20)
	}
	return response.StatusCode, payload, nil
}

func fleetErrorMessage(payload []byte, status int) string {
	var envelope struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(payload, &envelope) == nil && strings.TrimSpace(envelope.Error) != "" {
		return strings.TrimSpace(envelope.Error)
	}
	message := strings.TrimSpace(string(payload))
	if message != "" && len(message) <= 4096 {
		return message
	}
	return http.StatusText(status)
}
