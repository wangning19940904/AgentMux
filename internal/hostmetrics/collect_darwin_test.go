//go:build darwin

package hostmetrics

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

func TestIfMsghdr2LayoutMatchesSystemHeaders(t *testing.T) {
	var header unix.IfMsghdr2
	checks := []struct {
		name      string
		got, want uintptr
	}{
		{"size", unsafe.Sizeof(header), ifMsghdr2Size},
		{"type", unsafe.Offsetof(header.Type), ifMsghdr2TypeOff},
		{"flags", unsafe.Offsetof(header.Flags), ifMsghdr2FlagsOff},
		{"data", unsafe.Offsetof(header.Data), ifMsghdr2DataOff},
		{"data.type", unsafe.Offsetof(header.Data.Type), ifData64TypeOffset},
		{"data.ibytes", unsafe.Offsetof(header.Data.Ibytes), ifData64IBytesOff},
		{"data.obytes", unsafe.Offsetof(header.Data.Obytes), ifData64OBytesOff},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Errorf("%s offset = %d, want %d", check.name, check.got, check.want)
		}
	}
	if unix.RTM_IFINFO2 != rtmIFInfo2 || unix.IFF_LOOPBACK != iffLoopback || unix.IFT_ETHER != iftEther {
		t.Error("routing constants drifted from x/sys/unix")
	}
}

func TestDarwinSourceReadsLiveMetrics(t *testing.T) {
	src := darwinSource{}
	info := src.info()
	if info.CPUCores <= 0 || info.Platform == "" || info.Hostname == "" {
		t.Fatalf("info = %+v", info)
	}
	got := src.read()
	if got.cpuPercent == nil || *got.cpuPercent < 0 || *got.cpuPercent > 100 {
		t.Fatalf("cpu = %v (warnings %v)", got.cpuPercent, got.warnings)
	}
	if got.load == nil {
		t.Fatal("expected load average")
	}
	if got.memory == nil || got.memory.Total == 0 || got.memory.Used == 0 || got.memory.Used > got.memory.Total {
		t.Fatalf("memory = %+v", got.memory)
	}
	if len(got.disks) != 1 || got.disks[0].Total == 0 {
		t.Fatalf("disks = %+v", got.disks)
	}
	if got.net == nil {
		t.Fatal("expected network counters")
	}
}
