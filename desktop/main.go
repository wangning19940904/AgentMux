//go:build desktop
// +build desktop

// Command agentmux-desktop is the Wails v2 desktop shell. It starts the
// AgentMux daemon in-process and renders the same React WebUI (web/dist)
// inside a native WebView. On macOS it also launches the bundled menu bar
// helper so the app remains reachable after the main window is closed.
//
// Build (requires the Wails v2 toolchain and a built web/dist):
//
//	cd web && npm run build
//	wails build            # uses wails.json in this directory
//
// This file is excluded from the plain `go build ./...` of the CLI module via
// the `desktop` build tag so the core build never requires the Wails toolchain.

package main

import (
	"context"
	"embed"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend/dist
var assets embed.FS

// version is injected by the release build so the desktop daemon and bundled
// remote artifacts expose the same status version.
var version = "0.1.0"

func main() {
	app := newApp()
	err := wails.Run(&options.App{
		Title:             "AgentMux",
		Width:             1100,
		Height:            720,
		HideWindowOnClose: true,
		AssetServer: &assetserver.Options{
			Assets:     assets,
			Middleware: app.assetServerMiddleware,
		},
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		Bind:       []any{app},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "com.agentmux.desktop",
			OnSecondInstanceLaunch: func(_ options.SecondInstanceData) {
				if app.ctx == nil {
					return
				}
				wailsruntime.WindowShow(app.ctx)
				wailsruntime.WindowUnminimise(app.ctx)
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
}

// App holds desktop lifecycle state. The daemon runs in-process so the WebView
// hits the same /api endpoints as the browser WebUI.
type App struct {
	ctx     context.Context
	cancel  context.CancelFunc
	menubar *menuBarProcess

	// The WebView talks to /api on its own wails.localhost origin. Wails hands
	// those requests to apiProxy, which reaches the in-process loopback daemon
	// from Go. Keeping HTTP out of WebKit avoids macOS mixed-content/ATS failures
	// that otherwise surface in the UI as the opaque "TypeError: Load failed".
	apiTarget atomic.Value // *url.URL
	apiProxy  *httputil.ReverseProxy
}

func newApp() *App {
	app := &App{}
	app.apiTarget.Store(desktopAPITarget("127.0.0.1:8765"))
	app.apiProxy = &httputil.ReverseProxy{
		Director: func(request *http.Request) {
			target := app.apiTarget.Load().(*url.URL)
			request.URL.Scheme = target.Scheme
			request.URL.Host = target.Host
			request.Host = target.Host
		},
		ErrorHandler: func(response http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(response, "desktop API is starting", http.StatusServiceUnavailable)
		},
	}
	return app
}

func (a *App) assetServerMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api" || strings.HasPrefix(request.URL.Path, "/api/") {
			if isDesktopObservationSessionPath(request.URL.Path) {
				// This middleware is the native trust boundary. Preserve the
				// desktop marker even if a WebView strips custom fetch headers.
				request.Header.Set("X-AgentMux-Desktop", "1")
				if strings.TrimSpace(request.Header.Get("Origin")) == "" {
					request.Header.Set("Origin", "wails://wails.localhost")
				}
			}
			a.apiProxy.ServeHTTP(response, request)
			return
		}
		next.ServeHTTP(response, request)
	})
}

func isDesktopObservationSessionPath(path string) bool {
	if path == "/api/v1/observability/session" {
		return true
	}
	return strings.HasPrefix(path, "/api/v1/remote/proxy/") &&
		strings.HasSuffix(path, "/observability/session")
}

func (a *App) setAPITarget(addr string) {
	a.apiTarget.Store(desktopAPITarget(addr))
}

// OpenLocalWebUI opens the in-process daemon's Web UI in the user's default
// browser. The target follows the configured server address and is normalized
// to loopback when the daemon listens on a wildcard interface.
func (a *App) OpenLocalWebUI() {
	if a.ctx == nil {
		return
	}
	wailsruntime.BrowserOpenURL(a.ctx, a.localWebUIURL())
}

// OpenExternalURL opens a validated web link in the user's default browser.
// Login links contain short-lived OAuth/device challenges and must leave the
// embedded WebView without allowing arbitrary local schemes.
func (a *App) OpenExternalURL(raw string) error {
	if a.ctx == nil {
		return fmt.Errorf("desktop app is not ready")
	}
	target, err := externalBrowserURL(raw)
	if err != nil {
		return err
	}
	wailsruntime.BrowserOpenURL(a.ctx, target)
	return nil
}

func externalBrowserURL(raw string) (string, error) {
	target, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || target.Host == "" || target.Scheme != "https" && target.Scheme != "http" {
		return "", fmt.Errorf("invalid external web URL")
	}
	return target.String(), nil
}

func (a *App) localWebUIURL() string {
	return a.apiTarget.Load().(*url.URL).String()
}

func desktopAPITarget(addr string) *url.URL {
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil || port == "" {
		host, port = "127.0.0.1", "8765"
	}
	switch strings.Trim(host, "[]") {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return &url.URL{Scheme: "http", Host: net.JoinHostPort(host, port)}
}
