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
	s.mux.HandleFunc("GET /api/v1/remote/discovered-hosts", s.handleRemoteDiscoveredHosts)
	s.mux.HandleFunc("POST /api/v1/remote/hosts", s.handleRemoteHostUpsert)
	s.mux.HandleFunc("DELETE /api/v1/remote/hosts", s.handleRemoteHostDelete)
	s.mux.HandleFunc("POST /api/v1/remote/hosts/test", s.handleRemoteHostTest)
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		s.mux.HandleFunc(method+" /api/v1/remote/proxy/{id}/{path...}", s.handleRemoteProxy)
	}
}

func (s *Server) handleRemoteHostsList(w http.ResponseWriter, _ *http.Request) {
	if s.remote == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "remote SSH control unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, s.remote.List())
}

func (s *Server) handleRemoteDiscoveredHosts(w http.ResponseWriter, _ *http.Request) {
	hosts, err := remotepkg.DiscoverSSHHosts("")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, hosts)
}

func (s *Server) handleRemoteHostUpsert(w http.ResponseWriter, r *http.Request) {
	if s.remote == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "remote SSH control unavailable"})
		return
	}
	var req struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		Host          string `json:"host"`
		Port          int    `json:"port"`
		User          string `json:"user"`
		KeyPath       string `json:"key_path"`
		RemoteAddr    string `json:"remote_addr"`
		APIToken      string `json:"api_token"`
		ClearAPIToken bool   `json:"clear_api_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	host, err := s.remote.Upsert(remotepkg.Host{
		ID: req.ID, Name: req.Name, Host: req.Host, Port: req.Port,
		User: req.User, KeyPath: req.KeyPath, RemoteAddr: req.RemoteAddr,
		APIToken: req.APIToken,
	}, req.ClearAPIToken)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, host)
}

func (s *Server) handleRemoteHostDelete(w http.ResponseWriter, r *http.Request) {
	if s.remote == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "remote SSH control unavailable"})
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
		return
	}
	if err := s.remote.Delete(id); err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, os.ErrNotExist) {
			code = http.StatusNotFound
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleRemoteHostTest(w http.ResponseWriter, r *http.Request) {
	if s.remote == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "remote SSH control unavailable"})
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
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
		code := http.StatusBadGateway
		var unknown *remotepkg.UnknownHostKeyError
		switch {
		case errors.Is(err, os.ErrNotExist):
			code = http.StatusNotFound
		case errors.As(err, &unknown):
			code = http.StatusConflict
			writeJSON(w, code, map[string]any{
				"error": unknown.Error(), "code": "host_key_untrusted",
				"host_key_fingerprint": unknown.Fingerprint,
			})
			return
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleRemoteProxy(w http.ResponseWriter, r *http.Request) {
	if s.remote == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "remote SSH control unavailable"})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	path := strings.TrimPrefix(r.PathValue("path"), "/")
	if id == "" || path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "remote host and API path are required"})
		return
	}
	if path == "remote" || strings.HasPrefix(path, "remote/") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "nested remote proxying is not allowed"})
		return
	}
	host, ok := s.remote.Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "remote host not found"})
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
