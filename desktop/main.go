//go:build desktop
// +build desktop

// Command agentnexus-desktop is the Wails v2 desktop shell. It starts the
// AgentNexus daemon in-process and renders the same React WebUI (web/dist)
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
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := newApp()
	err := wails.Run(&options.App{
		Title:             "AgentNexus",
		Width:             1100,
		Height:            720,
		HideWindowOnClose: true,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		Bind:       []any{app},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "com.agentnexus.desktop",
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
}

func newApp() *App { return &App{} }
