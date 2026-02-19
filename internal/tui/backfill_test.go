package tui

import (
	"testing"

	"github.com/thobiasn/tori-cli/internal/protocol"
)

func TestDashboardBackfill_StaleResponseDiscarded(t *testing.T) {
	s := NewSession("test", nil, nil)
	s.BackfillGen = 2
	s.BackfillPending = true

	app := App{
		sessions:      map[string]*Session{"test": s},
		activeSession: "test",
	}
	msg := metricsBackfillMsg{
		server: "test",
		resp: &protocol.QueryMetricsResp{
			Host: []protocol.TimedHostMetrics{
				{HostMetrics: protocol.HostMetrics{CPUPercent: 50, MemPercent: 40}},
			},
		},
		rangeHist: true,
		gen:       1, // stale
	}

	result, _ := app.Update(msg)
	a := result.(App)
	updated := a.sessions["test"]

	if !updated.BackfillPending {
		t.Fatal("BackfillPending should remain true for stale response")
	}
	if updated.HostCPUHist.Len() != 0 {
		t.Fatalf("CPU history should be empty, got %d entries", updated.HostCPUHist.Len())
	}
}

func TestDashboardBackfill_CurrentResponseApplied(t *testing.T) {
	s := NewSession("test", nil, nil)
	s.BackfillGen = 2
	s.BackfillPending = true

	app := App{
		sessions:      map[string]*Session{"test": s},
		activeSession: "test",
	}
	msg := metricsBackfillMsg{
		server: "test",
		resp: &protocol.QueryMetricsResp{
			Host: []protocol.TimedHostMetrics{
				{HostMetrics: protocol.HostMetrics{CPUPercent: 50, MemPercent: 40}},
			},
		},
		rangeHist: true,
		gen:       2, // current
	}

	result, _ := app.Update(msg)
	a := result.(App)
	updated := a.sessions["test"]

	if updated.BackfillPending {
		t.Fatal("BackfillPending should be cleared for current response")
	}
	if updated.HostCPUHist.Len() != 1 {
		t.Fatalf("CPU history should have 1 entry, got %d", updated.HostCPUHist.Len())
	}
}

func TestDetailBackfill_StaleResponseDiscarded(t *testing.T) {
	det := &DetailState{}
	det.reset()
	det.containerID = "abc123"
	det.metricsGen = 2
	det.metricsBackfillPending = true

	msg := detailMetricsQueryMsg{
		resp: &protocol.QueryMetricsResp{
			Containers: []protocol.TimedContainerMetrics{
				{ContainerMetrics: protocol.ContainerMetrics{CPUPercent: 80, MemUsage: 1000}},
			},
		},
		containerID: "abc123",
		gen:         1, // stale
	}

	det.handleMetricsBackfill(msg)

	if !det.metricsBackfillPending {
		t.Fatal("metricsBackfillPending should remain true for stale response")
	}
	if det.cpuHist.Len() != 0 {
		t.Fatalf("CPU history should be empty, got %d entries", det.cpuHist.Len())
	}
}

func TestDetailBackfill_CurrentResponseApplied(t *testing.T) {
	det := &DetailState{}
	det.reset()
	det.containerID = "abc123"
	det.metricsGen = 2
	det.metricsBackfillPending = true

	msg := detailMetricsQueryMsg{
		resp: &protocol.QueryMetricsResp{
			Containers: []protocol.TimedContainerMetrics{
				{ContainerMetrics: protocol.ContainerMetrics{CPUPercent: 80, MemUsage: 1000}},
			},
		},
		containerID: "abc123",
		gen:         2, // current
	}

	det.handleMetricsBackfill(msg)

	if det.metricsBackfillPending {
		t.Fatal("metricsBackfillPending should be cleared for current response")
	}
	if det.cpuHist.Len() != 1 {
		t.Fatalf("CPU history should have 1 entry, got %d", det.cpuHist.Len())
	}
}
