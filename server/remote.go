package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"

	remotepkg "github.com/wangning19940904/AgentMux/remote"
)

func (s *Server) registerRemoteRoutes() {
	s.mux.HandleFunc("GET /api/v1/remote/hosts", s.handleRemoteHostsList)
	s.mux.HandleFunc("GET /api/v1/remote/meetings", s.handleMeetingAggregate)
	s.mux.HandleFunc("GET /api/v1/remote/meetings/events", s.handleMeetingAggregateEvents)
	s.mux.HandleFunc("GET /api/v1/remote/meetings/activity", s.handleRemoteMeetingActivity)
	s.mux.HandleFunc("POST /api/v1/remote/meetings/messages", s.handleRemoteMeetingMessageSend)
	s.mux.HandleFunc("POST /api/v1/remote/meetings/questions", s.handleRemoteMeetingQuestion)
	s.mux.HandleFunc("POST /api/v1/remote/meetings/response-mode", s.handleRemoteMeetingResponseMode)
	s.mux.HandleFunc("POST /api/v1/remote/meetings/invitations/respond", s.handleRemoteMeetingInvitationRespond)
	s.mux.HandleFunc("POST /api/v1/remote/meetings/join", s.handleRemoteMeetingJoin)
	s.mux.HandleFunc("POST /api/v1/remote/channels/claim", s.handleChannelClaim)
	s.mux.HandleFunc("GET /api/v1/remote/directories", s.handleRemoteDirectoryList)
	s.mux.HandleFunc("POST /api/v1/remote/directories", s.handleRemoteDirectoryEnsure)
	s.mux.HandleFunc("GET /api/v1/remote/discovered-hosts", s.handleRemoteDiscoveredHosts)
	s.mux.HandleFunc("POST /api/v1/remote/hosts", s.handleRemoteHostUpsert)
	s.mux.HandleFunc("POST /api/v1/remote/hosts/sync-ssh-config", s.handleRemoteHostsSyncSSHConfig)
	s.mux.HandleFunc("POST /api/v1/remote/hosts/import", s.handleRemoteHostImport)
	s.mux.HandleFunc("DELETE /api/v1/remote/hosts", s.handleRemoteHostDelete)
	s.mux.HandleFunc("POST /api/v1/remote/hosts/test", s.handleRemoteHostTest)
	s.mux.HandleFunc("POST /api/v1/remote/hosts/status", s.handleRemoteHostStatus)
	s.mux.HandleFunc("POST /api/v1/remote/hosts/update", s.handleRemoteHostUpdate)
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		s.mux.HandleFunc(method+" /api/v1/remote/proxy/{id}/{path...}", s.handleRemoteProxy)
	}
}

func (s *Server) handleRemoteDirectoryList(w http.ResponseWriter, r *http.Request) {
	if s.remote == nil {
		writeErr(w, http.StatusServiceUnavailable, "remote SSH control unavailable")
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	listing, err := s.remote.ListDirectories(r.Context(), id, r.URL.Query().Get("path"))
	if err != nil {
		writeRemoteFilesystemError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, listing)
}

func (s *Server) handleRemoteDirectoryEnsure(w http.ResponseWriter, r *http.Request) {
	if s.remote == nil {
		writeErr(w, http.StatusServiceUnavailable, "remote SSH control unavailable")
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	var req systemDirectoryRequest
	if !decodeJSONInto(w, r, &req) {
		return
	}
	path, err := s.remote.EnsureDirectory(r.Context(), id, req.Path)
	if err != nil {
		writeRemoteFilesystemError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, systemDirectoryResponse{Path: path})
}

func writeRemoteFilesystemError(w http.ResponseWriter, err error) {
	code := http.StatusBadGateway
	if errors.Is(err, os.ErrNotExist) {
		code = http.StatusNotFound
	}
	writeErr(w, code, err.Error())
}

func (s *Server) handleRemoteHostImport(w http.ResponseWriter, r *http.Request) {
	if s.remote == nil {
		writeErr(w, http.StatusServiceUnavailable, "remote SSH control unavailable")
		return
	}
	var req struct {
		Name            string `json:"name"`
		Host            string `json:"host"`
		Port            int    `json:"port"`
		User            string `json:"user"`
		KeyPath         string `json:"key_path"`
		SSHAlias        string `json:"ssh_alias"`
		RemoteAddr      string `json:"remote_addr"`
		APIToken        string `json:"api_token"`
		TrustOnFirstUse bool   `json:"trust_on_first_use"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	result, err := s.remote.Import(r.Context(), remotepkg.Host{
		Name: req.Name, Host: req.Host, Port: req.Port, User: req.User,
		KeyPath: req.KeyPath, SSHAlias: req.SSHAlias, RemoteAddr: req.RemoteAddr, APIToken: req.APIToken,
	}, req.TrustOnFirstUse)
	if err != nil {
		writeRemoteConnectionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleRemoteHostsList(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.remote == nil {
		writeErr(w, http.StatusServiceUnavailable, "remote SSH control unavailable")
		return
	}
	writeJSON(w, http.StatusOK, s.remote.List())
}

func (s *Server) handleRemoteDiscoveredHosts(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	hosts, err := remotepkg.DiscoverSSHHosts("")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, hosts)
}

func (s *Server) handleRemoteHostsSyncSSHConfig(w http.ResponseWriter, _ *http.Request) {
	if s.remote == nil {
		writeErr(w, http.StatusServiceUnavailable, "remote SSH control unavailable")
		return
	}
	hosts, err := remotepkg.DiscoverSSHHosts("")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	result, err := s.remote.SyncSSHConfig(hosts)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleRemoteHostUpsert(w http.ResponseWriter, r *http.Request) {
	if s.remote == nil {
		writeErr(w, http.StatusServiceUnavailable, "remote SSH control unavailable")
		return
	}
	var req struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		Host          string `json:"host"`
		Port          int    `json:"port"`
		User          string `json:"user"`
		KeyPath       string `json:"key_path"`
		SSHAlias      string `json:"ssh_alias"`
		RemoteAddr    string `json:"remote_addr"`
		APIToken      string `json:"api_token"`
		ClearAPIToken bool   `json:"clear_api_token"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	host, err := s.remote.Upsert(remotepkg.Host{
		ID: req.ID, Name: req.Name, Host: req.Host, Port: req.Port,
		User: req.User, KeyPath: req.KeyPath, SSHAlias: req.SSHAlias, RemoteAddr: req.RemoteAddr,
		APIToken: req.APIToken,
	}, req.ClearAPIToken)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, host)
}

func (s *Server) handleRemoteHostDelete(w http.ResponseWriter, r *http.Request) {
	if s.remote == nil {
		writeErr(w, http.StatusServiceUnavailable, "remote SSH control unavailable")
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := s.remote.Delete(id); err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, os.ErrNotExist) {
			code = http.StatusNotFound
		}
		writeErr(w, code, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleRemoteHostTest(w http.ResponseWriter, r *http.Request) {
	if s.remote == nil {
		writeErr(w, http.StatusServiceUnavailable, "remote SSH control unavailable")
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	var req struct {
		TrustOnFirstUse bool `json:"trust_on_first_use"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	result, err := s.remote.Test(r.Context(), id, req.TrustOnFirstUse)
	if err != nil {
		writeRemoteConnectionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleRemoteHostStatus(w http.ResponseWriter, r *http.Request) {
	if s.remote == nil {
		writeErr(w, http.StatusServiceUnavailable, "remote SSH control unavailable")
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	result, err := s.remote.Status(r.Context(), id)
	if err != nil {
		writeRemoteConnectionError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleRemoteHostUpdate(w http.ResponseWriter, r *http.Request) {
	if s.remote == nil {
		writeErr(w, http.StatusServiceUnavailable, "remote SSH control unavailable")
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	result, err := s.remote.Update(r.Context(), id)
	if err != nil {
		writeRemoteConnectionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func writeRemoteConnectionError(w http.ResponseWriter, err error) {
	code := http.StatusBadGateway
	var unknown *remotepkg.UnknownHostKeyError
	switch {
	case errors.Is(err, os.ErrNotExist):
		code = http.StatusNotFound
	case errors.As(err, &unknown):
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": unknown.Error(), "code": "host_key_untrusted",
			"host_key_fingerprint": unknown.Fingerprint,
		})
		return
	}
	writeErr(w, code, err.Error())
}

func (s *Server) handleRemoteProxy(w http.ResponseWriter, r *http.Request) {
	if s.remote == nil {
		writeErr(w, http.StatusServiceUnavailable, "remote SSH control unavailable")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	path := strings.TrimPrefix(r.PathValue("path"), "/")
	if id == "" || path == "" {
		writeErr(w, http.StatusBadRequest, "remote host and API path are required")
		return
	}
	if path == "remote" || strings.HasPrefix(path, "remote/") {
		writeErr(w, http.StatusForbidden, "nested remote proxying is not allowed")
		return
	}
	host, ok := s.remote.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "remote host not found")
		return
	}
	targetPath := "/api/v1/" + path
	target := &url.URL{Scheme: "http", Host: host.RemoteAddr}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return s.remote.DialContext(ctx, id, network)
		},
	}
	defer transport.CloseIdleConnections()
	proxy := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(target)
			request.Out.URL.Path = targetPath
			request.Out.URL.RawPath = ""
			request.Out.Host = host.RemoteAddr
			if !strings.HasPrefix(path, "observability/") {
				request.Out.Header.Del("Authorization")
				if host.APIToken != "" {
					request.Out.Header.Set("Authorization", "Bearer "+host.APIToken)
				}
			}
			request.Out.Header.Set("X-AgentMux-SSH-Host", host.Name)
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			if s.log != nil {
				s.log.Warn("remote SSH proxy failed", "remote", host.Name, "err", err)
			}
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"error": fmt.Sprintf("remote %s: %v", host.Name, err),
			})
		},
	}
	proxy.ServeHTTP(w, r)
}
