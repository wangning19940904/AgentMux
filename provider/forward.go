package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/agentnexus/agentnexus/core"
)

// forward.go is the protocol-agnostic request forwarder. A single code path
// serves every client entry (Anthropic, OpenAI Chat, Responses, Gemini) against
// every upstream protocol: when client and upstream speak the same protocol the
// body is passed through untouched, otherwise it is translated through the IR
// hub in xlate.go.

// forwardOpts carries per-request routing context shared by all client entries.
type forwardOpts struct {
	tool        string // active-route tool key (claudecode, codex, gemini, claude-desktop)
	clientProto string // the protocol the client speaks
	stream      bool
	// mapModel, when set, rewrites the request body's model in place before
	// translation (Claude Desktop route mapping). When nil the tiered/default
	// model resolution is used.
	mapModel func(*core.Provider, map[string]any) error
	// writeErr writes a protocol-appropriate error body to the client.
	writeErr func(w http.ResponseWriter, code int, message string)
}

// forwardChain runs the provider failover chain for one request, translating
// protocols as needed. It owns breaker checks, model mapping, and hot-switch on
// failover, mirroring the previous handleAnthropicMessages loop but generalized
// across all client protocols.
func (s *ProxyServer) forwardChain(w http.ResponseWriter, r *http.Request, parsed map[string]any, opts forwardOpts) {
	chain, cfg := s.providerChain(r.Context(), opts.tool)
	if len(chain) == 0 {
		opts.writeErr(w, http.StatusBadGateway, "no provider routed for "+opts.tool)
		return
	}
	requestModel := stringValue(parsed["model"])
	identity := proxyRequestIdentityFrom(r)
	var lastErr error
	var parentAttemptID string
	now := time.Now()
	for i, p := range chain {
		br := s.breakerFor(p.ID)
		if len(chain) > 1 && !br.available(now) {
			continue
		}
		reqBody := cloneJSONMap(parsed)
		attemptID := randomProxyID("pattempt-")
		startedAt := time.Now().UTC()
		upstreamModel, err := resolveUpstreamModel(p, reqBody, opts.mapModel)
		if err != nil {
			result := proxyAttemptResult{
				StatusCode: http.StatusBadRequest, Upstream: upstreamProto(p.Meta.APIFormat, opts.clientProto),
				Err: err, StartedAt: startedAt, Duration: time.Since(startedAt),
			}
			s.recordProxyTrace(r, parsed, opts, p, requestModel, "", identity, attemptID, parentAttemptID, i+1, result)
			opts.writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		var beforeRespond func()
		if i > 0 && cfg.AutoFailover {
			// Commit the route change before exposing the successful failover
			// response to the client. A net/http client may finish reading a
			// flushed response while this handler is still running, so switching
			// after forwardOne returns makes the route state observably racy.
			beforeRespond = func() {
				switchCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Second)
				defer cancel()
				s.hotSwitchAfterFailover(switchCtx, opts.tool, p)
			}
		}
		result := s.forwardOne(w, r, p, reqBody, requestModel, upstreamModel, opts, startedAt, beforeRespond)
		s.recordProxyTrace(r, parsed, opts, p, requestModel, upstreamModel, identity, attemptID, parentAttemptID, i+1, result)
		if result.OK {
			br.recordSuccess()
			return
		}
		lastErr = result.Err
		parentAttemptID = attemptID
		if br.recordFailure(int32(cfg.FailureThreshold), time.Duration(cfg.CooldownSeconds)*time.Second) {
			s.log.Warn("circuit opened", "provider", p.ID, "cooldown_s", cfg.CooldownSeconds)
		}
		if !result.Retryable {
			return
		}
	}
	msg := "all providers failed"
	if lastErr != nil {
		msg += ": " + lastErr.Error()
	}
	opts.writeErr(w, http.StatusBadGateway, msg)
}

// resolveUpstreamModel decides the model id sent upstream and writes it into the
// request body. mapModel (Claude Desktop) takes precedence; otherwise tiered and
// provider-default resolution applies.
func resolveUpstreamModel(p *core.Provider, body map[string]any, mapModel func(*core.Provider, map[string]any) error) (string, error) {
	if mapModel != nil {
		if err := mapModel(p, body); err != nil {
			return "", err
		}
		return stringValue(body["model"]), nil
	}
	model := upstreamModelFor(p, stringValue(body["model"]))
	if model != "" {
		body["model"] = model
	}
	return model, nil
}

// forwardOne performs a single upstream attempt against provider p. It returns
// ok=true once a response has been written to the client; retryable indicates
// whether failover should try the next provider on failure.
func (s *ProxyServer) forwardOne(w http.ResponseWriter, r *http.Request, p *core.Provider, body map[string]any, requestModel, upstreamModel string, opts forwardOpts, startedAt time.Time, beforeRespond func()) (result proxyAttemptResult) {
	result.StartedAt = startedAt
	result.Upstream = upstreamProto(p.Meta.APIFormat, opts.clientProto)
	defer func() { result.Duration = time.Since(startedAt) }()

	if issue := providerAPIKeyIssue(p); issue != "" {
		result.Retryable = true
		result.Err = fmt.Errorf("%s: %s", p.ID, issue)
		return result
	}

	var payload []byte
	if result.Upstream == opts.clientProto {
		// Same protocol: pass the (model-rewritten) body through unchanged.
		payload, _ = json.Marshal(body)
	} else {
		ir, cerr := decodeToIR(opts.clientProto, body)
		if cerr != nil {
			result.Err = cerr
			return result
		}
		if upstreamModel != "" {
			ir["model"] = upstreamModel
		}
		upstreamBody, cerr := encodeFromIR(result.Upstream, ir)
		if cerr != nil {
			result.Err = cerr
			return result
		}
		payload, _ = json.Marshal(upstreamBody)
	}

	result.RequestBytes = int64(len(payload))
	endpoint := upstreamEndpoint(result.Upstream, p.BaseURL, upstreamModel, opts.stream)
	req, rerr := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, bytes.NewReader(payload))
	if rerr != nil {
		result.Retryable = true
		result.Err = rerr
		return result
	}
	if result.Upstream == opts.clientProto {
		copyProxyHeaders(req.Header, r.Header)
	}
	req.Header.Set("Content-Type", "application/json")
	applyUpstreamAuth(req, result.Upstream, providerAPIKey(p))

	resp, derr := s.client.Do(req)
	if derr != nil {
		result.Retryable = true
		result.Err = derr
		return result
	}
	defer resp.Body.Close()
	result.StatusCode = resp.StatusCode
	capture := &proxyCaptureReader{r: resp.Body, start: startedAt}
	resp.Body = struct {
		io.Reader
		io.Closer
	}{Reader: capture, Closer: resp.Body}
	defer capture.apply(&result, result.Upstream, opts.stream)

	if isFailoverStatus(resp.StatusCode) || resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		result.Retryable = isFailoverStatus(resp.StatusCode)
		result.Err = fmt.Errorf("%s: upstream %d: %s", p.ID, resp.StatusCode, truncate(string(b), 300))
		return result
	}

	// Same-protocol responses (including streams) are relayed verbatim.
	if result.Upstream == opts.clientProto {
		if beforeRespond != nil {
			beforeRespond()
		}
		if err := relayResponse(w, resp); err != nil {
			result.Err = fmt.Errorf("relay upstream stream: %w", err)
			return result
		}
		result.OK = true
		return result
	}

	if opts.stream {
		if beforeRespond != nil {
			beforeRespond()
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		if serr := streamUpstreamToClient(result.Upstream, opts.clientProto, resp.Body, w, requestModel); serr != nil {
			s.log.Warn("stream conversion aborted", "provider", p.ID, "err", serr)
			result.Err = fmt.Errorf("stream conversion aborted: %w", serr)
			return result
		}
		result.OK = true
		return result
	}

	raw, rerr := io.ReadAll(resp.Body)
	if rerr != nil {
		result.Err = rerr
		return result
	}
	var upstreamResp map[string]any
	if jerr := json.Unmarshal(raw, &upstreamResp); jerr != nil {
		result.Retryable = true
		result.Err = fmt.Errorf("%s: invalid upstream JSON", p.ID)
		return result
	}
	ir, cerr := upstreamRespToIR(result.Upstream, upstreamResp)
	if cerr != nil {
		result.Err = cerr
		return result
	}
	clientResp, cerr := irRespToClient(opts.clientProto, ir, requestModel)
	if cerr != nil {
		result.Err = cerr
		return result
	}
	if beforeRespond != nil {
		beforeRespond()
	}
	writeProxyJSON(w, http.StatusOK, clientResp)
	result.OK = true
	return result
}

func (s *ProxyServer) recordProxyTrace(r *http.Request, body map[string]any, opts forwardOpts, p *core.Provider, clientModel, upstreamModel string, identity proxyRequestIdentity, attemptID, parentAttemptID string, attempt int, result proxyAttemptResult) {
	if s.st == nil || p == nil {
		return
	}
	trace := core.ProxyTrace{
		ID:               attemptID,
		RequestID:        identity.RequestID,
		TraceID:          identity.TraceID,
		ParentSpanID:     identity.ParentSpanID,
		Attempt:          attempt,
		ParentAttemptID:  parentAttemptID,
		Timestamp:        time.Now().UTC(),
		StartedAt:        result.StartedAt,
		Tool:             opts.tool,
		ProviderID:       p.ID,
		ProviderName:     p.Name,
		ClientProtocol:   opts.clientProto,
		UpstreamProtocol: result.Upstream,
		ClientModel:      clientModel,
		UpstreamModel:    upstreamModel,
		StatusCode:       result.StatusCode,
		Success:          result.OK,
		SessionID:        traceSessionID(r, body),
		ProjectDir:       traceProjectDir(r, body),
		TTFTMs:           result.TTFT.Milliseconds(),
		DurationMs:       result.Duration.Milliseconds(),
		StreamComplete:   result.StreamComplete,
		FinishReason:     result.FinishReason,
		InputTokens:      result.InputTokens,
		OutputTokens:     result.OutputTokens,
		CacheReadTokens:  result.CacheReadTokens,
		CacheWriteTokens: result.CacheWriteTokens,
		RequestBytes:     result.RequestBytes,
		ResponseBytes:    result.ResponseBytes,
	}
	rawError := ""
	if result.Err != nil {
		rawError = result.Err.Error()
		trace.Error = "Proxy request failed"
	}
	trace.CostUSD = s.estimateTraceCost(trace)
	traceCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Second)
	defer cancel()
	if err := s.st.InsertProxyTrace(traceCtx, trace); err != nil {
		s.log.Warn("proxy trace insert failed", "tool", opts.tool, "provider", p.ID, "err", err)
		return
	}
	requestBody, _ := json.Marshal(body)
	responseCapture := result.ResponseBody
	if len(responseCapture) == 0 && rawError != "" {
		responseCapture = []byte(rawError)
	}
	if err := s.observeTrace(traceCtx, trace, requestBody, responseCapture); err != nil {
		s.log.Warn("proxy observation publish failed", "tool", opts.tool, "provider", p.ID, "err", err)
	}
}

func traceSessionID(r *http.Request, body map[string]any) string {
	return firstTraceValue(
		traceBodyString(body, "session_id", "sessionId", "conversation_id", "conversationId", "thread_id", "threadId"),
		traceNestedBodyString(body, "metadata", "session_id", "sessionId", "conversation_id", "conversationId", "thread_id", "threadId"),
		r.Header.Get("x-agentnexus-session-id"),
		r.Header.Get("x-session-id"),
		r.Header.Get("x-conversation-id"),
		r.Header.Get("x-thread-id"),
	)
}

func traceProjectDir(r *http.Request, body map[string]any) string {
	return firstTraceValue(
		traceBodyString(body, "project_dir", "projectDir", "cwd", "working_directory", "workingDirectory"),
		traceNestedBodyString(body, "metadata", "project_dir", "projectDir", "cwd", "working_directory", "workingDirectory"),
		r.Header.Get("x-agentnexus-project-dir"),
		r.Header.Get("x-project-dir"),
		r.Header.Get("x-cwd"),
	)
}

func traceBodyString(body map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringValue(body[key]); value != "" {
			return value
		}
	}
	return ""
}

func traceNestedBodyString(body map[string]any, key string, keys ...string) string {
	nested, _ := body[key].(map[string]any)
	if nested == nil {
		return ""
	}
	return traceBodyString(nested, keys...)
}

func firstTraceValue(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// upstreamEndpoint builds the upstream URL for a protocol. Gemini carries the
// model and stream mode in the path; the others use a fixed suffix.
func upstreamEndpoint(proto, baseURL, model string, stream bool) string {
	switch proto {
	case protoAnthropic:
		return joinURL(baseURL, "/v1/messages")
	case protoResponses:
		return joinURL(baseURL, "/responses")
	case protoGemini:
		method := "generateContent"
		if stream {
			method = "streamGenerateContent"
		}
		m := strings.TrimSpace(model)
		if m == "" {
			m = "gemini-pro"
		}
		u := joinURL(baseURL, "/models/"+url.PathEscape(m)+":"+method)
		if stream {
			u += "?alt=sse"
		}
		return u
	default: // protoOpenAIChat
		return joinURL(baseURL, "/chat/completions")
	}
}

// applyUpstreamAuth sets the credential headers each upstream protocol expects.
func applyUpstreamAuth(req *http.Request, proto, key string) {
	if key == "" {
		return
	}
	switch proto {
	case protoAnthropic:
		req.Header.Set("x-api-key", key)
		req.Header.Set("Authorization", "Bearer "+key)
		if req.Header.Get("anthropic-version") == "" {
			req.Header.Set("anthropic-version", "2023-06-01")
		}
	case protoGemini:
		req.Header.Set("x-goog-api-key", key)
	default: // openai_chat, openai_responses
		req.Header.Set("Authorization", "Bearer "+key)
	}
}
