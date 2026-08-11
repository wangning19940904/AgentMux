package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

const maxInvocationRequestBytes = 1 << 20

// handleInvocation runs a configured Agent directly and returns its final
// answer. This is an API ingress, not a messaging channel: no Platform is
// created and no response is published to IM.
func (s *Server) handleInvocation(w http.ResponseWriter, r *http.Request) {
	if s.invoker == nil {
		writeErr(w, http.StatusServiceUnavailable, "invocation runtime unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxInvocationRequestBytes)
	req, ok := decodeJSON[core.InvocationRequest](w, r)
	if !ok {
		return
	}
	result, err := s.invoker.Invoke(r.Context(), req)
	if err != nil {
		writeErr(w, invocationErrorStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type invocationStreamOutcome struct {
	result core.InvocationResult
	err    error
}

// handleInvocationStream exposes direct Agent events as Server-Sent Events.
// Invocation work runs in a goroutine so this handler can send keepalive
// comments while an Agent is waiting on a model or long-running tool.
func (s *Server) handleInvocationStream(w http.ResponseWriter, r *http.Request) {
	streamer, ok := s.invoker.(core.StreamingInvoker)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "streaming invocation runtime unavailable")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming is unsupported by this HTTP server")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxInvocationRequestBytes)
	req, decoded := decodeJSON[core.InvocationRequest](w, r)
	if !decoded {
		return
	}

	streamCtx, cancel := context.WithCancel(r.Context())
	defer cancel()
	events := make(chan core.InvocationStreamEvent)
	done := make(chan invocationStreamOutcome, 1)
	go func() {
		result, err := streamer.InvokeStream(streamCtx, req, func(event core.InvocationStreamEvent) error {
			select {
			case events <- event:
				return nil
			case <-streamCtx.Done():
				return streamCtx.Err()
			}
		})
		done <- invocationStreamOutcome{result: result, err: err}
	}()

	started := false
	terminalEvent := false
	startSSE := func() {
		if started {
			return
		}
		started = true
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
	}
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case event := <-events:
			startSSE()
			if event.Type == "completed" || event.Type == "error" {
				terminalEvent = true
			}
			if err := writeInvocationSSE(w, flusher, event); err != nil {
				cancel()
				return
			}
		case outcome := <-done:
			if !started {
				if outcome.err != nil {
					writeErr(w, invocationErrorStatus(outcome.err), outcome.err.Error())
					return
				}
				startSSE()
				completed := outcome.result
				_ = writeInvocationSSE(w, flusher, core.InvocationStreamEvent{
					Type: "completed", InvocationID: completed.ID, ConversationID: completed.ConversationID,
					SessionID: completed.SessionID, Final: true, DurationMS: completed.DurationMS, Result: &completed,
				})
				return
			}
			if outcome.err != nil && !terminalEvent && !errors.Is(outcome.err, context.Canceled) {
				_ = writeInvocationSSE(w, flusher, core.InvocationStreamEvent{
					Type: "error", InvocationID: outcome.result.ID, ConversationID: outcome.result.ConversationID,
					SessionID: outcome.result.SessionID, Error: outcome.err.Error(), DurationMS: outcome.result.DurationMS,
				})
			}
			return
		case <-keepalive.C:
			if started {
				if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
					cancel()
					return
				}
				flusher.Flush()
			}
		case <-streamCtx.Done():
			return
		}
	}
}

func writeInvocationSSE(w io.Writer, flusher http.Flusher, event core.InvocationStreamEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", invocationSSEEventName(event.Type), payload); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func invocationSSEEventName(value string) string {
	if value == "" {
		return "event"
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' {
			continue
		}
		return "event"
	}
	return value
}

func invocationErrorStatus(err error) int {
	switch {
	case errors.Is(err, core.ErrInvalidInvocation):
		return http.StatusBadRequest
	case errors.Is(err, core.ErrInvocationNotFound):
		return http.StatusNotFound
	case errors.Is(err, core.ErrInvocationDisabled), errors.Is(err, core.ErrInvocationBusy):
		return http.StatusConflict
	case errors.Is(err, core.ErrInvocationRuntime):
		return http.StatusServiceUnavailable
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout
	default:
		return http.StatusInternalServerError
	}
}
