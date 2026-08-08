package server

import (
	"encoding/json"
	"fmt"
	"net/http"
)

type installProgress struct {
	Phase   string `json:"phase"`
	Detail  string `json:"detail,omitempty"`
	Percent int    `json:"percent"`
}

type installStreamEvent struct {
	Type     string           `json:"type"`
	Progress *installProgress `json:"progress,omitempty"`
	Result   any              `json:"result,omitempty"`
}

// streamInstall runs an installer beside the response loop so progress
// callbacks can be flushed immediately through the browser and SSH reverse
// proxies. The final event carries the same result shape as the JSON endpoint.
func streamInstall(w http.ResponseWriter, r *http.Request, run func(report func(string, string, int)) any) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	progressCh := make(chan installProgress, 16)
	resultCh := make(chan any, 1)
	go func() {
		resultCh <- run(func(phase, detail string, percent int) {
			select {
			case progressCh <- installProgress{Phase: phase, Detail: detail, Percent: percent}:
			default:
			}
		})
	}()

	writeEvent := func(event installStreamEvent) bool {
		payload, err := json.Marshal(event)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	for {
		select {
		case progress := <-progressCh:
			if !writeEvent(installStreamEvent{Type: "progress", Progress: &progress}) {
				return
			}
		case result := <-resultCh:
			// A very fast command can finish before the response loop consumes all
			// buffered stages. Flush those stages before the terminal events.
			for {
				select {
				case progress := <-progressCh:
					if !writeEvent(installStreamEvent{Type: "progress", Progress: &progress}) {
						return
					}
				default:
					goto progressDrained
				}
			}
		progressDrained:
			complete := installProgress{Phase: "complete", Percent: 100}
			if !writeEvent(installStreamEvent{Type: "progress", Progress: &complete}) {
				return
			}
			writeEvent(installStreamEvent{Type: "result", Result: result})
			return
		case <-r.Context().Done():
			return
		}
	}
}
