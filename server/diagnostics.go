package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/diagnostics"
)

// The Console's "Download diagnostics" button: the same redacted zip as
// `amux diagnose`, built by the diagnostics package and handed to the
// browser. Nothing is uploaded anywhere.

func (s *Server) registerDiagnosticsRoutes() {
	s.mux.HandleFunc("GET /api/v1/diagnostics", s.handleDiagnostics)
}

func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	home, _ := os.UserHomeDir()
	configPath, _, err := config.ResolvePath("")
	if err != nil {
		configPath = "(as loaded by the daemon)"
	}
	opts := diagnostics.Options{
		Version:        s.version,
		Config:         s.cfg,
		ConfigPath:     configPath,
		Home:           home,
		Environ:        os.Environ(),
		IncludeContent: r.URL.Query().Get("include_content") == "1",
		Origin:         "console",
		Daemon:         s.selfFetcher(r),
	}
	if s.st != nil {
		opts.Store = s.st
	} else {
		opts.StoreError = errors.New("this AgentMux runs without a database")
	}
	bundle, err := diagnostics.Build(r.Context(), opts)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	files, _ := json.Marshal(bundle.Files)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", diagnostics.FileName(time.Now())))
	w.Header().Set("X-AgentMux-Diagnostics-Files", string(files))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(bundle.Zip)
}

// selfFetcher reads this server's own GET endpoints in-process, as the
// administrator the request was already authorized as.
func (s *Server) selfFetcher(parent *http.Request) diagnostics.Fetcher {
	return func(ctx context.Context, path string) ([]byte, error) {
		req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
		req.RemoteAddr = parent.RemoteAddr
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, req)
		if rec.Code >= 300 {
			return nil, fmt.Errorf("%d %s", rec.Code, http.StatusText(rec.Code))
		}
		return rec.Body.Bytes(), nil
	}
}
