package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
	nativeintegration "github.com/wangning19940904/AgentMux/integrations/native"
	observationpkg "github.com/wangning19940904/AgentMux/observability"
	"github.com/wangning19940904/AgentMux/store"
)

const observabilitySessionCookie = "agentmux_observability_session"

type observabilityRuntime struct {
	config   config.ObservabilityConfig
	recorder *store.ObservationRecorder
	insights *observationpkg.InsightEngine
	native   *nativeintegration.Manager
	ingest   *observationpkg.IngestService

	mu       sync.Mutex
	nonces   map[string]time.Time
	sessions map[string]time.Time
}

// SetObservability attaches secure recording support, advisory insights,
// native plugin management and hook/OTLP ingest to the management API.
func (s *Server) SetObservability(cfg config.ObservabilityConfig, recorder *store.ObservationRecorder, insights *observationpkg.InsightEngine, native *nativeintegration.Manager, ingest *observationpkg.IngestService) {
	s.obs = &observabilityRuntime{
		config: cfg, recorder: recorder, insights: insights, native: native, ingest: ingest,
		nonces: map[string]time.Time{}, sessions: map[string]time.Time{},
	}
}

func (s *Server) registerObservabilityRoutes() {
	s.mux.HandleFunc("GET /api/v1/observability/session/nonce", s.handleObservationNonce)
	s.mux.HandleFunc("POST /api/v1/observability/session", s.handleObservationSession)
	s.mux.HandleFunc("POST /api/v1/observability/ingest", s.handleObservationIngest)
	s.mux.HandleFunc("POST /api/v1/observability/otlp/v1/traces", s.handleObservationOTLPTraces)
	s.mux.HandleFunc("GET /api/v1/observability/overview", s.handleObservationOverview)
	s.mux.HandleFunc("GET /api/v1/observability/traces", s.handleObservationTraces)
	s.mux.HandleFunc("GET /api/v1/observability/traces/{trace_id}", s.handleObservationTrace)
	s.mux.HandleFunc("GET /api/v1/observability/insights", s.handleObservationInsights)
	s.mux.HandleFunc("GET /api/v1/observability/settings", s.handleObservationSettings)
	s.mux.HandleFunc("GET /api/v1/observability/integrations", s.handleObservationIntegrations)
	s.mux.HandleFunc("POST /api/v1/observability/integrations/{host}/{action}", s.handleObservationIntegrationAction)
}

func isObservabilityPath(path string) bool {
	return strings.HasPrefix(path, "/api/v1/observability/") || path == "/api/v1/observability"
}

func (s *Server) applyObservabilityCORS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin != "" {
		parsed, err := url.Parse(origin)
		if err == nil && (strings.EqualFold(parsed.Host, r.Host) || isObservationDesktopOrigin(parsed)) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-AgentMux-Token, X-AgentMux-Desktop")
}

func isObservationDesktopOrigin(parsed *url.URL) bool {
	if parsed == nil || !strings.EqualFold(parsed.Hostname(), "wails.localhost") {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "wails", "http", "https":
		return true
	default:
		return false
	}
}

func (s *Server) authorizeObservabilityRequest(w http.ResponseWriter, r *http.Request) bool {
	path := r.URL.Path
	if path == "/api/v1/observability/session/nonce" || path == "/api/v1/observability/session" || path == "/api/v1/observability/ingest" || strings.HasPrefix(path, "/api/v1/observability/otlp/") {
		return true
	}
	if s.obs == nil {
		http.Error(w, "observability is not configured", http.StatusServiceUnavailable)
		return false
	}
	if s.cfg.Bridge.Enabled && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.cfg.Bridge.Token)) == 1 {
		return true
	}
	if bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); bearer != "" {
		s.obs.mu.Lock()
		expires, ok := s.obs.sessions[bearer]
		if ok && time.Now().After(expires) {
			delete(s.obs.sessions, bearer)
			ok = false
		}
		s.obs.mu.Unlock()
		if ok {
			return true
		}
	}
	cookie, err := r.Cookie(observabilitySessionCookie)
	if err != nil {
		http.Error(w, "observability session required", http.StatusUnauthorized)
		return false
	}
	s.obs.mu.Lock()
	expires, ok := s.obs.sessions[cookie.Value]
	if ok && time.Now().After(expires) {
		delete(s.obs.sessions, cookie.Value)
		ok = false
	}
	s.obs.mu.Unlock()
	if !ok {
		http.Error(w, "observability session expired", http.StatusUnauthorized)
		return false
	}
	return true
}

func (s *Server) handleObservationNonce(w http.ResponseWriter, r *http.Request) {
	if s.obs == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "observability is not configured"})
		return
	}
	if !requestIsLoopback(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "observability console session is loopback-only"})
		return
	}
	nonce := randomObservationCredential()
	expires := time.Now().Add(2 * time.Minute)
	s.obs.mu.Lock()
	s.obs.nonces[nonce] = expires
	for value, expiry := range s.obs.nonces {
		if time.Now().After(expiry) {
			delete(s.obs.nonces, value)
		}
	}
	s.obs.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"nonce": nonce, "expires_at": expires.UTC()})
}

func (s *Server) handleObservationSession(w http.ResponseWriter, r *http.Request) {
	if s.obs == nil || !requestIsLoopback(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "observability console session is unavailable"})
		return
	}
	var request struct {
		Nonce string `json:"nonce"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	now := time.Now()
	s.obs.mu.Lock()
	expires, ok := s.obs.nonces[request.Nonce]
	delete(s.obs.nonces, request.Nonce)
	if !ok || now.After(expires) {
		s.obs.mu.Unlock()
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or expired nonce"})
		return
	}
	session := randomObservationCredential()
	sessionExpires := now.Add(12 * time.Hour)
	s.obs.sessions[session] = sessionExpires
	s.obs.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: observabilitySessionCookie, Value: session, Path: "/api/v1/observability/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Expires: sessionExpires,
	})
	response := map[string]any{"ok": true, "expires_at": sessionExpires.UTC()}
	if r.Header.Get("X-AgentMux-Desktop") == "1" {
		if origin, err := url.Parse(r.Header.Get("Origin")); err == nil && isObservationDesktopOrigin(origin) {
			// A Wails WebView cannot send a SameSite cookie to the loopback HTTP
			// daemon. Return the same short-lived session as a memory-only bearer
			// exclusively to the allow-listed native origin.
			response["session_token"] = session
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func requestIsLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if host == "" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func randomObservationCredential() string {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(value)
}

func (s *Server) handleObservationIngest(w http.ResponseWriter, r *http.Request) {
	if s.obs == nil || s.obs.ingest == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "observation ingest unavailable"})
		return
	}
	s.obs.ingest.HandleHTTP(w, r)
}

func (s *Server) handleObservationOTLPTraces(w http.ResponseWriter, r *http.Request) {
	if s.obs == nil || s.obs.ingest == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "OTLP ingest unavailable"})
		return
	}
	s.obs.ingest.HandleOTLPTraces(w, r)
}

func (s *Server) handleObservationTraces(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeJSON(w, http.StatusOK, []store.ObservationTrace{})
		return
	}
	filter := store.ObservationTraceFilter{
		AgentID: r.URL.Query().Get("agent_id"), RuntimeID: r.URL.Query().Get("runtime_id"),
		ConversationID: r.URL.Query().Get("conversation_id"), SessionID: r.URL.Query().Get("session_id"),
		Status: r.URL.Query().Get("status"), Source: r.URL.Query().Get("source"),
		Limit: queryInt(r, "limit", 100), Offset: queryInt(r, "offset", 0),
	}
	traces, err := s.st.ListObservationTraces(r.Context(), filter)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, traces)
}

type observationSpanDetail struct {
	store.ObservationSpan
	Content    any `json:"content,omitempty"`
	ToolInput  any `json:"tool_input,omitempty"`
	ToolOutput any `json:"tool_output,omitempty"`
}

type observationEventDetail struct {
	core.ObservationEnvelope
	Content any `json:"content,omitempty"`
}

func (s *Server) handleObservationTrace(w http.ResponseWriter, r *http.Request) {
	traceID := r.PathValue("trace_id")
	trace, err := s.st.GetObservationTrace(r.Context(), traceID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if trace == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "trace not found"})
		return
	}
	spans, err := s.st.ListObservationSpans(r.Context(), traceID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	events, err := s.st.ListObservationEvents(r.Context(), traceID, 0, 5000)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	details := make([]observationSpanDetail, len(spans))
	spanIndex := map[string]int{}
	for index, span := range spans {
		details[index].ObservationSpan = span
		spanIndex[span.SpanID] = index
	}
	eventDetails := make([]observationEventDetail, 0, len(events))
	for _, event := range events {
		detail := observationEventDetail{ObservationEnvelope: event}
		if event.PayloadRef != nil && s.obs != nil && s.obs.recorder != nil {
			content, contentType, readErr := s.obs.recorder.ReadEnvelopePayload(r.Context(), event)
			if readErr == nil {
				detail.Content = decodeObservationContent(content, contentType)
				if index, ok := spanIndex[event.SpanID]; ok {
					switch {
					case event.Kind == "tool.call" && event.Lifecycle == core.ObservationLifecycleStart:
						details[index].ToolInput = detail.Content
					case event.Kind == "tool.call" && event.Lifecycle == core.ObservationLifecycleEnd:
						details[index].ToolOutput = detail.Content
					default:
						details[index].Content = detail.Content
					}
				}
			}
		}
		eventDetails = append(eventDetails, detail)
	}
	writeJSON(w, http.StatusOK, map[string]any{"trace": trace, "spans": details, "events": eventDetails})
}

func decodeObservationContent(content []byte, contentType string) any {
	if strings.Contains(strings.ToLower(contentType), "json") {
		var value any
		if json.Unmarshal(content, &value) == nil {
			return value
		}
	}
	if utf8.Valid(content) {
		return string(content)
	}
	return map[string]string{"encoding": "base64", "data": base64.StdEncoding.EncodeToString(content)}
}

func (s *Server) handleObservationOverview(w http.ResponseWriter, r *http.Request) {
	since := time.Now().UTC().Add(-24 * time.Hour)
	traces, err := s.st.ListObservationTraces(r.Context(), store.ObservationTraceFilter{Since: since, Limit: 1000})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var spansCount, eventsCount, modelRequests, toolCalls, failed, partial int64
	usage := core.ObservationUsage{}
	usageRecords, err := s.st.QueryObservationUsage(r.Context(), since)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	for _, record := range usageRecords {
		usage.InputTokens += record.InputTokens
		usage.OutputTokens += record.OutputTokens
		usage.CacheReadTokens += record.CacheReadTokens
		usage.CacheWriteTokens += record.CacheWriteTokens
		usage.TotalTokens += record.InputTokens + record.OutputTokens + record.CacheReadTokens + record.CacheWriteTokens
		usage.CostUSD += record.CostUSD
	}
	agents := map[string]bool{}
	type coverageStat struct {
		Source   string    `json:"source"`
		Quality  string    `json:"quality"`
		Status   string    `json:"status"`
		Events   int64     `json:"events"`
		Traces   int64     `json:"traces"`
		LastSeen time.Time `json:"last_seen_at"`
	}
	coverageMap := map[string]*coverageStat{}
	for _, trace := range traces {
		spansCount += trace.SpanCount
		eventsCount += trace.EventCount
		if trace.AgentID != "" {
			agents[trace.AgentID] = true
		}
		if trace.Status == core.ObservationStatusError {
			failed++
		}
		if trace.Quality == core.ObservationQualityPartial || trace.Quality == core.ObservationQualityInferred || trace.Quality == core.ObservationQualityLegacy {
			partial++
		}
		source := trace.Source
		if source == "" {
			source = "unknown"
		}
		stat := coverageMap[source]
		if stat == nil {
			stat = &coverageStat{Source: source, Quality: trace.Quality, Status: "active"}
			coverageMap[source] = stat
		}
		stat.Traces++
		stat.Events += trace.EventCount
		if trace.StartedAt.After(stat.LastSeen) {
			stat.LastSeen = trace.StartedAt
		}
		spans, spanErr := s.st.ListObservationSpans(r.Context(), trace.TraceID)
		if spanErr != nil {
			continue
		}
		for _, span := range spans {
			switch span.Kind {
			case "model.request":
				modelRequests++
			case "tool.call":
				toolCalls++
			}
		}
	}
	coverage := make([]coverageStat, 0, len(coverageMap))
	for _, stat := range coverageMap {
		coverage = append(coverage, *stat)
	}
	sort.Slice(coverage, func(i, j int) bool { return coverage[i].Source < coverage[j].Source })
	errorRate := 0.0
	if len(traces) > 0 {
		errorRate = float64(failed) / float64(len(traces))
	}
	recent := traces
	if len(recent) > 20 {
		recent = recent[:20]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"traces": len(traces), "spans": spansCount, "events": eventsCount,
		"model_requests": modelRequests, "tool_calls": toolCalls, "failed_traces": failed,
		"partial_traces": partial, "active_agents": len(agents), "error_rate": errorRate,
		"usage": usage, "coverage": coverage, "recent_traces": recent,
	})
}

func (s *Server) handleObservationInsights(w http.ResponseWriter, r *http.Request) {
	if s.obs != nil && s.obs.insights != nil {
		_, _ = s.obs.insights.Run(r.Context(), time.Now().UTC().Add(-7*24*time.Hour))
	}
	insights, err := s.st.ListObservationInsights(r.Context(), store.ObservationInsightFilter{
		AgentID: r.URL.Query().Get("agent_id"), Status: r.URL.Query().Get("status"),
		RuleID: r.URL.Query().Get("rule_id"), Limit: queryInt(r, "limit", 100),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, insights)
}

func (s *Server) handleObservationSettings(w http.ResponseWriter, r *http.Request) {
	if s.obs == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	exporters := make([]map[string]any, 0, len(s.obs.config.Exporters))
	for _, exporter := range s.obs.config.Exporters {
		headerKeys := make([]string, 0, len(exporter.Headers))
		for key := range exporter.Headers {
			headerKeys = append(headerKeys, key)
		}
		sort.Strings(headerKeys)
		exporters = append(exporters, map[string]any{
			"name": exporter.Name, "type": exporter.Type, "endpoint": exporter.Endpoint,
			"enabled": exporter.Enabled, "include_content": exporter.IncludeContent, "header_keys": headerKeys,
		})
	}
	metadataOnly, reason := true, "recorder unavailable"
	if s.obs.recorder != nil {
		metadataOnly, reason = s.obs.recorder.MetadataOnly(), s.obs.recorder.MetadataOnlyReason()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": s.obs.config.Enabled, "capture_content": s.obs.config.CaptureContent,
		"content_retention_days": s.obs.config.ContentRetentionDays, "detail_retention_days": s.obs.config.DetailRetentionDays,
		"backfill_days": s.obs.config.BackfillDays, "metadata_only": metadataOnly,
		"metadata_only_reason": reason, "key_status": map[bool]string{true: "metadata-only", false: "encrypted"}[metadataOnly],
		"exporters": exporters,
	})
}

func (s *Server) handleObservationIntegrations(w http.ResponseWriter, r *http.Request) {
	if s.obs == nil || s.obs.native == nil {
		writeJSON(w, http.StatusOK, []nativeintegration.DoctorReport{})
		return
	}
	reports := make([]nativeintegration.DoctorReport, 0, 2)
	for _, host := range []nativeintegration.Host{nativeintegration.HostClaude, nativeintegration.HostCodex} {
		report, err := s.obs.native.Doctor(r.Context(), host)
		if err != nil {
			report = nativeintegration.DoctorReport{Host: host, Status: nativeintegration.StatusUnavailable,
				Findings: []nativeintegration.Finding{{Code: "doctor_failed", Severity: nativeintegration.SeverityError, Message: err.Error()}}}
		}
		if report.Coverage == nil {
			report.Coverage = map[string]string{}
		}
		report.Coverage["otel"] = "private_runtime"
		report.Coverage["transcript"] = "enabled"
		if s.st != nil {
			tool := "claudecode"
			if host == nativeintegration.HostCodex {
				tool = "codex"
			}
			if proxyConfig, proxyErr := s.st.GetProxyToolConfig(r.Context(), tool); proxyErr == nil && proxyConfig.Enabled {
				report.Coverage["proxy"] = "enabled"
			}
		}
		reports = append(reports, report)
	}
	writeJSON(w, http.StatusOK, reports)
}

func (s *Server) handleObservationIntegrationAction(w http.ResponseWriter, r *http.Request) {
	if s.obs == nil || s.obs.native == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "native integration manager unavailable"})
		return
	}
	host := nativeintegration.Host(r.PathValue("host"))
	if !host.Valid() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported integration host"})
		return
	}
	action := r.PathValue("action")
	if s.st != nil && (action == "install" || action == "repair" || action == "uninstall") {
		lease, acquired, leaseErr := s.st.AcquireObservationResourceLease(r.Context(), "native-integration:"+string(host), "agentmux-api", "", 2*time.Minute,
			map[string]any{"action": action})
		if leaseErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": leaseErr.Error()})
			return
		}
		if !acquired {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "native integration is being changed by another owner"})
			return
		}
		defer func() {
			_, _ = s.st.ReleaseObservationResourceLease(context.Background(), lease.ResourceKey, lease.LeaseToken)
		}()
	}
	var result any
	var err error
	switch action {
	case "preview":
		result, err = s.obs.native.Preview(r.Context(), host)
	case "install":
		result, err = s.obs.native.Install(r.Context(), host)
	case "repair":
		result, err = s.obs.native.Repair(r.Context(), host)
	case "uninstall":
		result, err = s.obs.native.Uninstall(r.Context(), host)
	case "doctor":
		result, err = s.obs.native.Doctor(r.Context(), host)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported integration action"})
		return
	}
	if nativeResult, ok := result.(nativeintegration.Result); ok && s.st != nil {
		if syncErr := s.syncObservationIntegrationOwnership(r.Context(), action, nativeResult); syncErr != nil && err == nil {
			err = syncErr
		}
	}
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "result": result})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) syncObservationIntegrationOwnership(ctx context.Context, action string, result nativeintegration.Result) error {
	if action == "uninstall" && result.Record == nil {
		rows, err := s.st.ListObservationIntegrationOwnership(ctx, string(result.Host), "user")
		if err != nil {
			return err
		}
		for _, row := range rows {
			deleted, err := s.st.DeleteObservationIntegrationOwnership(ctx, row.InstallID, row.ResourceKey, row.HandlerFingerprint)
			if err != nil {
				return err
			}
			if !deleted {
				return fmt.Errorf("integration ownership drift for %s", row.ResourceKey)
			}
		}
		return nil
	}
	if result.Record == nil {
		return nil
	}
	for _, resource := range result.Record.Resources {
		fingerprint := resource.HandlerFingerprint
		if fingerprint == "" {
			fingerprint = resource.AfterHash
		}
		claimed, err := s.st.ClaimObservationIntegrationOwnership(ctx, store.ObservationIntegrationOwnership{
			InstallID: result.Record.InstallID, Host: string(result.Record.Host), Scope: result.Record.Scope,
			ResourceKey: resource.Kind + ":" + resource.TargetPath, Version: result.Record.Version,
			SHA256: resource.AfterHash, HandlerFingerprint: fingerprint, TargetPath: resource.TargetPath,
			BeforeHash: resource.BeforeHash, AfterHash: resource.AfterHash,
			Metadata: map[string]any{"plugin_id": result.Record.PluginID, "marketplace": result.Record.Marketplace},
		})
		if err != nil {
			return err
		}
		if !claimed {
			return fmt.Errorf("integration resource already owned: %s", resource.TargetPath)
		}
	}
	return nil
}

func queryInt(r *http.Request, key string, fallback int) int {
	value, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil {
		return fallback
	}
	return value
}
