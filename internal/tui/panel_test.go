package tui

import (
	"strings"
	"testing"

	"github.com/thobiasn/rook/internal/protocol"
)

func TestRenderAlertPanelEmpty(t *testing.T) {
	theme := DefaultTheme()
	got := renderAlertPanel(nil, 60, &theme)
	if !strings.Contains(got, "all clear") {
		t.Errorf("empty alerts should show 'all clear', got:\n%s", got)
	}
}

func TestRenderAlertPanelWithAlerts(t *testing.T) {
	theme := DefaultTheme()
	alerts := map[int64]*protocol.AlertEvent{
		1: {ID: 1, RuleName: "high_cpu", Severity: "critical", FiredAt: 1700000000, Message: "CPU at 95%"},
		2: {ID: 2, RuleName: "disk_full", Severity: "warning", FiredAt: 1700000060, Message: "Disk 90%"},
	}
	got := renderAlertPanel(alerts, 80, &theme)
	plain := stripANSI(got)
	if !strings.Contains(plain, "Alerts (2)") {
		t.Errorf("should show count, got:\n%s", plain)
	}
	if !strings.Contains(plain, "high_cpu") {
		t.Error("should contain rule name high_cpu")
	}
	if !strings.Contains(plain, "disk_full") {
		t.Error("should contain rule name disk_full")
	}
}

func TestRenderHostPanel(t *testing.T) {
	theme := DefaultTheme()
	host := &protocol.HostMetrics{
		CPUPercent: 42.5,
		MemTotal:   8 * 1024 * 1024 * 1024,
		MemUsed:    4 * 1024 * 1024 * 1024,
		MemPercent: 50.0,
		Load1:      1.5, Load5: 1.2, Load15: 0.8,
		Uptime: 86400 * 14,
	}
	disks := []protocol.DiskMetrics{
		{Mountpoint: "/", Total: 100 * 1e9, Used: 60 * 1e9, Percent: 60.0},
	}
	rates := NewRateCalc()

	got := renderHostPanel(host, disks, rates, 35, 12, &theme)
	plain := stripANSI(got)
	if !strings.Contains(plain, "CPU") {
		t.Error("should contain CPU label")
	}
	if !strings.Contains(plain, "MEM") {
		t.Error("should contain MEM label")
	}
	if !strings.Contains(plain, "DISK") {
		t.Error("should contain DISK label")
	}
	if !strings.Contains(plain, "LOAD") {
		t.Error("should contain LOAD label")
	}
	if !strings.Contains(plain, "14d") {
		t.Error("should contain uptime 14d")
	}
}

func TestRenderHostPanelNilHost(t *testing.T) {
	theme := DefaultTheme()
	got := renderHostPanel(nil, nil, NewRateCalc(), 30, 10, &theme)
	if !strings.Contains(got, "waiting") {
		t.Error("nil host should show waiting message")
	}
}

func TestRenderLogPanel(t *testing.T) {
	theme := DefaultTheme()
	logs := NewRingBuffer[protocol.LogEntryMsg](10)
	logs.Push(protocol.LogEntryMsg{Timestamp: 1700000000, ContainerName: "web", Message: "hello world", Stream: "stdout"})
	logs.Push(protocol.LogEntryMsg{Timestamp: 1700000001, ContainerName: "web", Message: "error!", Stream: "stderr"})

	got := renderLogPanel(logs, 80, 6, &theme)
	plain := stripANSI(got)
	if !strings.Contains(plain, "hello world") {
		t.Error("should contain log message")
	}
	if !strings.Contains(plain, "web") {
		t.Error("should contain container name")
	}
}

func TestRenderContainerPanel(t *testing.T) {
	theme := DefaultTheme()
	groups := []containerGroup{
		{name: "myapp", containers: []protocol.ContainerMetrics{
			{ID: "c1", Name: "web", State: "running", CPUPercent: 1.5, MemUsage: 128 * 1024 * 1024},
			{ID: "c2", Name: "db", State: "running", CPUPercent: 0.8, MemUsage: 256 * 1024 * 1024},
		}, running: 2},
	}
	got := renderContainerPanel(groups, map[string]bool{}, 0, nil, nil, 50, 10, &theme)
	plain := stripANSI(got)
	if !strings.Contains(plain, "myapp") {
		t.Error("should contain group name")
	}
	if !strings.Contains(plain, "web") {
		t.Error("should contain container name 'web'")
	}
	if !strings.Contains(plain, "db") {
		t.Error("should contain container name 'db'")
	}
}

func TestRenderSelectedPanelContainer(t *testing.T) {
	a := newTestApp()
	s := a.session()
	s.Containers = []protocol.ContainerMetrics{
		{ID: "c1", Name: "web", State: "running", CPUPercent: 5.5, MemUsage: 128 * 1024 * 1024, MemLimit: 512 * 1024 * 1024, Image: "nginx:latest"},
	}
	s.ContInfo = []protocol.ContainerInfo{
		{ID: "c1", Name: "web", Project: "app"},
	}
	s.Dash.groups = buildGroups(s.Containers, s.ContInfo)
	s.Dash.cursor = 1 // First container.

	// Add some CPU history.
	s.CPUHistory["c1"] = NewRingBuffer[float64](60)
	for i := 0; i < 10; i++ {
		s.CPUHistory["c1"].Push(float64(i * 10))
	}

	got := renderSelectedPanel(&a, s, 50, 15, &a.theme)
	plain := stripANSI(got)
	if !strings.Contains(plain, "web") {
		t.Error("should contain container name 'web'")
	}
	if !strings.Contains(plain, "CPU") {
		t.Error("should contain CPU label")
	}
	if !strings.Contains(plain, "nginx") {
		t.Error("should contain image name")
	}
}

func TestRenderSelectedPanelGroupHeader(t *testing.T) {
	a := newTestApp()
	s := a.session()
	s.Containers = []protocol.ContainerMetrics{
		{ID: "c1", Name: "web", State: "running", CPUPercent: 5.0, MemUsage: 100e6},
		{ID: "c2", Name: "db", State: "running", CPUPercent: 3.0, MemUsage: 200e6},
	}
	s.ContInfo = []protocol.ContainerInfo{
		{ID: "c1", Name: "web", Project: "app"},
		{ID: "c2", Name: "db", Project: "app"},
	}
	s.Dash.groups = buildGroups(s.Containers, s.ContInfo)
	s.Dash.cursor = 0 // Group header.

	got := renderSelectedPanel(&a, s, 50, 10, &a.theme)
	plain := stripANSI(got)
	if !strings.Contains(plain, "Group: app") {
		t.Error("should show group summary title")
	}
	if !strings.Contains(plain, "2/2 running") {
		t.Error("should show running count")
	}
}

func TestRenderSelectedPanelNoSelection(t *testing.T) {
	a := newTestApp()
	s := a.session()
	got := renderSelectedPanel(&a, s, 50, 10, &a.theme)
	plain := stripANSI(got)
	if !strings.Contains(plain, "Move cursor") {
		t.Error("should show hint when no container selected")
	}
}

func TestRenderContainerPanelCollapsed(t *testing.T) {
	theme := DefaultTheme()
	groups := []containerGroup{
		{name: "myapp", containers: []protocol.ContainerMetrics{
			{ID: "c1", Name: "web", State: "running"},
		}, running: 1},
	}
	got := renderContainerPanel(groups, map[string]bool{"myapp": true}, 0, nil, nil, 50, 10, &theme)
	plain := stripANSI(got)
	if !strings.Contains(plain, "myapp") {
		t.Error("collapsed should still show group header")
	}
	if strings.Contains(plain, "web") {
		t.Error("collapsed should hide container rows")
	}
}
