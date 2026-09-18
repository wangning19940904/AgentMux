//go:build desktop && darwin

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#include "../../dock_darwin.h"
void agentmuxRunDockSmoke(void);
int agentmuxDockSmokeCompleted(void);
*/
import "C"

import (
	"context"
	"fmt"
	"testing/fstest"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

func main() {
	var ctx context.Context
	quitSeen := make(chan struct{}, 1)
	err := wails.Run(&options.App{
		Title: "AgentMux Dock Smoke Test", Width: 400, Height: 240,
		HideWindowOnClose: true,
		AssetServer: &assetserver.Options{Assets: fstest.MapFS{
			"index.html": &fstest.MapFile{Data: []byte("<!doctype html><html><body>AgentMux Dock lifecycle test</body></html>")},
		}},
		OnStartup: func(c context.Context) {
			ctx = c
			C.agentmuxConfigureDock()
			go func() {
				time.Sleep(time.Second)
				C.agentmuxRunDockSmoke()
			}()
		},
		OnBeforeClose: func(context.Context) bool {
			quitSeen <- struct{}{}
			return false
		},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "com.agentmux.dock-smoke",
			OnSecondInstanceLaunch: func(options.SecondInstanceData) {
				wailsruntime.WindowShow(ctx)
				wailsruntime.WindowUnminimise(ctx)
			},
		},
	})
	if err != nil {
		panic(err)
	}
	if C.agentmuxDockSmokeCompleted() != 1 {
		panic("app quit before the Dock lifecycle test completed")
	}
	select {
	case <-quitSeen:
		fmt.Println("PASS: explicit quit reaches Wails")
	default:
		panic("quit did not reach the Wails lifecycle")
	}
}
