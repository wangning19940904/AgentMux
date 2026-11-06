// Package hostmetrics samples the resource usage of the machine running
// AgentMux: CPU, load, memory, disks and network throughput, plus a short
// history for sparklines. Sampling starts on the first request and stops by
// itself when nobody has asked for a while, so an unattended daemon pays
// nothing for it.
package hostmetrics

import "time"

// HostInfo describes the machine itself. It rarely changes.
type HostInfo struct {
	Hostname string `json:"hostname"`
	// OS is the Go operating system name: linux, darwin, windows, ...
	OS string `json:"os"`
	// Platform is the human-readable release, such as
	// "Debian GNU/Linux 12 (bookworm)" or "macOS 15.6.1".
	Platform string `json:"platform"`
	// PlatformID is the lowercase distribution id (os-release ID on Linux,
	// "macos" on macOS) so clients can pick an icon.
	PlatformID string `json:"platform_id,omitempty"`
	Kernel     string `json:"kernel,omitempty"`
	Arch       string `json:"arch"`
	CPUCores   int    `json:"cpu_cores"`
	// BootTime is the Unix time in seconds when the machine booted.
	BootTime int64 `json:"boot_time,omitempty"`
}

// LoadAverage is the classic 1/5/15 minute run-queue average.
type LoadAverage struct {
	One     float64 `json:"one"`
	Five    float64 `json:"five"`
	Fifteen float64 `json:"fifteen"`
}

// CPUStats holds the latest CPU utilisation. Percent covers all cores
// (0-100) and is absent until two readings exist on platforms that derive it
// from cumulative counters.
type CPUStats struct {
	Percent *float64     `json:"percent,omitempty"`
	Load    *LoadAverage `json:"load,omitempty"`
}

// MemoryStats reports physical memory in bytes. Used excludes reclaimable
// caches: MemAvailable on Linux, and app + wired + compressed memory on macOS
// (the Activity Monitor definition).
type MemoryStats struct {
	Total     uint64  `json:"total"`
	Used      uint64  `json:"used"`
	Percent   float64 `json:"percent"`
	SwapTotal uint64  `json:"swap_total,omitempty"`
	SwapUsed  uint64  `json:"swap_used,omitempty"`
}

// DiskStats reports one local filesystem in bytes. Percent matches df:
// used / (used + available to unprivileged users).
type DiskStats struct {
	Mount   string  `json:"mount"`
	FSType  string  `json:"fs_type,omitempty"`
	Total   uint64  `json:"total"`
	Used    uint64  `json:"used"`
	Free    uint64  `json:"free"`
	Percent float64 `json:"percent"`
}

// NetworkStats is the throughput of external interfaces in bytes/second.
type NetworkStats struct {
	RxBytesPerSec float64 `json:"rx_bytes_per_sec"`
	TxBytesPerSec float64 `json:"tx_bytes_per_sec"`
}

// Point is one history sample. Fields are absent when unknown.
type Point struct {
	// At is the Unix time in milliseconds.
	At   int64    `json:"t"`
	CPU  *float64 `json:"cpu,omitempty"`
	Mem  *float64 `json:"mem,omitempty"`
	Load *float64 `json:"load,omitempty"`
	Rx   *float64 `json:"rx,omitempty"`
	Tx   *float64 `json:"tx,omitempty"`
}

// Snapshot is the current state plus recent history.
type Snapshot struct {
	// Supported is false when this platform has no collector; Host is still
	// filled in.
	Supported       bool          `json:"supported"`
	Host            HostInfo      `json:"host"`
	CollectedAt     time.Time     `json:"collected_at"`
	IntervalSeconds int           `json:"interval_seconds"`
	CPU             *CPUStats     `json:"cpu,omitempty"`
	Memory          *MemoryStats  `json:"memory,omitempty"`
	Disks           []DiskStats   `json:"disks"`
	Network         *NetworkStats `json:"network,omitempty"`
	History         []Point       `json:"history"`
	Warnings        []string      `json:"warnings,omitempty"`
}

// reading is one raw platform observation. Cumulative counters are turned
// into rates by comparing two readings.
type reading struct {
	at         time.Time
	cpuTicks   *cpuTicks
	cpuPercent *float64
	load       *LoadAverage
	memory     *MemoryStats
	disks      []DiskStats
	net        *netCounters
	warnings   []string
}

// cpuTicks are cumulative CPU time counters across all cores.
type cpuTicks struct {
	total uint64
	idle  uint64
}

// netCounters are cumulative byte counters across external interfaces.
type netCounters struct {
	rx uint64
	tx uint64
}

// source is the platform-specific collector.
type source interface {
	supported() bool
	info() HostInfo
	read() reading
}
