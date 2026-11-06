package hostmetrics

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeSource returns scripted cumulative counters that grow on every read.
type fakeSource struct {
	mu          sync.Mutex
	reads       int
	unsupported bool
}

func (f *fakeSource) supported() bool { return !f.unsupported }

func (f *fakeSource) info() HostInfo {
	return HostInfo{Hostname: "box", OS: "linux", Platform: "Debian", CPUCores: 4}
}

func (f *fakeSource) read() reading {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	n := uint64(f.reads)
	return reading{
		at: time.Now(),
		// Each read adds 100 ticks, 75 of them idle: 25% busy.
		cpuTicks: &cpuTicks{total: 1000 + 100*n, idle: 500 + 75*n},
		load:     &LoadAverage{One: 2, Five: 1, Fifteen: 0.5},
		memory:   &MemoryStats{Total: 1000, Used: 610, Percent: 61},
		disks:    []DiskStats{{Mount: "/", Total: 100, Used: 95, Free: 5, Percent: 95}},
		net:      &netCounters{rx: 4096 * n, tx: 1024 * n},
	}
}

func (f *fakeSource) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

func waitUntil(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met before deadline")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSamplerFirstSnapshotHasRates(t *testing.T) {
	src := &fakeSource{}
	sampler := newSampler(src, Options{Interval: time.Hour, PrimeDelay: 20 * time.Millisecond})
	defer sampler.Close()

	snapshot := sampler.Snapshot(context.Background())
	if !snapshot.Supported || snapshot.Host.Hostname != "box" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if src.readCount() != 2 {
		t.Fatalf("priming reads = %d, want 2", src.readCount())
	}
	if snapshot.CPU == nil || snapshot.CPU.Percent == nil || *snapshot.CPU.Percent != 25 {
		t.Fatalf("cpu = %+v", snapshot.CPU)
	}
	if snapshot.Network == nil || snapshot.Network.RxBytesPerSec <= 0 || snapshot.Network.TxBytesPerSec <= 0 {
		t.Fatalf("network = %+v", snapshot.Network)
	}
	if len(snapshot.History) != 1 || snapshot.History[0].Mem == nil || *snapshot.History[0].Mem != 61 {
		t.Fatalf("history = %+v", snapshot.History)
	}
	if len(snapshot.Disks) != 1 || snapshot.Disks[0].Percent != 95 {
		t.Fatalf("disks = %+v", snapshot.Disks)
	}
	if !sampler.Running() {
		t.Fatal("sampler should keep running after a snapshot")
	}
}

func TestSamplerKeepsBoundedHistory(t *testing.T) {
	src := &fakeSource{}
	sampler := newSampler(src, Options{Interval: 2 * time.Millisecond, Capacity: 5, PrimeDelay: time.Millisecond})
	defer sampler.Close()
	sampler.Snapshot(context.Background())
	waitUntil(t, func() bool { return src.readCount() >= 12 })

	history := sampler.Snapshot(context.Background()).History
	if len(history) != 5 {
		t.Fatalf("history length = %d, want 5", len(history))
	}
	for index := 1; index < len(history); index++ {
		if history[index].At < history[index-1].At {
			t.Fatalf("history out of order: %+v", history)
		}
	}
}

func TestSamplerStopsWhenIdleAndRestarts(t *testing.T) {
	src := &fakeSource{}
	sampler := newSampler(src, Options{
		Interval: 2 * time.Millisecond, IdleTimeout: 20 * time.Millisecond, PrimeDelay: time.Millisecond,
	})
	defer sampler.Close()
	sampler.Snapshot(context.Background())
	waitUntil(t, func() bool { return !sampler.Running() })

	stoppedAt := src.readCount()
	time.Sleep(20 * time.Millisecond)
	if src.readCount() != stoppedAt {
		t.Fatal("idle sampler kept reading")
	}
	snapshot := sampler.Snapshot(context.Background())
	if !sampler.Running() || len(snapshot.History) != 1 {
		t.Fatalf("restart history = %d, running = %v", len(snapshot.History), sampler.Running())
	}
}

func TestSamplerUnsupportedPlatformStillDescribesHost(t *testing.T) {
	src := &fakeSource{unsupported: true}
	sampler := newSampler(src, Options{})
	snapshot := sampler.Snapshot(context.Background())
	if snapshot.Supported || snapshot.Host.CPUCores != 4 || snapshot.Disks == nil || snapshot.History == nil {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if sampler.Running() || src.readCount() != 0 {
		t.Fatal("unsupported platform must not sample")
	}
}

func TestNetworkRateIgnoresCounterResets(t *testing.T) {
	if networkRate(netCounters{rx: 100, tx: 100}, netCounters{rx: 50, tx: 200}, time.Second) != nil {
		t.Fatal("shrinking counters must not report a rate")
	}
	got := networkRate(netCounters{rx: 0, tx: 0}, netCounters{rx: 5000, tx: 1000}, 2*time.Second)
	if got == nil || got.RxBytesPerSec != 2500 || got.TxBytesPerSec != 500 {
		t.Fatalf("rate = %+v", got)
	}
}
