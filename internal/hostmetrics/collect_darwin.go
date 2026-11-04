//go:build darwin

package hostmetrics

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"golang.org/x/net/route"
	"golang.org/x/sys/unix"
)

const darwinCommandTimeout = 3 * time.Second

type darwinSource struct{}

func newPlatformSource() source { return darwinSource{} }

func (darwinSource) supported() bool { return true }

func (darwinSource) info() HostInfo {
	info := HostInfo{
		OS: runtime.GOOS, Arch: runtime.GOARCH, CPUCores: runtime.NumCPU(),
		Platform: "macOS", PlatformID: "macos",
	}
	info.Hostname, _ = os.Hostname()
	if version, err := unix.Sysctl("kern.osproductversion"); err == nil && strings.TrimSpace(version) != "" {
		info.Platform = "macOS " + strings.TrimSpace(version)
	}
	if release, err := unix.Sysctl("kern.osrelease"); err == nil {
		info.Kernel = strings.TrimSpace(release)
	}
	if boot, err := unix.SysctlTimeval("kern.boottime"); err == nil {
		info.BootTime = boot.Sec
	}
	return info
}

func (darwinSource) read() reading {
	out := reading{at: time.Now()}
	if output, err := runQuiet("/bin/ps", "-A", "-o", "%cpu="); err == nil {
		out.cpuPercent = parsePSCPU(output, runtime.NumCPU())
	} else {
		out.warnings = append(out.warnings, "cpu: "+err.Error())
	}
	if raw, err := unix.SysctlRaw("vm.loadavg"); err == nil {
		out.load = parseLoadAvgSysctl(raw)
	}
	if total, err := unix.SysctlUint64("hw.memsize"); err == nil {
		if output, err := runQuiet("/usr/bin/vm_stat"); err == nil {
			out.memory = parseVMStat(output, total)
		} else {
			out.warnings = append(out.warnings, "memory: "+err.Error())
		}
		if out.memory != nil {
			if raw, err := unix.SysctlRaw("vm.swapusage"); err == nil {
				out.memory.SwapTotal, out.memory.SwapUsed, _ = parseSwapUsage(raw)
			}
		}
	}
	// NET_RT_IFLIST2 carries 64-bit byte counters; the name-based sysctl
	// lookup cannot address the routing tree, so use the numeric MIB.
	if raw, err := route.FetchRIB(unix.AF_UNSPEC, route.RIBType(unix.NET_RT_IFLIST2), 0); err == nil {
		out.net = parseIfList2(raw)
	} else {
		out.warnings = append(out.warnings, "network: "+err.Error())
	}
	out.disks = darwinDisks()
	return out
}

// darwinDisks reports the APFS data volume, which holds user data and whose
// statfs counts reflect the whole container's free space.
func darwinDisks() []DiskStats {
	for _, mount := range []string{"/System/Volumes/Data", "/"} {
		var stat unix.Statfs_t
		if unix.Statfs(mount, &stat) != nil {
			continue
		}
		fsType := unix.ByteSliceToString(stat.Fstypename[:])
		if disk, ok := diskStats(mount, fsType, uint64(stat.Bsize), stat.Blocks, stat.Bfree, stat.Bavail); ok {
			return []DiskStats{disk}
		}
	}
	return []DiskStats{}
}

func runQuiet(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), darwinCommandTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	// A fixed locale keeps decimal separators parseable.
	command.Env = append(os.Environ(), "LC_ALL=C")
	output, err := command.Output()
	return string(output), err
}
