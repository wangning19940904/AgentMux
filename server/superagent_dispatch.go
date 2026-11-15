package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	linkpkg "github.com/wangning19940904/AgentMux/link"
	"github.com/wangning19940904/AgentMux/superagent"
)

// A Ticket's worker starts and reads helpers through its own machine's API
// (superagent/dispatch.go). The routes take no account credential: the
// Ticket's token in the body is the credential, and it is good for that
// Ticket only. A machine that does not hold the Ticket passes the call up its
// hub link.

const maxDispatchBodyBytes = 256 << 10

func (s *Server) registerDispatchRoutes() {
	s.mux.HandleFunc("POST "+superagent.DispatchStartPath, s.handleDispatch(superagent.DispatchCallStart, true))
	s.mux.HandleFunc("POST "+superagent.DispatchReadPath, s.handleDispatch(superagent.DispatchCallRead, true))
}

// registerDispatchUplink takes calls for the hub's own agents from workers on
// linked machines. They are answered here and never passed on.
func (s *Server) registerDispatchUplink(mux *http.ServeMux) {
	uplink := func(call string) http.HandlerFunc {
		handle := s.handleDispatch(call, false)
		return func(w http.ResponseWriter, r *http.Request) {
			if linkpkg.DeviceID(r.Context()) == "" {
				writeErr(w, http.StatusUnauthorized, "dispatch calls come up a device link")
				return
			}
			handle(w, r)
		}
	}
	mux.HandleFunc("POST "+superagent.DispatchUplinkStartPath, uplink(superagent.DispatchCallStart))
	mux.HandleFunc("POST "+superagent.DispatchUplinkReadPath, uplink(superagent.DispatchCallRead))
}

func (s *Server) handleDispatch(call string, forward bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.requireSuperAgents(w) {
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxDispatchBodyBytes))
		if err != nil {
			writeErr(w, http.StatusRequestEntityTooLarge, "the call is too large")
			return
		}
		var request superagent.DispatchRequest
		if err := json.Unmarshal(body, &request); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		result, err := s.superAgents.Dispatch(r.Context(), call, request)
		var elsewhere superagent.DispatchElsewhere
		var refused superagent.DispatchError
		switch {
		case err == nil:
			writeJSON(w, http.StatusOK, result)
		case errors.As(err, &elsewhere) && forward:
			status, data, forwardErr := s.superAgents.ForwardDispatch(r.Context(), call, elsewhere.Home, body)
			if forwardErr != nil {
				writeErr(w, http.StatusBadGateway, forwardErr.Error())
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write(data)
		case errors.As(err, &elsewhere):
			writeErr(w, http.StatusNotFound, "the token names no Ticket here")
		case errors.As(err, &refused):
			writeErr(w, refused.Status, refused.Message)
		default:
			writeErr(w, http.StatusInternalServerError, err.Error())
		}
	}
}

// dispatchHost is how this machine's workers reach it.
func (s *Server) dispatchHost() superagent.DispatchHost {
	return superagent.DispatchHost{
		Home: s.dispatchHome, Uplink: s.HubRelay(),
		Addr: linkpkg.LoopbackAddr(s.cfg.Server.Addr), Command: amuxCommand(),
	}
}

// dispatchHome names this machine to its hub: its device id on a hub, "hub"
// when it is one, else nothing.
func (s *Server) dispatchHome() string {
	if s.node != nil {
		if status, joined := s.node.status(); joined && status.DeviceID != "" {
			return status.DeviceID
		}
	}
	if strings.TrimSpace(s.cfg.Hub.Addr) != "" {
		return superagent.DispatchHomeHub
	}
	return ""
}

// amuxCommand finds the amux command a local worker can run: this program
// when it is amux, the one the desktop app carries, or amux on PATH.
func amuxCommand() string {
	if executable, err := os.Executable(); err == nil {
		base := strings.TrimSuffix(filepath.Base(executable), ".exe")
		if base == "amux" || strings.HasPrefix(base, "amux-") {
			return shellWord(executable)
		}
		bundled := filepath.Join(filepath.Dir(executable), "..", "Resources", "agentmux-remote", "amux-"+runtime.GOOS+"-"+runtime.GOARCH)
		if info, err := os.Stat(bundled); err == nil && !info.IsDir() {
			return shellWord(filepath.Clean(bundled))
		}
	}
	if path, err := exec.LookPath("amux"); err == nil {
		return shellWord(path)
	}
	return ""
}

func shellWord(value string) string {
	if !strings.ContainsAny(value, " '\"$`\\") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
