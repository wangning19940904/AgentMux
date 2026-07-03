// Command agentnexus-desktop is the Wails v2 desktop shell. It starts the
// AgentNexus daemon in-process and renders the same React WebUI (web/dist)
// inside a native WebView, adding a system tray for quick provider switching.
//
// Build (requires the Wails v2 toolchain and a built web/dist):
//
//	cd web && npm run build
//	wails build            # uses wails.json in this directory
//
// This file is excluded from the plain `go build ./...` of the CLI module via
// the `desktop` build tag so the core build never requires the Wails toolchain.
//go:build desktop

package main

import (
	"context"
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := newApp()
	err := wails.Run(&options.App{
		Title:  "AgentNexus",
		Width:  1100,
		Height: 720,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		Bind:       []any{app},
	})
	if err != nil {
		log.Fatal(err)
	}
}

// App holds desktop lifecycle state. The daemon runs in-process so the WebView
// hits the same /api endpoints as the browser WebUI.
type App struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func newApp() *App { return &App{} }
