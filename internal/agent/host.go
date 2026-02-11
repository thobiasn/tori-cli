package agent

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// HostCollector reads host metrics from /proc and /sys.
type HostCollector struct {
	proc string
	sys  string

	// Previous CPU counters for delta-based percent calculation.
	prevBusy  uint64
	prevTotal uint64
	hasPrev   bool
}

// NewHostCollector creates a collector using paths from config.
func NewHostCollector(cfg *HostConfig) *HostCollector {
	return &HostCollector{proc: cfg.Proc, sys: cfg.Sys}
}

// Collect reads all host metrics in a single call.
func (h *HostCollector) Collect() (*HostMetrics, []DiskMetrics, []NetMetrics, error) {
	m := &HostMetrics{}

	if err := h.readCPU(m); err != nil {
		return nil, nil, nil, fmt.Errorf("cpu: %w", err)
	}
	if err := h.readMemory(m); err != nil {
		return nil, nil, nil, fmt.Errorf("memory: %w", err)
	}
	if err := h.readLoadAvg(m); err != nil {
		return nil, nil, nil, fmt.Errorf("loadavg: %w", err)
	}
	if err := h.readUptime(m); err != nil {
		return nil, nil, nil, fmt.Errorf("uptime: %w", err)
	}

	disks, err := h.readDisk()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("disk: %w", err)
	}

	nets, err := h.readNetwork()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("network: %w", err)
	}

	return m, disks, nets, nil
}

// readCPU parses /proc/stat line 1 for aggregate CPU counters and computes
// percent from delta between current and previous readings.
func (h *HostCollector) readCPU(m *HostMetrics) error {
	f, err := os.Open(filepath.Join(h.proc, "stat"))
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return fmt.Errorf("empty /proc/stat")
	}

	line := scanner.Text()
	if !strings.HasPrefix(line, "cpu ") {
		return fmt.Errorf("unexpected /proc/stat first line: %q", line)
	}

	fields := strings.Fields(line)
	if len(fields) < 8 {
		return fmt.Errorf("/proc/stat cpu line too short: %d fields", len(fields))
	}

	// fields: cpu user nice system idle iowait irq softirq [steal guest guest_nice]
	var vals [10]uint64
	for i := 1; i < len(fields) && i <= 10; i++ {
		vals[i-1], _ = strconv.ParseUint(fields[i], 10, 64)
	}

	var total uint64
	for _, v := range vals {
		total += v
	}
	idle := vals[3] + vals[4] // idle + iowait
	busy := total - idle

	if h.hasPrev && total >= h.prevTotal && busy >= h.prevBusy {
		dTotal := total - h.prevTotal
		dBusy := busy - h.prevBusy
		if dTotal > 0 {
			m.CPUPercent = float64(dBusy) / float64(dTotal) * 100
		}
	}

	h.prevBusy = busy
	h.prevTotal = total
	h.hasPrev = true

	return nil
}

// readMemory parses /proc/meminfo for memory and swap.
func (h *HostCollector) readMemory(m *HostMetrics) error {
	f, err := os.Open(filepath.Join(h.proc, "meminfo"))
	if err != nil {
		return err
	}
	defer f.Close()

	vals := make(map[string]uint64)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := parts[0]
		valStr := strings.TrimSpace(parts[1])
		valStr = strings.TrimSuffix(valStr, " kB")
		v, err := strconv.ParseUint(strings.TrimSpace(valStr), 10, 64)
		if err != nil {
			continue
		}
		vals[key] = v
	}

	m.MemTotal = vals["MemTotal"] * 1024 // kB to bytes
	memAvail := vals["MemAvailable"] * 1024
	m.MemUsed = m.MemTotal - memAvail
	if m.MemTotal > 0 {
		m.MemPercent = float64(m.MemUsed) / float64(m.MemTotal) * 100
	}
	m.MemCached = vals["Cached"] * 1024
	m.MemFree = vals["MemFree"] * 1024
	m.SwapTotal = vals["SwapTotal"] * 1024
	swapFree := vals["SwapFree"] * 1024
	m.SwapUsed = m.SwapTotal - swapFree

	return nil
}

// readLoadAvg parses /proc/loadavg.
func (h *HostCollector) readLoadAvg(m *HostMetrics) error {
	data, err := os.ReadFile(filepath.Join(h.proc, "loadavg"))
	if err != nil {
		return err
	}

	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return fmt.Errorf("/proc/loadavg too short: %d fields", len(fields))
	}

	m.Load1, _ = strconv.ParseFloat(fields[0], 64)
	m.Load5, _ = strconv.ParseFloat(fields[1], 64)
	m.Load15, _ = strconv.ParseFloat(fields[2], 64)
	return nil
}

// readUptime parses /proc/uptime.
func (h *HostCollector) readUptime(m *HostMetrics) error {
	data, err := os.ReadFile(filepath.Join(h.proc, "uptime"))
	if err != nil {
		return err
	}

	fields := strings.Fields(string(data))
	if len(fields) < 1 {
		return fmt.Errorf("/proc/uptime empty")
	}

	m.Uptime, _ = strconv.ParseFloat(fields[0], 64)
	return nil
}

// readDisk reads PID 1's mount table, filters to real block device directory
// mounts, and calls statfs. File bind-mounts are skipped. When a device has
// multiple directory mounts, the shortest path is kept.
//
// When /proc/1/root is traversable (bare metal, or container with SYS_PTRACE),
// mountpoints are resolved through it for correct host filesystem stats.
// Otherwise mountpoints are accessed directly — inside a container this still
// shows correct stats for the root partition (overlay reports backing fs stats).
func (h *HostCollector) readDisk() ([]DiskMetrics, error) {
	// Prefer PID 1's mount table (host init's namespace, works in containers
	// with pid:host). Fall back to /proc/mounts for test envs or restricted setups.
	mountsPath := filepath.Join(h.proc, "1", "mounts")
	rootPrefix := filepath.Join(h.proc, "1", "root")

	if _, err := os.Stat(mountsPath); err != nil {
		mountsPath = filepath.Join(h.proc, "mounts")
		rootPrefix = ""
	} else if _, err := os.Stat(rootPrefix); err != nil {
		// /proc/1/mounts is readable but /proc/1/root is not (e.g. Docker
		// without SYS_PTRACE). Use /proc/1/mounts for the mount list but
		// access mountpoints directly.
		rootPrefix = ""
	}

	f, err := os.Open(mountsPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	type devMount struct {
		device     string
		mountpoint string
	}
	best := make(map[string]devMount)

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		device := fields[0]
		mountpoint := fields[1]

		if !strings.HasPrefix(device, "/dev/") {
			continue
		}

		// Resolve through /proc/1/root to access host paths.
		resolvedPath := filepath.Join(rootPrefix, mountpoint)
		info, err := os.Stat(resolvedPath)
		if err != nil || !info.IsDir() {
			continue
		}

		prev, ok := best[device]
		if !ok || len(mountpoint) < len(prev.mountpoint) {
			best[device] = devMount{device, mountpoint}
		}
	}

	var disks []DiskMetrics
	for _, dm := range best {
		resolvedPath := filepath.Join(rootPrefix, dm.mountpoint)
		var stat syscall.Statfs_t
		if err := syscall.Statfs(resolvedPath, &stat); err != nil {
			continue
		}

		total := stat.Blocks * uint64(stat.Bsize)
		free := stat.Bavail * uint64(stat.Bsize)
		used := total - free
		var pct float64
		if total > 0 {
			pct = float64(used) / float64(total) * 100
		}

		disks = append(disks, DiskMetrics{
			Mountpoint: dm.mountpoint,
			Device:     dm.device,
			Total:      total,
			Used:       used,
			Free:       free,
			Percent:    pct,
		})
	}

	sort.Slice(disks, func(i, j int) bool {
		return disks[i].Mountpoint < disks[j].Mountpoint
	})
	return disks, nil
}

// readNetwork parses /proc/net/dev for per-interface counters.
func (h *HostCollector) readNetwork() ([]NetMetrics, error) {
	f, err := os.Open(filepath.Join(h.proc, "net", "dev"))
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var nets []NetMetrics
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if lineNum <= 2 {
			continue // skip header lines
		}

		line := scanner.Text()
		colonIdx := strings.Index(line, ":")
		if colonIdx < 0 {
			continue
		}

		iface := strings.TrimSpace(line[:colonIdx])
		if iface == "lo" {
			continue
		}

		fields := strings.Fields(line[colonIdx+1:])
		if len(fields) < 16 {
			continue
		}

		// Receive: bytes packets errs drop fifo frame compressed multicast
		// Transmit: bytes packets errs drop fifo colls carrier compressed
		rxBytes, _ := strconv.ParseUint(fields[0], 10, 64)
		rxPackets, _ := strconv.ParseUint(fields[1], 10, 64)
		rxErrors, _ := strconv.ParseUint(fields[2], 10, 64)
		txBytes, _ := strconv.ParseUint(fields[8], 10, 64)
		txPackets, _ := strconv.ParseUint(fields[9], 10, 64)
		txErrors, _ := strconv.ParseUint(fields[10], 10, 64)

		nets = append(nets, NetMetrics{
			Iface:     iface,
			RxBytes:   rxBytes,
			TxBytes:   txBytes,
			RxPackets: rxPackets,
			TxPackets: txPackets,
			RxErrors:  rxErrors,
			TxErrors:  txErrors,
		})
	}
	return nets, nil
}
