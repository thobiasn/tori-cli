package agent

import (
	"testing"

	"github.com/thobiasn/rook/internal/protocol"
)

func TestDownsampleHost(t *testing.T) {
	// Generate 100 data points covering timestamps 0-99.
	data := make([]protocol.TimedHostMetrics, 100)
	for i := range data {
		data[i] = protocol.TimedHostMetrics{
			Timestamp: int64(i),
			HostMetrics: protocol.HostMetrics{
				CPUPercent: float64(i),
				MemPercent: float64(100 - i),
			},
		}
	}

	out := downsampleHost(data, 10, 0, 100)
	if len(out) != 10 {
		t.Fatalf("len = %d, want 10", len(out))
	}

	// Each bucket covers 10 timestamps. Max CPU in bucket 0 (ts 0-9) = 9.
	if out[0].CPUPercent != 9 {
		t.Errorf("bucket 0 CPU = %f, want 9", out[0].CPUPercent)
	}
	// Last bucket: timestamps 90-99, max CPU = 99.
	if out[9].CPUPercent != 99 {
		t.Errorf("bucket 9 CPU = %f, want 99", out[9].CPUPercent)
	}
	// Timestamps should be monotonically increasing.
	for i := 1; i < len(out); i++ {
		if out[i].Timestamp <= out[i-1].Timestamp {
			t.Errorf("timestamps not monotonic: [%d]=%d <= [%d]=%d", i, out[i].Timestamp, i-1, out[i-1].Timestamp)
		}
	}

	// Partial coverage: data from ts 60-99 in window 0-100.
	// First 6 buckets should be zero-filled, last 4 should have data.
	partial := data[60:]
	out2 := downsampleHost(partial, 10, 0, 100)
	if len(out2) != 10 {
		t.Fatalf("partial: len = %d, want 10", len(out2))
	}
	for i := 0; i < 6; i++ {
		if out2[i].CPUPercent != 0 {
			t.Errorf("partial: bucket %d CPU = %f, want 0", i, out2[i].CPUPercent)
		}
	}
	// Bucket 6 (ts 60-69) should have max CPU = 69.
	if out2[6].CPUPercent != 69 {
		t.Errorf("partial: bucket 6 CPU = %f, want 69", out2[6].CPUPercent)
	}

	// Empty data returns empty.
	empty := downsampleHost(nil, 10, 0, 100)
	if len(empty) != 0 {
		t.Errorf("empty: len = %d, want 0", len(empty))
	}
}

func TestDownsampleContainers(t *testing.T) {
	// Two containers, 20 points each, timestamps 0-19.
	var data []protocol.TimedContainerMetrics
	for i := 0; i < 20; i++ {
		data = append(data, protocol.TimedContainerMetrics{
			Timestamp:        int64(i),
			ContainerMetrics: protocol.ContainerMetrics{ID: "aaa", CPUPercent: float64(i)},
		})
		data = append(data, protocol.TimedContainerMetrics{
			Timestamp:        int64(i),
			ContainerMetrics: protocol.ContainerMetrics{ID: "bbb", CPUPercent: float64(i * 2)},
		})
	}

	out := downsampleContainers(data, 5, 0, 20)

	// Count per container — each should have exactly 5 filled buckets
	// (dense data: 20 points into 5 buckets, all buckets get filled).
	counts := make(map[string]int)
	for _, m := range out {
		counts[m.ID]++
	}
	if counts["aaa"] != 5 {
		t.Errorf("aaa = %d points, want 5", counts["aaa"])
	}
	if counts["bbb"] != 5 {
		t.Errorf("bbb = %d points, want 5", counts["bbb"])
	}

	// Short series (<=n) returned as-is (TUI handles zero-fill).
	small := make([]protocol.TimedContainerMetrics, 3)
	for i := range small {
		small[i] = protocol.TimedContainerMetrics{
			Timestamp:        int64(i),
			ContainerMetrics: protocol.ContainerMetrics{ID: "ccc"},
		}
	}
	same := downsampleContainers(small, 10, 0, 10)
	if len(same) != 3 {
		t.Errorf("short series: len = %d, want 3", len(same))
	}

	// Partial coverage: data only in second half of window.
	// Only filled buckets are emitted, unfilled ones are omitted.
	var partial []protocol.TimedContainerMetrics
	for i := 10; i < 20; i++ {
		partial = append(partial, protocol.TimedContainerMetrics{
			Timestamp:        int64(i),
			ContainerMetrics: protocol.ContainerMetrics{ID: "ddd", CPUPercent: float64(i)},
		})
	}
	// Window 0-20, 5 buckets of 4s each. Data in ts 10-19 fills buckets 2-4.
	out2 := downsampleContainers(partial, 5, 0, 20)
	dddCount := 0
	for _, m := range out2 {
		if m.ID == "ddd" {
			dddCount++
			if m.CPUPercent == 0 {
				t.Errorf("filled bucket has zero CPU, want nonzero")
			}
		}
	}
	// Buckets 0-1 have no data → omitted. Buckets 2-4 have data → 3 entries.
	if dddCount != 3 {
		t.Errorf("ddd = %d points, want 3 (only filled buckets)", dddCount)
	}
}
