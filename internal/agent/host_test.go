package agent

import (
	"math"
	"os"
	"path/filepath"
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
