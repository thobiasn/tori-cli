package tui

import (
	"strings"
	"testing"

	"github.com/thobiasn/tori-cli/internal/protocol"
)

func TestRenderAlertPanelEmpty(t *testing.T) {
	theme := DefaultTheme()
	got := renderAlertPanel(nil, 60, 0, &theme, "2006-01-02 15:04:05", 0, false)
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
	got := renderAlertPanel(alerts, 80, 0, &theme, "2006-01-02 15:04:05", 0, false)
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

func TestRenderCPUPanel(t *testing.T) {
	theme := DefaultTheme()
	host := &protocol.HostMetrics{
		CPUPercent: 42.5,
		Load1:      1.5, Load5: 1.2, Load15: 0.8,
		Uptime: 86400 * 14,
	}

	got := renderCPUPanel([]float64{10, 20, 42.5}, host, RenderContext{Width: 50, Height: 8, Theme: &theme})
	plain := stripANSI(got)
	if !strings.Contains(plain, "42.5%") {
		t.Error("should contain CPU percentage")
	}
	if !strings.Contains(plain, "Load") {
		t.Error("should contain Load label")
	}
	if !strings.Contains(plain, "14d") {
		t.Error("should contain uptime 14d")
	}
}

func TestRenderCPUPanelNilHost(t *testing.T) {
	theme := DefaultTheme()
	got := renderCPUPanel(nil, nil, RenderContext{Width: 30, Height: 8, Theme: &theme})
	if !strings.Contains(got, "waiting") {
		t.Error("nil host should show waiting message")
	}
}

func TestRenderMemPanel(t *testing.T) {
	theme := DefaultTheme()
	host := &protocol.HostMetrics{
		MemTotal:   8 * 1024 * 1024 * 1024,
		MemUsed:    4 * 1024 * 1024 * 1024,
		MemPercent: 50.0,
		MemCached:  1 * 1024 * 1024 * 1024,
		MemFree:    3 * 1024 * 1024 * 1024,
	}

	got := renderMemPanel(host, []float64{40, 45, 50}, RenderContext{Width: 50, Height: 14, Theme: &theme})
	plain := stripANSI(got)
	if !strings.Contains(plain, "Used") {
		t.Error("should contain Used label")
	}
	if !strings.Contains(plain, "50.0%") {
		t.Error("should contain memory percentage")
	}
	if !strings.Contains(plain, "4.0G") {
		t.Error("should contain used bytes")
	}
	if !strings.Contains(plain, "8.0G") {
		t.Error("should contain total bytes")
	}
}

func TestRenderMemPanelNilHost(t *testing.T) {
	theme := DefaultTheme()
	got := renderMemPanel(nil, nil, RenderContext{Width: 30, Height: 8, Theme: &theme})
	if !strings.Contains(got, "waiting") {
		t.Error("nil host should show waiting message")
	}
}

func TestRenderDiskPanel(t *testing.T) {
	theme := DefaultTheme()
	disks := []protocol.DiskMetrics{
		{Mountpoint: "/", Device: "sda1", Total: 100 * 1024 * 1024 * 1024, Used: 60 * 1024 * 1024 * 1024, Free: 40 * 1024 * 1024 * 1024, Percent: 60},
		{Mountpoint: "/home", Device: "sda2", Total: 200 * 1024 * 1024 * 1024, Used: 50 * 1024 * 1024 * 1024, Free: 150 * 1024 * 1024 * 1024, Percent: 25},
	}

	got := renderDiskPanel(disks, 2*1024*1024*1024, 512*1024*1024, 50, 13, &theme)
	plain := stripANSI(got)
	if !strings.Contains(plain, "Disks") {
		t.Error("should contain Disks title")
	}
	if !strings.Contains(plain, "/home") {
		t.Error("should contain /home mountpoint")
	}
	if !strings.Contains(plain, "Used") {
		t.Error("should contain Used label")
	}
	if !strings.Contains(plain, "Free") {
		t.Error("should contain Free label")
	}
	if !strings.Contains(plain, "60%") {
		t.Error("should contain 60% usage for /")
	}
	if !strings.Contains(plain, "Swap") {
		t.Error("should contain Swap label")
	}
}

func TestRenderDiskPanelEmpty(t *testing.T) {
	theme := DefaultTheme()
	got := renderDiskPanel(nil, 0, 0, 30, 5, &theme)
	if !strings.Contains(got, "no disks") {
		t.Error("empty disks should show 'no disks'")
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
	got := renderContainerPanel(groups, map[string]bool{}, 0, nil, nil, RenderContext{Width: 50, Height: 10, Theme: &theme}, true)
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


func TestRenderContainerPanelCollapsed(t *testing.T) {
	theme := DefaultTheme()
	groups := []containerGroup{
		{name: "myapp", containers: []protocol.ContainerMetrics{
			{ID: "c1", Name: "web", State: "running"},
		}, running: 1},
	}
	got := renderContainerPanel(groups, map[string]bool{"myapp": true}, 0, nil, nil, RenderContext{Width: 50, Height: 10, Theme: &theme}, true)
	plain := stripANSI(got)
	if !strings.Contains(plain, "myapp") {
		t.Error("collapsed should still show group header")
	}
	if strings.Contains(plain, "web") {
		t.Error("collapsed should hide container rows")
	}
}
