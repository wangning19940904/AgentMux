package hostmetrics

import (
	"bufio"
	"sort"
	"strconv"
	"strings"
)

// Linux /proc parsers. They are platform independent so they can be tested
// on any build host.

// parseProcStat reads the aggregate "cpu" line and the boot time.
func parseProcStat(data string) (ticks *cpuTicks, bootTime int64) {
	scanner := bufio.NewScanner(strings.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "cpu":
			// user nice system idle iowait irq softirq steal guest guest_nice.
			// guest time is already included in user/nice, so stop at steal.
			var values [8]uint64
			for index := 0; index < len(values) && index+1 < len(fields); index++ {
				values[index], _ = strconv.ParseUint(fields[index+1], 10, 64)
			}
			var total uint64
			for _, value := range values {
				total += value
			}
			if total > 0 {
				ticks = &cpuTicks{total: total, idle: values[3] + values[4]}
			}
		case "btime":
			if len(fields) > 1 {
				bootTime, _ = strconv.ParseInt(fields[1], 10, 64)
			}
		}
	}
	return ticks, bootTime
}

func parseLoadAvg(data string) *LoadAverage {
	fields := strings.Fields(data)
	if len(fields) < 3 {
		return nil
	}
	var values [3]float64
	for index := range values {
		value, err := strconv.ParseFloat(fields[index], 64)
		if err != nil {
			return nil
		}
		values[index] = value
	}
	return &LoadAverage{One: values[0], Five: values[1], Fifteen: values[2]}
}

// parseMemInfo uses MemAvailable, the kernel's estimate of memory that can be
// used without swapping, so page cache does not count as used.
func parseMemInfo(data string) *MemoryStats {
	values := map[string]uint64{}
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		name, rest, ok := strings.Cut(scanner.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		value, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		if len(fields) > 1 && strings.EqualFold(fields[1], "kB") {
			value *= 1024
		}
		values[strings.TrimSpace(name)] = value
	}
	total := values["MemTotal"]
	if total == 0 {
		return nil
	}
	available, ok := values["MemAvailable"]
	if !ok {
		// Kernels before 3.14 lack MemAvailable.
		available = values["MemFree"] + values["Buffers"] + values["Cached"]
	}
	if available > total {
		available = total
	}
	used := total - available
	stats := &MemoryStats{Total: total, Used: used, Percent: percentOf(used, total)}
	if swapTotal := values["SwapTotal"]; swapTotal > 0 {
		stats.SwapTotal = swapTotal
		if free := values["SwapFree"]; free <= swapTotal {
			stats.SwapUsed = swapTotal - free
		}
	}
	return stats
}

type netDevEntry struct {
	name string
	rx   uint64
	tx   uint64
}

// parseNetDev reads /proc/net/dev. Each data line is
// "iface: rx_bytes rx_packets ... (8 fields) tx_bytes ...".
func parseNetDev(data string) []netDevEntry {
	var entries []netDevEntry
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		name, rest, ok := strings.Cut(scanner.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 9 {
			continue
		}
		rx, rxErr := strconv.ParseUint(fields[0], 10, 64)
		tx, txErr := strconv.ParseUint(fields[8], 10, 64)
		if rxErr != nil || txErr != nil {
			continue
		}
		entries = append(entries, netDevEntry{name: strings.TrimSpace(name), rx: rx, tx: tx})
	}
	return entries
}

// sumNetDev totals the external interfaces. Physical devices are preferred so
// traffic bridged into containers or tunnelled through a VPN is not counted
// twice; without any (inside a container) every non-loopback interface counts.
func sumNetDev(entries []netDevEntry, physical func(string) bool) *netCounters {
	var all, phys netCounters
	foundAll, foundPhysical := false, false
	for _, entry := range entries {
		if entry.name == "lo" {
			continue
		}
		all.rx += entry.rx
		all.tx += entry.tx
		foundAll = true
		if physical != nil && physical(entry.name) {
			phys.rx += entry.rx
			phys.tx += entry.tx
			foundPhysical = true
		}
	}
	switch {
	case foundPhysical:
		return &phys
	case foundAll:
		return &all
	default:
		return nil
	}
}

// parseOSRelease returns PRETTY_NAME (or NAME VERSION) and ID.
func parseOSRelease(data string) (pretty, id string) {
	values := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		key, value, ok := strings.Cut(strings.TrimSpace(scanner.Text()), "=")
		if !ok || strings.HasPrefix(key, "#") {
			continue
		}
		if unquoted, err := strconv.Unquote(value); err == nil {
			value = unquoted
		} else {
			value = strings.Trim(value, `"'`)
		}
		values[key] = value
	}
	pretty = values["PRETTY_NAME"]
	if pretty == "" {
		pretty = strings.TrimSpace(values["NAME"] + " " + values["VERSION"])
	}
	return pretty, strings.ToLower(values["ID"])
}

type mountEntry struct {
	device string
	mount  string
	fsType string
}

// localFilesystems lists disk-backed filesystem types. Network and FUSE
// filesystems are excluded on purpose: statfs on a hung NFS server blocks
// forever, and they do not describe this machine's disks.
var localFilesystems = map[string]bool{
	"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true,
	"zfs": true, "f2fs": true, "jfs": true, "reiserfs": true, "bcachefs": true,
	"vfat": true, "exfat": true, "ntfs": true, "ntfs3": true, "hfsplus": true,
}

const maxDisks = 8

// parseMounts returns local disk mounts from /proc/self/mounts, one per
// device (bind mounts and btrfs subvolumes repeat a device; the shortest
// mount path wins), root first.
func parseMounts(data string) []mountEntry {
	byDevice := map[string]mountEntry{}
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 || !localFilesystems[fields[2]] {
			continue
		}
		entry := mountEntry{device: fields[0], mount: unescapeMount(fields[1]), fsType: fields[2]}
		if strings.HasPrefix(entry.mount, "/snap/") || strings.HasPrefix(entry.mount, "/var/lib/docker/") ||
			strings.HasPrefix(entry.mount, "/var/lib/containers/") || strings.HasPrefix(entry.mount, "/run/") {
			continue
		}
		if existing, ok := byDevice[entry.device]; ok && len(existing.mount) <= len(entry.mount) {
			continue
		}
		byDevice[entry.device] = entry
	}
	mounts := make([]mountEntry, 0, len(byDevice))
	for _, entry := range byDevice {
		mounts = append(mounts, entry)
	}
	sort.Slice(mounts, func(i, j int) bool {
		if (mounts[i].mount == "/") != (mounts[j].mount == "/") {
			return mounts[i].mount == "/"
		}
		return mounts[i].mount < mounts[j].mount
	})
	if len(mounts) > maxDisks {
		mounts = mounts[:maxDisks]
	}
	return mounts
}

// unescapeMount decodes the octal escapes (\040 for space) used in mounts.
func unescapeMount(value string) string {
	if !strings.Contains(value, `\`) {
		return value
	}
	var out strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] == '\\' && index+3 < len(value) {
			if code, err := strconv.ParseUint(value[index+1:index+4], 8, 8); err == nil {
				out.WriteByte(byte(code))
				index += 3
				continue
			}
		}
		out.WriteByte(value[index])
	}
	return out.String()
}

// diskStats derives df-style numbers from statfs block counts.
func diskStats(mount, fsType string, blockSize, blocks, free, available uint64) (DiskStats, bool) {
	if blockSize == 0 || blocks == 0 {
		return DiskStats{}, false
	}
	total := blocks * blockSize
	freeBytes := free * blockSize
	if freeBytes > total {
		freeBytes = total
	}
	availableBytes := available * blockSize
	used := total - freeBytes
	stats := DiskStats{Mount: mount, FSType: fsType, Total: total, Used: used, Free: availableBytes}
	stats.Percent = percentOf(used, used+availableBytes)
	return stats, true
}
