//go:build !linux && !darwin

package hostmetrics

import (
	"os"
	"runtime"
	"time"
)

// otherSource describes the host on platforms without a collector so the
// Console can still show the machine and explain why metrics are missing.
type otherSource struct{}

func newPlatformSource() source { return otherSource{} }

func (otherSource) supported() bool { return false }

func (otherSource) info() HostInfo {
	info := HostInfo{OS: runtime.GOOS, Arch: runtime.GOARCH, CPUCores: runtime.NumCPU(), Platform: runtime.GOOS}
	info.Hostname, _ = os.Hostname()
	return info
}

func (otherSource) read() reading { return reading{at: time.Now()} }
