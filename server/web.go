package server

import (
	"io/fs"
	"net/http"
	"strings"
	"time"

	webdist "github.com/wangning19940904/AgentMux/web"
)

// registerWeb mounts the embedded WebUI when present (release builds with the
// `embedweb` tag); otherwise it serves a minimal placeholder so the server is
// usable standalone during development.
func (s *Server) registerWeb(mux *http.ServeMux) {
	if dist, ok := webdist.FS(); ok {
		s.serveSPA(mux, dist)
		return
	}
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		if devBase, ok := detectDevWebServer(); ok {
			http.Redirect(w, r, devBase+r.URL.RequestURI(), http.StatusTemporaryRedirect)
			return
		}
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(placeholderHTML))
	})
}

func detectDevWebServer() (string, bool) {
	const devBase = "http://127.0.0.1:5173"
	client := http.Client{Timeout: 200 * time.Millisecond}
	res, err := client.Head(devBase + "/")
	if err != nil {
		return "", false
	}
	defer res.Body.Close()
	return devBase, res.StatusCode >= 200 && res.StatusCode < 500
}

// serveSPA serves static assets from dist and falls back to index.html for
// client-side routes.
func (s *Server) serveSPA(mux *http.ServeMux, dist fs.FS) {
	fileServer := http.FileServer(http.FS(dist))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(dist, trimLeadingSlash(r.URL.Path)); err == nil && r.URL.Path != "/" {
			fileServer.ServeHTTP(w, r)
			return
		}
		// SPA fallback: serve index.html for unknown routes.
		index, err := fs.ReadFile(dist, "index.html")
		if err != nil {
			http.Error(w, "index.html missing", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})
}

func trimLeadingSlash(p string) string {
	if len(p) > 0 && p[0] == '/' {
		return p[1:]
	}
	return p
}

const placeholderHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>AgentMux</title>
<style>body{font-family:system-ui,sans-serif;background:#0d1117;color:#c9d1d9;margin:0;display:grid;place-items:center;height:100vh}
.card{max-width:560px;padding:2rem;border:1px solid #30363d;border-radius:12px;background:#161b22}
h1{margin:0 0 .5rem;font-size:1.4rem}code{background:#0d1117;padding:.1rem .35rem;border-radius:4px}</style>
</head><body><div class="card">
<h1>AgentMux</h1>
<p>Daemon is running. The React WebUI is bundled in release builds (built with <code>-tags embedweb</code>); in dev run the Vite dev server with <code>npm run dev</code>.</p>
<p>API base: <code>/api/v1</code> &middot; status: <code>/api/v1/status</code></p>
</div></body></html>`
