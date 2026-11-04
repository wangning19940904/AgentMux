package hostmetrics

import (
	"context"
	"math"
	"sync"
	"time"
)

const (
	defaultInterval    = 5 * time.Second
	defaultCapacity    = 72 // six minutes at the default interval
	defaultIdleTimeout = 10 * time.Minute
	defaultPrimeDelay  = 400 * time.Millisecond
	hostInfoMaxAge     = time.Hour
)

// Options tunes a Sampler. Zero values select the defaults.
type Options struct {
	// Interval between background readings.
	Interval time.Duration
	// Capacity is the number of history points kept.
	Capacity int
	// IdleTimeout stops sampling when no snapshot was requested for this long.
	IdleTimeout time.Duration
	// PrimeDelay separates the two readings taken when sampling starts, so
	// the first snapshot already has rates.
	PrimeDelay time.Duration
}

// Sampler keeps a rolling window of host metrics while someone is watching.
type Sampler struct {
	src         source
	interval    time.Duration
	capacity    int
	idleTimeout time.Duration
	primeDelay  time.Duration
	now         func() time.Time

	mu         sync.Mutex
	stop       chan struct{}
	lastAccess time.Time
	prev       *reading
	current    reading
	cpu        *CPUStats
	network    *NetworkStats
	history    []Point
	host       HostInfo
	hostAt     time.Time
}

// New returns a sampler for the current platform.
func New(options Options) *Sampler {
	return newSampler(newPlatformSource(), options)
}

func newSampler(src source, options Options) *Sampler {
	s := &Sampler{
		src:         src,
		interval:    options.Interval,
		capacity:    options.Capacity,
		idleTimeout: options.IdleTimeout,
		primeDelay:  options.PrimeDelay,
		now:         time.Now,
	}
	if s.interval <= 0 {
		s.interval = defaultInterval
	}
	if s.capacity <= 0 {
		s.capacity = defaultCapacity
	}
	if s.idleTimeout <= 0 {
		s.idleTimeout = defaultIdleTimeout
	}
	if s.primeDelay <= 0 {
		s.primeDelay = defaultPrimeDelay
	}
	return s
}

// Snapshot returns the latest metrics, starting the background sampler when
// it is not running. The first call after a start waits PrimeDelay so rates
// are available immediately.
func (s *Sampler) Snapshot(ctx context.Context) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastAccess = s.now()
	if s.now().Sub(s.hostAt) > hostInfoMaxAge {
		s.host = s.src.info()
		s.hostAt = s.now()
	}
	if !s.src.supported() {
		return Snapshot{
			Supported: false, Host: s.host, CollectedAt: s.now().UTC(),
			IntervalSeconds: int(s.interval / time.Second), Disks: []DiskStats{}, History: []Point{},
		}
	}
	if s.stop == nil {
		s.startLocked(ctx)
	}
	return s.snapshotLocked()
}

// Running reports whether background sampling is active.
func (s *Sampler) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stop != nil
}

// Close stops background sampling. A later Snapshot starts it again.
func (s *Sampler) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
}

func (s *Sampler) startLocked(ctx context.Context) {
	baseline := s.src.read()
	s.prev = &baseline
	timer := time.NewTimer(s.primeDelay)
	select {
	case <-timer.C:
	case <-ctx.Done():
		timer.Stop()
	}
	s.recordLocked(s.src.read())
	stop := make(chan struct{})
	s.stop = stop
	go s.loop(stop)
}

func (s *Sampler) stopLocked() {
	if s.stop == nil {
		return
	}
	close(s.stop)
	s.stop = nil
	s.prev = nil
	s.current = reading{}
	s.cpu = nil
	s.network = nil
	s.history = nil
}

func (s *Sampler) loop(stop chan struct{}) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		// Platform reads may run a short command; keep them outside the lock
		// so snapshots stay instant.
		next := s.src.read()
		s.mu.Lock()
		if s.stop != stop {
			s.mu.Unlock()
			return
		}
		s.recordLocked(next)
		if s.now().Sub(s.lastAccess) > s.idleTimeout {
			s.stopLocked()
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
	}
}

func (s *Sampler) recordLocked(next reading) {
	prev := s.prev
	cpu := &CPUStats{Load: next.load}
	switch {
	case next.cpuPercent != nil:
		cpu.Percent = roundPtr(clampPercent(*next.cpuPercent))
	case prev != nil && prev.cpuTicks != nil && next.cpuTicks != nil:
		cpu.Percent = cpuPercent(*prev.cpuTicks, *next.cpuTicks)
	}
	if cpu.Percent == nil && cpu.Load == nil {
		cpu = nil
	}
	var network *NetworkStats
	if prev != nil && prev.net != nil && next.net != nil {
		network = networkRate(*prev.net, *next.net, next.at.Sub(prev.at))
	}

	point := Point{At: next.at.UnixMilli()}
	if cpu != nil {
		point.CPU = cpu.Percent
		if cpu.Load != nil {
			point.Load = roundPtr(cpu.Load.One)
		}
	}
	if next.memory != nil {
		point.Mem = roundPtr(next.memory.Percent)
	}
	if network != nil {
		point.Rx = roundPtr(network.RxBytesPerSec)
		point.Tx = roundPtr(network.TxBytesPerSec)
	}
	s.history = append(s.history, point)
	if overflow := len(s.history) - s.capacity; overflow > 0 {
		s.history = append(s.history[:0], s.history[overflow:]...)
	}
	s.current = next
	s.cpu = cpu
	s.network = network
	s.prev = &next
}

func (s *Sampler) snapshotLocked() Snapshot {
	snapshot := Snapshot{
		Supported:       true,
		Host:            s.host,
		CollectedAt:     s.current.at.UTC(),
		IntervalSeconds: int(s.interval / time.Second),
		CPU:             s.cpu,
		Memory:          s.current.memory,
		Disks:           append([]DiskStats{}, s.current.disks...),
		Network:         s.network,
		History:         append([]Point{}, s.history...),
		Warnings:        append([]string(nil), s.current.warnings...),
	}
	if snapshot.CollectedAt.IsZero() {
		snapshot.CollectedAt = s.now().UTC()
	}
	return snapshot
}

func cpuPercent(prev, next cpuTicks) *float64 {
	if next.total <= prev.total || next.idle < prev.idle {
		return nil
	}
	total := float64(next.total - prev.total)
	idle := float64(next.idle - prev.idle)
	return roundPtr(clampPercent((1 - idle/total) * 100))
}

func networkRate(prev, next netCounters, elapsed time.Duration) *NetworkStats {
	seconds := elapsed.Seconds()
	// Counters shrink when an interface disappears or resets; report nothing
	// for that interval rather than a bogus spike.
	if seconds <= 0 || next.rx < prev.rx || next.tx < prev.tx {
		return nil
	}
	return &NetworkStats{
		RxBytesPerSec: round2(float64(next.rx-prev.rx) / seconds),
		TxBytesPerSec: round2(float64(next.tx-prev.tx) / seconds),
	}
}

func clampPercent(value float64) float64 {
	if math.IsNaN(value) || value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func percentOf(part, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return round2(clampPercent(float64(part) / float64(total) * 100))
}

func round2(value float64) float64 {
	return math.Round(value*100) / 100
}

func roundPtr(value float64) *float64 {
	rounded := round2(value)
	return &rounded
}
