package server

import (
	"net/http"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
)

// lookupAgent resolves an agent for a handler, writing 404 (or 503 without a
// store) and returning false when it cannot.
func (s *Server) lookupAgent(w http.ResponseWriter, r *http.Request, agentID string) (*core.AgentInstance, bool) {
	if s.st == nil {
		writeErr(w, http.StatusServiceUnavailable, "store unavailable")
		return nil, false
	}
	// config.toml agents live outside the store.
	if strings.HasPrefix(agentID, "config:") {
		agent, found, err := s.findAgentInstance(r.Context(), agentID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return nil, false
		}
		if !found {
			writeErr(w, http.StatusNotFound, "agent not found")
			return nil, false
		}
		return &agent, true
	}
	agent, err := s.st.GetAgentInstance(r.Context(), agentID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	if agent == nil {
		writeErr(w, http.StatusNotFound, "agent not found")
		return nil, false
	}
	return agent, true
}

// lookupChannel mirrors lookupAgent for channels.
func (s *Server) lookupChannel(w http.ResponseWriter, r *http.Request, channelID string) (*core.Channel, bool) {
	if s.st == nil {
		writeErr(w, http.StatusServiceUnavailable, "store unavailable")
		return nil, false
	}
	channel, err := s.st.GetChannel(r.Context(), channelID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	if channel == nil {
		writeErr(w, http.StatusNotFound, "channel not found")
		return nil, false
	}
	return channel, true
}

// lookupTrigger mirrors lookupAgent for triggers.
func (s *Server) lookupTrigger(w http.ResponseWriter, r *http.Request, triggerID string) (*core.Trigger, bool) {
	if s.st == nil {
		writeErr(w, http.StatusServiceUnavailable, "store unavailable")
		return nil, false
	}
	trigger, err := s.st.GetTrigger(r.Context(), triggerID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	if trigger == nil {
		writeErr(w, http.StatusNotFound, "trigger not found")
		return nil, false
	}
	return trigger, true
}

// writeNotVisible answers as not found rather than confirming that a
// resource the caller may not see exists.
func writeNotVisible(w http.ResponseWriter, kind string) {
	writeErr(w, http.StatusNotFound, kind+" not found")
}
