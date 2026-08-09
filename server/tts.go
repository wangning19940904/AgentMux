package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	ttspkg "github.com/wangning19940904/AgentMux/tts"
)

func (s *Server) handleTTSModels(w http.ResponseWriter, _ *http.Request) {
	if s.ttsModels == nil {
		serviceUnavailable(w, "local TTS model manager")
		return
	}
	writeJSON(w, http.StatusOK, s.ttsModels.Catalog())
}

func (s *Server) handleTTSModelDelete(w http.ResponseWriter, r *http.Request) {
	if s.ttsModels == nil {
		serviceUnavailable(w, "local TTS model manager")
		return
	}
	id, ok := requireQuery(w, r, "id")
	if !ok {
		return
	}
	if err := s.ttsModels.Remove(id); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.ttsModels.Catalog())
}

func (s *Server) handleTTSModelDownload(w http.ResponseWriter, r *http.Request) {
	if s.ttsModels == nil {
		serviceUnavailable(w, "local TTS model manager")
		return
	}
	request, ok := decodeJSON[struct {
		ID string `json:"id"`
	}](w, r)
	if !ok {
		return
	}
	request.ID = strings.TrimSpace(request.ID)
	if request.ID == "" {
		writeErr(w, http.StatusBadRequest, "model id is required")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming is unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	writeEvent := func(value any) bool {
		payload, err := json.Marshal(value)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	model, err := s.ttsModels.Install(r.Context(), request.ID, func(progress ttspkg.Progress) {
		writeEvent(map[string]any{"type": "progress", "progress": progress})
	})
	if err != nil {
		writeEvent(map[string]any{"type": "error", "error": err.Error()})
		return
	}
	writeEvent(map[string]any{"type": "result", "result": model})
}
