package observability

import (
	"context"
	"encoding/json"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// ObserveProxyTrace converts one local-routing attempt into a model span. A
// W3C traceparent supplied by the launched agent joins the attempt to its
// agent.run span; standalone proxy clients still get a complete trace.
func (r *Runtime) ObserveProxyTrace(ctx context.Context, trace core.ProxyTrace, requestBody, responseBody []byte) error {
	if r == nil || r.Bus == nil {
		return nil
	}
	traceID := trace.TraceID
	if r.Ingest != nil {
		traceID = r.Ingest.ResolveTraceID(traceID)
	}
	if traceID == "" {
		traceID = stableHex("proxy:trace:"+trace.RequestID+":"+trace.ID, 16)
	}
	spanID := stableHex("proxy:attempt:"+trace.ID, 8)
	started := trace.StartedAt
	if started.IsZero() {
		started = trace.Timestamp.Add(-time.Duration(trace.DurationMs) * time.Millisecond)
	}
	if started.IsZero() {
		started = time.Now().UTC()
	}
	ended := trace.Timestamp
	if ended.IsZero() {
		ended = started.Add(time.Duration(trace.DurationMs) * time.Millisecond)
	}
	model := &core.ObservationModel{
		Provider: trace.ProviderName, Requested: trace.ClientModel, Resolved: trace.UpstreamModel,
		Protocol: trace.UpstreamProtocol, RequestID: trace.RequestID, Attempt: trace.Attempt,
		FinishReason: trace.FinishReason, TTFTMillis: trace.TTFTMs, DurationMillis: trace.DurationMs,
	}
	attributes := map[string]any{
		"tool": trace.Tool, "provider_id": trace.ProviderID,
		"client_protocol": trace.ClientProtocol, "upstream_protocol": trace.UpstreamProtocol,
		"attempt_id": trace.ID, "parent_attempt_id": trace.ParentAttemptID,
		"request_bytes": trace.RequestBytes, "response_bytes": trace.ResponseBytes,
		"stream_complete":            trace.StreamComplete,
		"response_capture_truncated": trace.ResponseBytes > int64(len(responseBody)),
	}
	start := core.ObservationEnvelope{
		EventID: stableProxyEventID(trace.ID, "start"), Time: started, TraceID: traceID, SpanID: spanID,
		ParentSpanID: trace.ParentSpanID, DedupeKey: "proxy:" + trace.ID + ":start",
		Kind: "model.request", Name: "Proxy model request", Lifecycle: core.ObservationLifecycleStart,
		RuntimeID: trace.Tool, SessionID: trace.SessionID, Source: "agentmux.proxy",
		Provenance: []string{"proxy", trace.ClientProtocol, trace.UpstreamProtocol}, Quality: core.ObservationQualityComplete,
		Status: core.ObservationStatusRunning, Model: model, Attributes: attributes,
	}
	if len(requestBody) > 0 {
		start.Content = &core.ObservationContent{ContentType: "application/json", Data: requestBody}
	}
	if err := r.Bus.Publish(ctx, start); err != nil {
		return err
	}
	status := core.ObservationStatusOK
	var observationError *core.ObservationError
	if !trace.Success {
		status = core.ObservationStatusError
		observationError = &core.ObservationError{Code: "proxy_request_failed", Message: "Proxy model request failed", Retryable: trace.ParentAttemptID == ""}
	}
	usage := &core.ObservationUsage{
		InputTokens: trace.InputTokens, OutputTokens: trace.OutputTokens,
		CacheReadTokens: trace.CacheReadTokens, CacheWriteTokens: trace.CacheWriteTokens,
		TotalTokens: trace.InputTokens + trace.OutputTokens + trace.CacheReadTokens + trace.CacheWriteTokens, CostUSD: trace.CostUSD,
	}
	end := start
	end.EventID = stableProxyEventID(trace.ID, "end")
	end.Time = ended
	end.DedupeKey = "proxy:" + trace.ID + ":end"
	end.Lifecycle = core.ObservationLifecycleEnd
	end.Status = status
	end.Error = observationError
	end.Usage = usage
	end.Content = nil
	if len(responseBody) > 0 {
		contentType := "application/json"
		if !json.Valid(responseBody) {
			contentType = "text/plain; charset=utf-8"
		}
		end.Content = &core.ObservationContent{ContentType: contentType, Data: responseBody}
	} else if trace.Error != "" {
		end.Content = &core.ObservationContent{ContentType: "text/plain; charset=utf-8", Data: []byte(trace.Error)}
	}
	return r.Bus.Publish(ctx, end)
}

func stableProxyEventID(attemptID, lifecycle string) string {
	return "obs_" + stableHex("proxy:event:"+attemptID+":"+lifecycle, 16)
}
