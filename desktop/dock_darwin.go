//go:build desktop && darwin

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#include "dock_darwin.h"
*/
import "C"

// Wails hides the application on close but leaves its Dock icon visible.
// Install the native lifecycle bridge before the window becomes interactive.
func configureDesktopDock() {
	C.agentmuxConfigureDock()
}
