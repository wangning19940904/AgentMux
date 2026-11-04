package hostmetrics

import (
	"reflect"
	"testing"
)

func TestParseProcStat(t *testing.T) {
	ticks, boot := parseProcStat(`cpu  100 5 50 800 20 3 2 10 7 1
cpu0 50 2 25 400 10 1 1 5 3 0
intr 12345
btime 1730000000
`)
	if ticks == nil {
		t.Fatal("expected cpu ticks")
	}
	// guest columns are already part of user/nice and must not be added.
	if ticks.total != 990 || ticks.idle != 820 {
		t.Fatalf("ticks = %+v, want total 990 idle 820", *ticks)
	}
	if boot != 1730000000 {
		t.Fatalf("boot = %d", boot)
	}
}

func TestCPUPercentFromTicks(t *testing.T) {
	got := cpuPercent(cpuTicks{total: 1000, idle: 800}, cpuTicks{total: 1200, idle: 950})
	if got == nil || *got != 25 {
		t.Fatalf("cpuPercent = %v, want 25", got)
	}
	if cpuPercent(cpuTicks{total: 1000, idle: 800}, cpuTicks{total: 900, idle: 700}) != nil {
		t.Fatal("counter reset must not produce a value")
	}
}

func TestParseLoadAvg(t *testing.T) {
	got := parseLoadAvg("32.40 28.10 20.05 3/1234 5678\n")
	want := &LoadAverage{One: 32.4, Five: 28.1, Fifteen: 20.05}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseLoadAvg = %+v", got)
	}
	if parseLoadAvg("garbage") != nil {
		t.Fatal("expected nil for malformed input")
	}
}

func TestParseMemInfoUsesAvailable(t *testing.T) {
	got := parseMemInfo(`MemTotal:       14155776 kB
MemFree:          512000 kB
MemAvailable:    5505024 kB
Buffers:          100000 kB
Cached:          4000000 kB
SwapTotal:       2097152 kB
SwapFree:        1048576 kB
`)
	if got == nil {
		t.Fatal("expected memory stats")
	}
	if got.Total != 14155776*1024 || got.Used != (14155776-5505024)*1024 {
		t.Fatalf("memory = %+v", got)
	}
	if got.Percent != 61.11 {
		t.Fatalf("percent = %v, want 61.11", got.Percent)
	}
	if got.SwapTotal != 2097152*1024 || got.SwapUsed != 1048576*1024 {
		t.Fatalf("swap = %d/%d", got.SwapUsed, got.SwapTotal)
	}
}

func TestParseMemInfoWithoutAvailable(t *testing.T) {
	got := parseMemInfo("MemTotal: 1000 kB\nMemFree: 100 kB\nBuffers: 100 kB\nCached: 200 kB\n")
	if got == nil || got.Used != 600*1024 {
		t.Fatalf("memory = %+v", got)
	}
}

func TestParseNetDevPrefersPhysicalInterfaces(t *testing.T) {
	entries := parseNetDev(`Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo:  999999     100    0    0    0     0          0         0   999999     100    0    0    0     0       0          0
  eth0: 1000 10 0 0 0 0 0 0 2000 20 0 0 0 0 0 0
docker0:500 5 0 0 0 0 0 0 600 6 0 0 0 0 0 0
veth12: 400 4 0 0 0 0 0 0 300 3 0 0 0 0 0 0
`)
	if len(entries) != 4 {
		t.Fatalf("entries = %+v", entries)
	}
	physical := func(name string) bool { return name == "eth0" }
	got := sumNetDev(entries, physical)
	if got == nil || got.rx != 1000 || got.tx != 2000 {
		t.Fatalf("physical sum = %+v", got)
	}
	// Inside a container nothing is physical: count every non-loopback link.
	got = sumNetDev(entries, func(string) bool { return false })
	if got == nil || got.rx != 1900 || got.tx != 2900 {
		t.Fatalf("fallback sum = %+v", got)
	}
	if sumNetDev([]netDevEntry{{name: "lo", rx: 1, tx: 1}}, physical) != nil {
		t.Fatal("loopback only must report nothing")
	}
}

func TestParseOSRelease(t *testing.T) {
	pretty, id := parseOSRelease(`PRETTY_NAME="Debian GNU/Linux 12 (bookworm)"
NAME="Debian GNU/Linux"
VERSION_ID="12"
ID=debian
`)
	if pretty != "Debian GNU/Linux 12 (bookworm)" || id != "debian" {
		t.Fatalf("got %q %q", pretty, id)
	}
	pretty, id = parseOSRelease("NAME=veLinux\nVERSION='2 (lyra)'\nID=\"velinux\"\n")
	if pretty != "veLinux 2 (lyra)" || id != "velinux" {
		t.Fatalf("fallback got %q %q", pretty, id)
	}
}

func TestParseMountsKeepsLocalDisksOncePerDevice(t *testing.T) {
	got := parseMounts(`sysfs /sys sysfs rw 0 0
/dev/sda2 /home ext4 rw 0 0
/dev/sda1 / ext4 rw 0 0
/dev/sda1 /var/lib/bind ext4 rw 0 0
tmpfs /run tmpfs rw 0 0
nas:/export /mnt/nas nfs4 rw 0 0
/dev/nvme0n1p1 /data\040disk xfs rw 0 0
/dev/loop3 /snap/core/1 squashfs ro 0 0
overlay /var/lib/docker/overlay2/x/merged overlay rw 0 0
`)
	want := []mountEntry{
		{device: "/dev/sda1", mount: "/", fsType: "ext4"},
		{device: "/dev/nvme0n1p1", mount: "/data disk", fsType: "xfs"},
		{device: "/dev/sda2", mount: "/home", fsType: "ext4"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseMounts = %+v", got)
	}
}

func TestDiskStatsMatchesDF(t *testing.T) {
	// 100 blocks, 20 free of which 15 are available to unprivileged users.
	got, ok := diskStats("/", "ext4", 4096, 100, 20, 15)
	if !ok {
		t.Fatal("expected stats")
	}
	if got.Total != 409600 || got.Used != 80*4096 || got.Free != 15*4096 {
		t.Fatalf("disk = %+v", got)
	}
	// df: used / (used + avail) = 80 / 95.
	if got.Percent != 84.21 {
		t.Fatalf("percent = %v", got.Percent)
	}
	if _, ok := diskStats("/", "ext4", 0, 100, 1, 1); ok {
		t.Fatal("zero block size must be rejected")
	}
}
