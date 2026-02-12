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
	// Two services, 20 points each, timestamps 0-19.
	var data []protocol.TimedContainerMetrics
	for i := 0; i < 20; i++ {
		data = append(data, protocol.TimedContainerMetrics{
			Timestamp:        int64(i),
			ContainerMetrics: protocol.ContainerMetrics{Project: "app", Service: "web", CPUPercent: float64(i)},
		})
		data = append(data, protocol.TimedContainerMetrics{
			Timestamp:        int64(i),
			ContainerMetrics: protocol.ContainerMetrics{Project: "app", Service: "api", CPUPercent: float64(i * 2)},
		})
	}

	out := downsampleContainers(data, 5, 0, 20)

	// Each service should produce exactly 5 buckets (zero-filled).
	webCount, apiCount := 0, 0
	for _, m := range out {
		if m.Project == "app" && m.Service == "web" {
			webCount++
		}
		if m.Project == "app" && m.Service == "api" {
			apiCount++
		}
	}
	if webCount != 5 {
		t.Errorf("web = %d points, want 5", webCount)
	}
	if apiCount != 5 {
		t.Errorf("api = %d points, want 5", apiCount)
	}

	// Short series (<=n) now also zero-filled to n points.
	small := make([]protocol.TimedContainerMetrics, 3)
	for i := range small {
		small[i] = protocol.TimedContainerMetrics{
			Timestamp:        int64(i),
			ContainerMetrics: protocol.ContainerMetrics{Project: "", Service: "solo"},
		}
	}
	same := downsampleContainers(small, 10, 0, 10)
	soloCount := 0
	for _, m := range same {
		if m.Service == "solo" {
			soloCount++
		}
	}
	if soloCount != 10 {
		t.Errorf("short series: len = %d, want 10 (zero-filled)", soloCount)
	}

	// Partial coverage: data only in second half of window.
	// All 5 buckets should be emitted (zero-filled for empty ones).
	var partial []protocol.TimedContainerMetrics
	for i := 10; i < 20; i++ {
		partial = append(partial, protocol.TimedContainerMetrics{
			Timestamp:        int64(i),
			ContainerMetrics: protocol.ContainerMetrics{Project: "p", Service: "svc", CPUPercent: float64(i)},
		})
	}
	// Window 0-20, 5 buckets of 4s each. Data in ts 10-19 fills buckets 2-4.
	out2 := downsampleContainers(partial, 5, 0, 20)
	svcCount := 0
	svcNonZero := 0
	for _, m := range out2 {
		if m.Service == "svc" {
			svcCount++
			if m.CPUPercent > 0 {
				svcNonZero++
			}
		}
	}
	// All 5 buckets emitted (zero-filled).
	if svcCount != 5 {
		t.Errorf("svc = %d points, want 5 (all buckets zero-filled)", svcCount)
	}
	// Buckets 2-4 have data → 3 non-zero entries.
	if svcNonZero != 3 {
		t.Errorf("svc nonzero = %d, want 3", svcNonZero)
	}
}
