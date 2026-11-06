//go:build linux

package hostmetrics

import "testing"

func TestLinuxSourceReadsLiveMetrics(t *testing.T) {
	src := linuxSource{}
	info := src.info()
	if info.CPUCores <= 0 || info.Platform == "" || info.Kernel == "" {
		t.Fatalf("info = %+v", info)
	}
	got := src.read()
	if got.cpuTicks == nil || got.cpuTicks.total == 0 {
		t.Fatalf("cpu ticks = %+v (warnings %v)", got.cpuTicks, got.warnings)
	}
	if got.load == nil {
		t.Fatal("expected load average")
	}
	if got.memory == nil || got.memory.Total == 0 || got.memory.Used > got.memory.Total {
		t.Fatalf("memory = %+v", got.memory)
	}
	if len(got.disks) == 0 || got.disks[0].Total == 0 {
		t.Fatalf("disks = %+v", got.disks)
	}
}
