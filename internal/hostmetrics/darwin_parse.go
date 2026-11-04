package hostmetrics

import (
	"bufio"
	"encoding/binary"
	"strconv"
	"strings"
)

// macOS parsers. They are platform independent so they can be tested on any
// build host; collect_darwin.go feeds them sysctl buffers and command output.

// parseVMStat derives used memory from vm_stat output the way Activity
// Monitor does: app memory (anonymous minus purgeable) + wired + compressed.
// Without the anonymous counter (older releases) it falls back to
// total minus free, inactive and speculative pages.
func parseVMStat(output string, total uint64) *MemoryStats {
	if total == 0 {
		return nil
	}
	pageSize := uint64(4096)
	pages := map[string]uint64{}
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		if _, after, ok := strings.Cut(line, "page size of "); ok {
			if size, err := strconv.ParseUint(strings.Fields(after)[0], 10, 64); err == nil && size > 0 {
				pageSize = size
			}
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		count, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(value), "."), 10, 64)
		if err != nil {
			continue
		}
		pages[strings.Trim(strings.TrimSpace(name), `"`)] = count
	}
	var usedPages uint64
	if anonymous, ok := pages["Anonymous pages"]; ok {
		app := anonymous
		if purgeable := pages["Pages purgeable"]; purgeable <= app {
			app -= purgeable
		}
		usedPages = app + pages["Pages wired down"] + pages["Pages occupied by compressor"]
	} else {
		if _, ok := pages["Pages free"]; !ok {
			return nil
		}
		available := (pages["Pages free"] + pages["Pages inactive"] + pages["Pages speculative"]) * pageSize
		if available > total {
			available = total
		}
		usedPages = (total - available) / pageSize
	}
	used := usedPages * pageSize
	if used > total {
		used = total
	}
	return &MemoryStats{Total: total, Used: used, Percent: percentOf(used, total)}
}

// parsePSCPU sums `ps -A -o %cpu=` and normalises by core count. macOS has no
// cgo-free source of cumulative CPU ticks, and ps reports a short decaying
// average per process, which tracks Activity Monitor closely enough for a
// dashboard at a fraction of top's cost.
func parsePSCPU(output string, cores int) *float64 {
	if cores <= 0 {
		return nil
	}
	var sum float64
	found := false
	for _, field := range strings.Fields(output) {
		value, err := strconv.ParseFloat(strings.ReplaceAll(field, ",", "."), 64)
		if err != nil {
			continue
		}
		sum += value
		found = true
	}
	if !found {
		return nil
	}
	percent := clampPercent(sum / float64(cores))
	return &percent
}

// parseLoadAvgSysctl decodes vm.loadavg: struct loadavg { fixpt_t ldavg[3];
// long fscale; } on 64-bit macOS.
func parseLoadAvgSysctl(raw []byte) *LoadAverage {
	if len(raw) < 24 {
		return nil
	}
	scale := float64(binary.LittleEndian.Uint64(raw[16:24]))
	if scale <= 0 {
		return nil
	}
	return &LoadAverage{
		One:     round2(float64(binary.LittleEndian.Uint32(raw[0:4])) / scale),
		Five:    round2(float64(binary.LittleEndian.Uint32(raw[4:8])) / scale),
		Fifteen: round2(float64(binary.LittleEndian.Uint32(raw[8:12])) / scale),
	}
}

// parseSwapUsage decodes vm.swapusage: struct xsw_usage { u_int64_t total,
// avail, used; ... }.
func parseSwapUsage(raw []byte) (total, used uint64, ok bool) {
	if len(raw) < 24 {
		return 0, 0, false
	}
	return binary.LittleEndian.Uint64(raw[0:8]), binary.LittleEndian.Uint64(raw[16:24]), true
}

// Layout of struct if_msghdr2 (net/if.h) on 64-bit macOS. A darwin test
// cross-checks these against golang.org/x/sys/unix.IfMsghdr2.
const (
	rtmIFInfo2         = 0x12
	iffLoopback        = 0x8
	iftEther           = 0x6
	iftCellular        = 0xff
	ifMsghdr2Size      = 160
	ifMsghdr2TypeOff   = 3
	ifMsghdr2FlagsOff  = 8
	ifMsghdr2DataOff   = 32
	ifData64IBytesOff  = 64
	ifData64OBytesOff  = 72
	ifData64TypeOffset = 0
)

// parseIfList2 totals the byte counters of Ethernet-like interfaces (Wi-Fi,
// Ethernet, cellular) from a NET_RT_IFLIST2 routing dump. Loopback, VPN
// tunnels and bridges are skipped so tunnelled traffic is not counted twice.
func parseIfList2(buf []byte) *netCounters {
	var counters netCounters
	found := false
	for offset := 0; offset+4 <= len(buf); {
		length := int(binary.LittleEndian.Uint16(buf[offset:]))
		if length < 4 || offset+length > len(buf) {
			break
		}
		message := buf[offset : offset+length]
		offset += length
		if message[ifMsghdr2TypeOff] != rtmIFInfo2 || len(message) < ifMsghdr2Size {
			continue
		}
		flags := binary.LittleEndian.Uint32(message[ifMsghdr2FlagsOff:])
		ifType := message[ifMsghdr2DataOff+ifData64TypeOffset]
		if flags&iffLoopback != 0 || (ifType != iftEther && ifType != iftCellular) {
			continue
		}
		counters.rx += binary.LittleEndian.Uint64(message[ifMsghdr2DataOff+ifData64IBytesOff:])
		counters.tx += binary.LittleEndian.Uint64(message[ifMsghdr2DataOff+ifData64OBytesOff:])
		found = true
	}
	if !found {
		return nil
	}
	return &counters
}
