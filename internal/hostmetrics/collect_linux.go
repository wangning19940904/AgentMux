//go:build linux

package hostmetrics

import (
	"os"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type linuxSource struct{}

func newPlatformSource() source { return linuxSource{} }

func (linuxSource) supported() bool { return true }

func (linuxSource) info() HostInfo {
	info := HostInfo{OS: runtime.GOOS, Arch: runtime.GOARCH, CPUCores: runtime.NumCPU(), Platform: "Linux"}
	info.Hostname, _ = os.Hostname()
	for _, path := range []string{"/etc/os-release", "/usr/lib/os-release"} {
		if data, err := os.ReadFile(path); err == nil {
			pretty, id := parseOSRelease(string(data))
			if pretty != "" {
				info.Platform = pretty
			}
			info.PlatformID = id
			break
		}
	}
	var uname unix.Utsname
	if unix.Uname(&uname) == nil {
		info.Kernel = unix.ByteSliceToString(uname.Release[:])
	}
	if data, err := os.ReadFile("/proc/stat"); err == nil {
		_, info.BootTime = parseProcStat(string(data))
	}
	return info
}

func (linuxSource) read() reading {
	out := reading{at: time.Now()}
	if data, err := os.ReadFile("/proc/stat"); err == nil {
		out.cpuTicks, _ = parseProcStat(string(data))
	} else {
		out.warnings = append(out.warnings, "cpu: "+err.Error())
	}
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		out.load = parseLoadAvg(string(data))
	}
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		out.memory = parseMemInfo(string(data))
	} else {
		out.warnings = append(out.warnings, "memory: "+err.Error())
	}
	if data, err := os.ReadFile("/proc/net/dev"); err == nil {
		out.net = sumNetDev(parseNetDev(string(data)), physicalInterface)
	}
	out.disks = linuxDisks()
	return out
}

// physicalInterface reports whether a network interface is backed by a
// device. Bridges, veth pairs, tunnels and loopback have no device link.
func physicalInterface(name string) bool {
	if name == "" || strings.ContainsAny(name, "/.") {
		return false
	}
	_, err := os.Stat("/sys/class/net/" + name + "/device")
	return err == nil
}

func linuxDisks() []DiskStats {
	data, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		data = nil
	}
	mounts := parseMounts(string(data))
	if len(mounts) == 0 {
		mounts = []mountEntry{{mount: "/"}}
	}
	disks := make([]DiskStats, 0, len(mounts))
	for _, mount := range mounts {
		var stat unix.Statfs_t
		if unix.Statfs(mount.mount, &stat) != nil {
			continue
		}
		blockSize := uint64(stat.Frsize)
		if blockSize == 0 {
			blockSize = uint64(stat.Bsize)
		}
		if disk, ok := diskStats(mount.mount, mount.fsType, blockSize, stat.Blocks, stat.Bfree, stat.Bavail); ok {
			disks = append(disks, disk)
		}
	}
	return disks
}
