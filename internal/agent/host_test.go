package agent

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeProc creates a fake /proc tree for testing.
func writeFakeProc(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(path), 0755)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadCPUDelta(t *testing.T) {
	dir := t.TempDir()

	// First reading: user=100 nice=0 system=50 idle=850 iowait=0
	// Total=1000, busy=150
	writeFakeProc(t, dir, map[string]string{
		"stat": "cpu  100 0 50 850 0 0 0 0 0 0\n",
	})

	h := NewHostCollector(&HostConfig{Proc: dir, Sys: dir})
	m := &HostMetrics{}
	if err := h.readCPU(m); err != nil {
		t.Fatal(err)
	}
	// First reading: no previous, CPU should be 0
	if m.CPUPercent != 0 {
		t.Errorf("first reading cpu = %f, want 0", m.CPUPercent)
	}

	// Second reading: user=200 nice=0 system=100 idle=1700 iowait=0
	// Total=2000, busy=300
	// Delta: total=1000, busy=150, percent=15%
	writeFakeProc(t, dir, map[string]string{
		"stat": "cpu  200 0 100 1700 0 0 0 0 0 0\n",
	})

	m2 := &HostMetrics{}
	if err := h.readCPU(m2); err != nil {
		t.Fatal(err)
	}
	if math.Abs(m2.CPUPercent-15.0) > 0.1 {
		t.Errorf("second reading cpu = %f, want ~15.0", m2.CPUPercent)
	}

	// Third reading: counters reset (simulating a wraparound scenario).
	// Values smaller than previous — should return 0, not overflow.
	writeFakeProc(t, dir, map[string]string{
		"stat": "cpu  50 0 25 425 0 0 0 0 0 0\n",
	})

	m3 := &HostMetrics{}
	if err := h.readCPU(m3); err != nil {
		t.Fatal(err)
	}
	if m3.CPUPercent != 0 {
		t.Errorf("counter reset cpu = %f, want 0", m3.CPUPercent)
	}
}

func TestReadMemory(t *testing.T) {
	dir := t.TempDir()
	writeFakeProc(t, dir, map[string]string{
		"meminfo": `MemTotal:       16384000 kB
MemFree:         4000000 kB
MemAvailable:    8192000 kB
Buffers:          500000 kB
Cached:          3000000 kB
SwapTotal:       4096000 kB
SwapFree:        2048000 kB
`,
	})

	h := NewHostCollector(&HostConfig{Proc: dir, Sys: dir})
	m := &HostMetrics{}
	if err := h.readMemory(m); err != nil {
		t.Fatal(err)
	}

	expectTotal := uint64(16384000) * 1024
	if m.MemTotal != expectTotal {
		t.Errorf("mem_total = %d, want %d", m.MemTotal, expectTotal)
	}

	expectUsed := expectTotal - uint64(8192000)*1024
	if m.MemUsed != expectUsed {
		t.Errorf("mem_used = %d, want %d", m.MemUsed, expectUsed)
	}

	if math.Abs(m.MemPercent-50.0) > 0.1 {
		t.Errorf("mem_percent = %f, want ~50.0", m.MemPercent)
	}

	expectSwapTotal := uint64(4096000) * 1024
	if m.SwapTotal != expectSwapTotal {
		t.Errorf("swap_total = %d, want %d", m.SwapTotal, expectSwapTotal)
	}

	expectSwapUsed := expectSwapTotal - uint64(2048000)*1024
	if m.SwapUsed != expectSwapUsed {
		t.Errorf("swap_used = %d, want %d", m.SwapUsed, expectSwapUsed)
	}
}

func TestReadLoadAvg(t *testing.T) {
	dir := t.TempDir()
	writeFakeProc(t, dir, map[string]string{
		"loadavg": "1.50 1.20 0.90 2/300 12345\n",
	})

	h := NewHostCollector(&HostConfig{Proc: dir, Sys: dir})
	m := &HostMetrics{}
	if err := h.readLoadAvg(m); err != nil {
		t.Fatal(err)
	}

	if m.Load1 != 1.50 {
		t.Errorf("load1 = %f, want 1.50", m.Load1)
	}
	if m.Load5 != 1.20 {
		t.Errorf("load5 = %f, want 1.20", m.Load5)
	}
	if m.Load15 != 0.90 {
		t.Errorf("load15 = %f, want 0.90", m.Load15)
	}
}

func TestReadUptime(t *testing.T) {
	dir := t.TempDir()
	writeFakeProc(t, dir, map[string]string{
		"uptime": "86400.55 172000.10\n",
	})

	h := NewHostCollector(&HostConfig{Proc: dir, Sys: dir})
	m := &HostMetrics{}
	if err := h.readUptime(m); err != nil {
		t.Fatal(err)
	}

	if math.Abs(m.Uptime-86400.55) > 0.01 {
		t.Errorf("uptime = %f, want 86400.55", m.Uptime)
	}
}

func TestReadNetwork(t *testing.T) {
	dir := t.TempDir()
	writeFakeProc(t, dir, map[string]string{
		"net/dev": `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 1000 10 0 0 0 0 0 0 1000 10 0 0 0 0 0 0
  eth0: 50000 500 2 0 0 0 0 0 30000 300 1 0 0 0 0 0
 wlan0: 20000 200 0 0 0 0 0 0 10000 100 0 0 0 0 0 0
`,
	})

	h := NewHostCollector(&HostConfig{Proc: dir, Sys: dir})
	nets, err := h.readNetwork()
	if err != nil {
		t.Fatal(err)
	}

	// lo should be skipped
	if len(nets) != 2 {
		t.Fatalf("got %d interfaces, want 2", len(nets))
	}

	eth0 := nets[0]
	if eth0.Iface != "eth0" {
		t.Errorf("iface = %q, want eth0", eth0.Iface)
	}
	if eth0.RxBytes != 50000 {
		t.Errorf("rx_bytes = %d, want 50000", eth0.RxBytes)
	}
	if eth0.TxBytes != 30000 {
		t.Errorf("tx_bytes = %d, want 30000", eth0.TxBytes)
	}
	if eth0.RxPackets != 500 {
		t.Errorf("rx_packets = %d, want 500", eth0.RxPackets)
	}
	if eth0.RxErrors != 2 {
		t.Errorf("rx_errors = %d, want 2", eth0.RxErrors)
	}
	if eth0.TxErrors != 1 {
		t.Errorf("tx_errors = %d, want 1", eth0.TxErrors)
	}
}

func TestReadDiskFiltersVirtualFSTypes(t *testing.T) {
	dir := t.TempDir()

	// Create real directories that statfs can succeed on. We use the temp
	// dir itself as a stand-in for all mountpoints (statfs will return the
	// same values, but we're testing filtering, not arithmetic).
	mp := filepath.Join(dir, "mnt")
	os.MkdirAll(mp, 0755)

	// Build a mounts file with a mix of real and virtual mounts.
	// The device must start with /dev/ to pass the first filter.
	// We point all mountpoints at our real temp dir so os.Stat succeeds.
	mounts := strings.Join([]string{
		"/dev/sda1 " + mp + " ext4 rw,relatime 0 0",
		"/dev/loop0 " + mp + " squashfs ro,nodev,relatime 0 0",
		"/dev/loop1 " + mp + " squashfs ro,nodev,relatime 0 0",
		"/dev/sr0 " + mp + " iso9660 ro,relatime 0 0",
		"/dev/sr1 " + mp + " udf ro,relatime 0 0",
		"tmpfs /tmp tmpfs rw 0 0",
		"proc /proc proc rw 0 0",
	}, "\n") + "\n"

	writeFakeProc(t, dir, map[string]string{
		"mounts": mounts,
	})

	h := NewHostCollector(&HostConfig{Proc: dir, Sys: dir})
	disks, err := h.readDisk()
	if err != nil {
		t.Fatal(err)
	}

	// Only /dev/sda1 (ext4) should survive filtering.
	if len(disks) != 1 {
		var got []string
		for _, d := range disks {
			got = append(got, d.Device)
		}
		t.Fatalf("got %d disks %v, want 1 (only /dev/sda1)", len(disks), got)
	}
	if disks[0].Device != "/dev/sda1" {
		t.Errorf("device = %q, want /dev/sda1", disks[0].Device)
	}
}

func TestReadDiskShortestMountpointWins(t *testing.T) {
	dir := t.TempDir()

	root := filepath.Join(dir, "root")
	sub := filepath.Join(dir, "root", "sub")
	os.MkdirAll(sub, 0755)

	mounts := strings.Join([]string{
		"/dev/sda1 " + sub + " ext4 rw 0 0",
		"/dev/sda1 " + root + " ext4 rw 0 0",
	}, "\n") + "\n"

	writeFakeProc(t, dir, map[string]string{
		"mounts": mounts,
	})

	h := NewHostCollector(&HostConfig{Proc: dir, Sys: dir})
	disks, err := h.readDisk()
	if err != nil {
		t.Fatal(err)
	}

	if len(disks) != 1 {
		t.Fatalf("got %d disks, want 1 (deduplicated by device)", len(disks))
	}
	if disks[0].Mountpoint != root {
		t.Errorf("mountpoint = %q, want %q (shortest)", disks[0].Mountpoint, root)
	}
}

func TestReadDiskSkipsNonDirectories(t *testing.T) {
	dir := t.TempDir()

	// Create a file (not a directory) to act as a bind-mount target.
	filePath := filepath.Join(dir, "afile")
	os.WriteFile(filePath, []byte("x"), 0644)

	mounts := "/dev/sda1 " + filePath + " ext4 rw 0 0\n"

	writeFakeProc(t, dir, map[string]string{
		"mounts": mounts,
	})

	h := NewHostCollector(&HostConfig{Proc: dir, Sys: dir})
	disks, err := h.readDisk()
	if err != nil {
		t.Fatal(err)
	}

	if len(disks) != 0 {
		t.Errorf("got %d disks, want 0 (file mount should be skipped)", len(disks))
	}
}

func TestReadDiskMultipleRealDevices(t *testing.T) {
	dir := t.TempDir()

	mp1 := filepath.Join(dir, "root")
	mp2 := filepath.Join(dir, "boot")
	os.MkdirAll(mp1, 0755)
	os.MkdirAll(mp2, 0755)

	mounts := strings.Join([]string{
		"/dev/sda2 " + mp1 + " ext4 rw 0 0",
		"/dev/sda1 " + mp2 + " vfat rw 0 0",
	}, "\n") + "\n"

	writeFakeProc(t, dir, map[string]string{
		"mounts": mounts,
	})

	h := NewHostCollector(&HostConfig{Proc: dir, Sys: dir})
	disks, err := h.readDisk()
	if err != nil {
		t.Fatal(err)
	}

	if len(disks) != 2 {
		t.Fatalf("got %d disks, want 2", len(disks))
	}
	// Sorted by mountpoint.
	if disks[0].Device != "/dev/sda1" || disks[1].Device != "/dev/sda2" {
		t.Errorf("devices = [%q, %q], want [/dev/sda1, /dev/sda2]",
			disks[0].Device, disks[1].Device)
	}
}

func TestReadDiskEmptyMounts(t *testing.T) {
	dir := t.TempDir()
	writeFakeProc(t, dir, map[string]string{
		"mounts": "",
	})

	h := NewHostCollector(&HostConfig{Proc: dir, Sys: dir})
	disks, err := h.readDisk()
	if err != nil {
		t.Fatal(err)
	}
	if len(disks) != 0 {
		t.Errorf("got %d disks, want 0", len(disks))
	}
}

func TestReadDiskDeviceMapperAndNVMe(t *testing.T) {
	dir := t.TempDir()

	mp1 := filepath.Join(dir, "root")
	mp2 := filepath.Join(dir, "home")
	mp3 := filepath.Join(dir, "boot")
	os.MkdirAll(mp1, 0755)
	os.MkdirAll(mp2, 0755)
	os.MkdirAll(mp3, 0755)

	mounts := strings.Join([]string{
		"/dev/mapper/vg-root " + mp1 + " ext4 rw 0 0",
		"/dev/dm-1 " + mp2 + " xfs rw 0 0",
		"/dev/nvme0n1p1 " + mp3 + " vfat rw 0 0",
	}, "\n") + "\n"

	writeFakeProc(t, dir, map[string]string{
		"mounts": mounts,
	})

	h := NewHostCollector(&HostConfig{Proc: dir, Sys: dir})
	disks, err := h.readDisk()
	if err != nil {
		t.Fatal(err)
	}

	if len(disks) != 3 {
		var got []string
		for _, d := range disks {
			got = append(got, d.Device)
		}
		t.Fatalf("got %d disks %v, want 3", len(disks), got)
	}
}

func TestReadDiskMetricsPopulated(t *testing.T) {
	dir := t.TempDir()
	mp := filepath.Join(dir, "mnt")
	os.MkdirAll(mp, 0755)

	mounts := "/dev/sda1 " + mp + " ext4 rw 0 0\n"
	writeFakeProc(t, dir, map[string]string{
		"mounts": mounts,
	})

	h := NewHostCollector(&HostConfig{Proc: dir, Sys: dir})
	disks, err := h.readDisk()
	if err != nil {
		t.Fatal(err)
	}

	if len(disks) != 1 {
		t.Fatalf("got %d disks, want 1", len(disks))
	}
	d := disks[0]
	if d.Device != "/dev/sda1" {
		t.Errorf("device = %q, want /dev/sda1", d.Device)
	}
	if d.Mountpoint != mp {
		t.Errorf("mountpoint = %q, want %q", d.Mountpoint, mp)
	}
	// statfs on a real temp dir should return nonzero values.
	if d.Total == 0 {
		t.Error("total = 0, want nonzero")
	}
	if d.Percent <= 0 || d.Percent > 100 {
		t.Errorf("percent = %f, want 0 < pct <= 100", d.Percent)
	}
	if d.Used == 0 {
		t.Error("used = 0, want nonzero")
	}
}

func TestReadDiskOctalMountpointIntegration(t *testing.T) {
	dir := t.TempDir()

	// Create a directory with a space in the name.
	mp := filepath.Join(dir, "my disk")
	os.MkdirAll(mp, 0755)

	// In /proc/mounts, the space is encoded as \040.
	escapedMp := strings.ReplaceAll(mp, " ", `\040`)
	mounts := "/dev/sda1 " + escapedMp + " ext4 rw 0 0\n"

	writeFakeProc(t, dir, map[string]string{
		"mounts": mounts,
	})

	h := NewHostCollector(&HostConfig{Proc: dir, Sys: dir})
	disks, err := h.readDisk()
	if err != nil {
		t.Fatal(err)
	}

	if len(disks) != 1 {
		t.Fatalf("got %d disks, want 1 (octal-escaped mountpoint should resolve)", len(disks))
	}
	if disks[0].Mountpoint != mp {
		t.Errorf("mountpoint = %q, want %q", disks[0].Mountpoint, mp)
	}
}

func TestReadDiskFallbackToProcMounts(t *testing.T) {
	dir := t.TempDir()
	mp := filepath.Join(dir, "root")
	os.MkdirAll(mp, 0755)

	// Only create <proc>/mounts, NOT <proc>/1/mounts — triggers fallback.
	mounts := "/dev/sda1 " + mp + " ext4 rw 0 0\n"
	writeFakeProc(t, dir, map[string]string{
		"mounts": mounts,
	})

	h := NewHostCollector(&HostConfig{Proc: dir, Sys: dir})
	disks, err := h.readDisk()
	if err != nil {
		t.Fatal(err)
	}

	if len(disks) != 1 {
		t.Fatalf("got %d disks, want 1", len(disks))
	}
	if disks[0].Device != "/dev/sda1" {
		t.Errorf("device = %q, want /dev/sda1", disks[0].Device)
	}
}

func TestReadDiskPrefersProc1Mounts(t *testing.T) {
	dir := t.TempDir()
	mp := filepath.Join(dir, "root")
	os.MkdirAll(mp, 0755)

	// Create both /proc/mounts and /proc/1/mounts with different content.
	selfMounts := "/dev/sdb1 " + mp + " ext4 rw 0 0\n"
	pid1Mounts := "/dev/sda1 " + mp + " btrfs rw 0 0\n"

	writeFakeProc(t, dir, map[string]string{
		"mounts":   selfMounts,
		"1/mounts": pid1Mounts,
	})

	h := NewHostCollector(&HostConfig{Proc: dir, Sys: dir})
	disks, err := h.readDisk()
	if err != nil {
		t.Fatal(err)
	}

	if len(disks) != 1 {
		t.Fatalf("got %d disks, want 1", len(disks))
	}
	// Should use /proc/1/mounts, not /proc/mounts.
	if disks[0].Device != "/dev/sda1" {
		t.Errorf("device = %q, want /dev/sda1 (from /proc/1/mounts)", disks[0].Device)
	}
}

func TestUnescapeOctal(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"/mnt/normal", "/mnt/normal"},
		{`/mnt/my\040disk`, "/mnt/my disk"},
		{`/mnt/tab\011here`, "/mnt/tab\there"},
		{`/mnt/back\134slash`, `/mnt/back\slash`},
		{`\040start`, " start"},
		{`end\040`, "end "},
		{`/mnt/multi\040word\040path`, "/mnt/multi word path"},
		// Incomplete sequences left as-is.
		{`/mnt/trail\04`, `/mnt/trail\04`},
		{`/mnt/trail\0`, `/mnt/trail\0`},
		{`/mnt/trail\`, `/mnt/trail\`},
		// Non-octal digits left as-is.
		{`/mnt/bad\890`, `/mnt/bad\890`},
	}
	for _, tt := range tests {
		got := unescapeOctal(tt.in)
		if got != tt.want {
			t.Errorf("unescapeOctal(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCollectIntegration(t *testing.T) {
	dir := t.TempDir()
	writeFakeProc(t, dir, map[string]string{
		"stat":    "cpu  100 0 50 850 0 0 0 0 0 0\n",
		"meminfo": "MemTotal: 16384000 kB\nMemFree: 4000000 kB\nMemAvailable: 8192000 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n",
		"loadavg": "0.50 0.30 0.20 1/100 1234\n",
		"uptime":  "3600.00 7200.00\n",
		"mounts":  "", // no real devices in test
		"net/dev": "Inter-|   Receive\n face |bytes\n",
	})

	h := NewHostCollector(&HostConfig{Proc: dir, Sys: dir})
	m, disks, nets, err := h.Collect()
	if err != nil {
		t.Fatal(err)
	}

	if m == nil {
		t.Fatal("metrics is nil")
	}
	// No real devices in test env, so disks/nets may be nil or empty — that's fine.
	_ = disks
	_ = nets
}
