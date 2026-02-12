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

	// Each container should produce exactly 5 buckets (zero-filled).
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

	// Short series (<=n) now also zero-filled to n points.
	small := make([]protocol.TimedContainerMetrics, 3)
	for i := range small {
		small[i] = protocol.TimedContainerMetrics{
			Timestamp:        int64(i),
			ContainerMetrics: protocol.ContainerMetrics{ID: "ccc"},
		}
	}
	same := downsampleContainers(small, 10, 0, 10)
	cccCount := 0
	for _, m := range same {
		if m.ID == "ccc" {
			cccCount++
		}
	}
	if cccCount != 10 {
		t.Errorf("short series: len = %d, want 10 (zero-filled)", cccCount)
	}

	// Partial coverage: data only in second half of window.
	// All 5 buckets should be emitted (zero-filled for empty ones).
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
	dddNonZero := 0
	for _, m := range out2 {
		if m.ID == "ddd" {
			dddCount++
			if m.CPUPercent > 0 {
				dddNonZero++
			}
		}
	}
	// All 5 buckets emitted (zero-filled).
	if dddCount != 5 {
		t.Errorf("ddd = %d points, want 5 (all buckets zero-filled)", dddCount)
	}
	// Buckets 2-4 have data → 3 non-zero entries.
	if dddNonZero != 3 {
		t.Errorf("ddd nonzero = %d, want 3", dddNonZero)
	}
}

func TestMergeContainersByService(t *testing.T) {
	// Old container (stopped) and new container (running) for same service.
	data := []protocol.TimedContainerMetrics{
		{Timestamp: 1, ContainerMetrics: protocol.ContainerMetrics{ID: "old1", CPUPercent: 10, Project: "myapp", Service: "web", State: "exited"}},
		{Timestamp: 2, ContainerMetrics: protocol.ContainerMetrics{ID: "old1", CPUPercent: 20, Project: "myapp", Service: "web", State: "exited"}},
		{Timestamp: 3, ContainerMetrics: protocol.ContainerMetrics{ID: "new1", CPUPercent: 30, Project: "myapp", Service: "web", State: "running"}},
	}
	out, markers := mergeContainersByService(data)

	// All points should now be under "new1".
	for _, d := range out {
		if d.ID != "new1" {
			t.Errorf("expected all IDs to be new1, got %s", d.ID)
		}
	}
	if len(out) != 3 {
		t.Errorf("output len = %d, want 3", len(out))
	}

	// Should have one deploy marker for new1 (old1 → new1 transition).
	if len(markers) == 0 {
		t.Fatal("expected deploy markers")
	}
	if len(markers["new1"]) != 1 {
		t.Fatalf("deploy markers for new1 = %d, want 1", len(markers["new1"]))
	}
	if markers["new1"][0] != 3 {
		t.Errorf("deploy marker = %d, want 3", markers["new1"][0])
	}
}

func TestMergeContainersByServiceScaledSkipped(t *testing.T) {
	// Two containers with same service, both running (scaled service).
	data := []protocol.TimedContainerMetrics{
		{Timestamp: 1, ContainerMetrics: protocol.ContainerMetrics{ID: "s1", CPUPercent: 10, Project: "myapp", Service: "worker", State: "running"}},
		{Timestamp: 1, ContainerMetrics: protocol.ContainerMetrics{ID: "s2", CPUPercent: 20, Project: "myapp", Service: "worker", State: "running"}},
	}
	out, markers := mergeContainersByService(data)

	// Both should remain separate — not merged.
	ids := make(map[string]bool)
	for _, d := range out {
		ids[d.ID] = true
	}
	if !ids["s1"] || !ids["s2"] {
		t.Errorf("scaled service should not be merged, got IDs: %v", ids)
	}
	if len(markers) != 0 {
		t.Errorf("no deploy markers expected for scaled service, got %v", markers)
	}
}

func TestMergeContainersByServiceNonComposeSkipped(t *testing.T) {
	// Containers without Service label should not be merged.
	data := []protocol.TimedContainerMetrics{
		{Timestamp: 1, ContainerMetrics: protocol.ContainerMetrics{ID: "a1", CPUPercent: 10}},
		{Timestamp: 2, ContainerMetrics: protocol.ContainerMetrics{ID: "a2", CPUPercent: 20}},
	}
	out, markers := mergeContainersByService(data)

	ids := make(map[string]bool)
	for _, d := range out {
		ids[d.ID] = true
	}
	if !ids["a1"] || !ids["a2"] {
		t.Errorf("non-compose containers should not be merged, got IDs: %v", ids)
	}
	if len(markers) != 0 {
		t.Errorf("no deploy markers expected, got %v", markers)
	}
}

func TestMergeContainersByServiceDeployMarkers(t *testing.T) {
	// Three deploys of the same service.
	data := []protocol.TimedContainerMetrics{
		{Timestamp: 1, ContainerMetrics: protocol.ContainerMetrics{ID: "v1", CPUPercent: 10, Project: "app", Service: "api", State: "exited"}},
		{Timestamp: 2, ContainerMetrics: protocol.ContainerMetrics{ID: "v2", CPUPercent: 20, Project: "app", Service: "api", State: "exited"}},
		{Timestamp: 3, ContainerMetrics: protocol.ContainerMetrics{ID: "v3", CPUPercent: 30, Project: "app", Service: "api", State: "running"}},
	}
	_, markers := mergeContainersByService(data)

	// Should have 2 deploy markers (v1→v2 and v2→v3).
	if len(markers["v3"]) != 2 {
		t.Fatalf("deploy markers = %d, want 2", len(markers["v3"]))
	}
}
