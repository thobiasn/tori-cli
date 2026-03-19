package agent

import (
	"testing"

	"github.com/thobiasn/tori-cli/internal/protocol"
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

func TestDownsampleHostEdgeCases(t *testing.T) {
	// start == end → bucketDur <= 0, early return.
	data := []protocol.TimedHostMetrics{{Timestamp: 5, HostMetrics: protocol.HostMetrics{CPUPercent: 10}}}
	out := downsampleHost(data, 5, 10, 10)
	if len(out) != 1 {
		t.Errorf("same start/end: len = %d, want 1 (unchanged)", len(out))
	}

	// Data point well before start → idx clamped to 0.
	before := []protocol.TimedHostMetrics{{Timestamp: -50, HostMetrics: protocol.HostMetrics{CPUPercent: 42}}}
	out2 := downsampleHost(before, 5, 0, 100)
	if out2[0].CPUPercent != 42 {
		t.Errorf("before-start: bucket 0 CPU = %f, want 42", out2[0].CPUPercent)
	}

	// Multiple points in same bucket: verify all max fields are tracked independently.
	multi := []protocol.TimedHostMetrics{
		{Timestamp: 1, HostMetrics: protocol.HostMetrics{CPUPercent: 10, MemPercent: 80, MemUsed: 100, Load1: 0.5, Load5: 1.0, Load15: 2.0}},
		{Timestamp: 2, HostMetrics: protocol.HostMetrics{CPUPercent: 5, MemPercent: 90, MemUsed: 200, Load1: 0.8, Load5: 0.5, Load15: 3.0}},
		{Timestamp: 3, HostMetrics: protocol.HostMetrics{CPUPercent: 15, MemPercent: 70, MemUsed: 50, Load1: 1.0, Load5: 1.5, Load15: 1.0}},
	}
	// All 3 points in one bucket (bucket size = 10).
	out3 := downsampleHost(multi, 1, 0, 10)
	if out3[0].CPUPercent != 15 {
		t.Errorf("merge: CPU = %f, want 15", out3[0].CPUPercent)
	}
	if out3[0].MemPercent != 90 {
		t.Errorf("merge: MemPercent = %f, want 90", out3[0].MemPercent)
	}
	if out3[0].MemUsed != 200 {
		t.Errorf("merge: MemUsed = %d, want 200", out3[0].MemUsed)
	}
	if out3[0].Load1 != 1.0 {
		t.Errorf("merge: Load1 = %f, want 1.0", out3[0].Load1)
	}
	if out3[0].Load5 != 1.5 {
		t.Errorf("merge: Load5 = %f, want 1.5", out3[0].Load5)
	}
	if out3[0].Load15 != 3.0 {
		t.Errorf("merge: Load15 = %f, want 3.0", out3[0].Load15)
	}
}

func TestDownsampleContainersEdgeCases(t *testing.T) {
	// start == end → bucketDur <= 0, early return.
	data := []protocol.TimedContainerMetrics{{Timestamp: 5, ContainerMetrics: protocol.ContainerMetrics{Service: "web"}}}
	out := downsampleContainers(data, 5, 10, 10)
	if len(out) != 1 {
		t.Errorf("same start/end: len = %d, want 1", len(out))
	}

	// Data well before start in zero-fill path (len(series) <= n).
	before := []protocol.TimedContainerMetrics{
		{Timestamp: -50, ContainerMetrics: protocol.ContainerMetrics{Service: "web", CPUPercent: 42}},
	}
	out2 := downsampleContainers(before, 5, 0, 100)
	if out2[0].CPUPercent != 42 {
		t.Errorf("before-start: bucket 0 CPU = %f, want 42", out2[0].CPUPercent)
	}

	// Data after end in zero-fill path (len(series) <= n).
	after := []protocol.TimedContainerMetrics{
		{Timestamp: 200, ContainerMetrics: protocol.ContainerMetrics{Service: "web", CPUPercent: 77}},
	}
	out3 := downsampleContainers(after, 5, 0, 100)
	if out3[4].CPUPercent != 77 {
		t.Errorf("after-end: last bucket CPU = %f, want 77", out3[4].CPUPercent)
	}

	// Data before start in aggregation path (len(series) > n).
	// Need more data points than buckets to trigger aggregation path.
	var many []protocol.TimedContainerMetrics
	many = append(many, protocol.TimedContainerMetrics{
		Timestamp: -50, ContainerMetrics: protocol.ContainerMetrics{Service: "web", CPUPercent: 99},
	})
	for i := 0; i < 20; i++ {
		many = append(many, protocol.TimedContainerMetrics{
			Timestamp:        int64(i),
			ContainerMetrics: protocol.ContainerMetrics{Service: "web", CPUPercent: float64(i), MemUsage: uint64(i * 100), MemPercent: float64(i)},
		})
	}
	out4 := downsampleContainers(many, 5, 0, 20)
	// First bucket should contain the clamped before-start point (CPU 99).
	if out4[0].CPUPercent != 99 {
		t.Errorf("agg before-start: bucket 0 CPU = %f, want 99", out4[0].CPUPercent)
	}
	// Verify MemPercent max is tracked in aggregation.
	if out4[4].MemPercent != 19 {
		t.Errorf("agg: last bucket MemPercent = %f, want 19", out4[4].MemPercent)
	}
}
