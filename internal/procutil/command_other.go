//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package procutil

import "os/exec"

// Prepare keeps the platform default cancellation behavior where Unix process
// groups and signals are unavailable.
func Prepare(_ *exec.Cmd) {}
