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
	var lastErr error
	now := time.Now()
	for i, p := range chain {
		br := s.breakerFor(p.ID)
		if len(chain) > 1 && !br.available(now) {
			continue
		}
		reqBody := cloneJSONMap(parsed)
		upstreamModel, err := resolveUpstreamModel(p, reqBody, opts.mapModel)
		if err != nil {
			s.recordProxyTrace(r, parsed, opts, p, requestModel, "", upstreamProto(p.Meta.APIFormat, opts.clientProto), http.StatusBadRequest, false, err)
			opts.writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		ok, retryable, statusCode, upstream, ferr := s.forwardOne(w, r, p, reqBody, requestModel, upstreamModel, opts)
		if ok {
			br.recordSuccess()
			if i > 0 && cfg.AutoFailover {
				s.hotSwitchAfterFailover(r.Context(), opts.tool, p)
			}
			s.recordProxyTrace(r, parsed, opts, p, requestModel, upstreamModel, upstream, statusCode, true, nil)
			return
		}
		s.recordProxyTrace(r, parsed, opts, p, requestModel, upstreamModel, upstream, statusCode, false, ferr)
		lastErr = ferr
		if br.recordFailure(int32(cfg.FailureThreshold), time.Duration(cfg.CooldownSeconds)*time.Second) {
			s.log.Warn("circuit opened", "provider", p.ID, "cooldown_s", cfg.CooldownSeconds)
		}
		if !retryable {
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
func (s *ProxyServer) forwardOne(w http.ResponseWriter, r *http.Request, p *core.Provider, body map[string]any, requestModel, upstreamModel string, opts forwardOpts) (ok, retryable bool, statusCode int, upstream string, err error) {
	upstream = upstreamProto(p.Meta.APIFormat, opts.clientProto)

	if issue := providerAPIKeyIssue(p); issue != "" {
		return false, true, 0, upstream, fmt.Errorf("%s: %s", p.ID, issue)
	}

	var payload []byte
	if upstream == opts.clientProto {
		// Same protocol: pass the (model-rewritten) body through unchanged.
		payload, _ = json.Marshal(body)
	} else {
		ir, cerr := decodeToIR(opts.clientProto, body)
		if cerr != nil {
			return false, false, 0, upstream, cerr
		}
		if upstreamModel != "" {
			ir["model"] = upstreamModel
		}
		upstreamBody, cerr := encodeFromIR(upstream, ir)
		if cerr != nil {
			return false, false, 0, upstream, cerr
		}
		payload, _ = json.Marshal(upstreamBody)
	}

	endpoint := upstreamEndpoint(upstream, p.BaseURL, upstreamModel, opts.stream)
	req, rerr := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, bytes.NewReader(payload))
	if rerr != nil {
		return false, true, 0, upstream, rerr
	}
	if upstream == opts.clientProto {
		copyProxyHeaders(req.Header, r.Header)
	}
	req.Header.Set("Content-Type", "application/json")
	applyUpstreamAuth(req, upstream, providerAPIKey(p))

	resp, derr := s.client.Do(req)
	if derr != nil {
		return false, true, 0, upstream, derr
	}
	defer resp.Body.Close()

	if isFailoverStatus(resp.StatusCode) || resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return false, isFailoverStatus(resp.StatusCode), resp.StatusCode, upstream, fmt.Errorf("%s: upstream %d: %s", p.ID, resp.StatusCode, truncate(string(b), 300))
	}

	// Same-protocol responses (including streams) are relayed verbatim.
	if upstream == opts.clientProto {
		relayResponse(w, resp)
		return true, false, resp.StatusCode, upstream, nil
	}

	if opts.stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		if serr := streamUpstreamToClient(upstream, opts.clientProto, resp.Body, w, requestModel); serr != nil {
			s.log.Warn("stream conversion aborted", "provider", p.ID, "err", serr)
		}
		return true, false, resp.StatusCode, upstream, nil
	}

	raw, rerr := io.ReadAll(resp.Body)
	if rerr != nil {
		return false, false, resp.StatusCode, upstream, rerr
	}
	var upstreamResp map[string]any
	if jerr := json.Unmarshal(raw, &upstreamResp); jerr != nil {
		return false, true, resp.StatusCode, upstream, fmt.Errorf("%s: invalid upstream JSON", p.ID)
	}
	ir, cerr := upstreamRespToIR(upstream, upstreamResp)
	if cerr != nil {
		return false, false, resp.StatusCode, upstream, cerr
	}
	clientResp, cerr := irRespToClient(opts.clientProto, ir, requestModel)
	if cerr != nil {
		return false, false, resp.StatusCode, upstream, cerr
	}
	writeProxyJSON(w, http.StatusOK, clientResp)
	return true, false, resp.StatusCode, upstream, nil
}

func (s *ProxyServer) recordProxyTrace(r *http.Request, body map[string]any, opts forwardOpts, p *core.Provider, clientModel, upstreamModel, upstream string, statusCode int, success bool, ferr error) {
	if s.st == nil || p == nil {
		return
	}
	trace := core.ProxyTrace{
		Timestamp:        time.Now().UTC(),
		Tool:             opts.tool,
		ProviderID:       p.ID,
		ProviderName:     p.Name,
		ClientProtocol:   opts.clientProto,
		UpstreamProtocol: upstream,
		ClientModel:      clientModel,
		UpstreamModel:    upstreamModel,
		StatusCode:       statusCode,
		Success:          success,
		SessionID:        traceSessionID(r, body),
		ProjectDir:       traceProjectDir(r, body),
	}
	if ferr != nil {
		trace.Error = ferr.Error()
	}
	traceCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Second)
	defer cancel()
	if err := s.st.InsertProxyTrace(traceCtx, trace); err != nil {
		s.log.Warn("proxy trace insert failed", "tool", opts.tool, "provider", p.ID, "err", err)
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
