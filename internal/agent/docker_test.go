package agent

import (
	"math"
	"testing"

	"github.com/docker/docker/api/types/container"
)

func TestCalcCPUPercentDelta(t *testing.T) {
	tests := []struct {
		name      string
		prevC     uint64
		curC      uint64
		prevS     uint64
		curS      uint64
		cpus      uint32
		wantApprx float64
	}{
		{
			name:      "50% of 2 CPUs",
			prevC:     0,
			curC:      500_000_000,
			prevS:     0,
			curS:      1_000_000_000,
			cpus:      2,
			wantApprx: 100.0, // (500M/1000M) * 2 * 100
		},
		{
			name:      "25% of 4 CPUs",
			prevC:     0,
			curC:      250_000_000,
			prevS:     0,
			curS:      1_000_000_000,
			cpus:      4,
			wantApprx: 100.0, // (250M/1000M) * 4 * 100
		},
		{
			name:      "no delta",
			prevC:     100,
			curC:      100,
			prevS:     100,
			curS:      200,
			cpus:      1,
			wantApprx: 0,
		},
		{
			name:      "zero system delta",
			prevC:     0,
			curC:      100,
			prevS:     100,
			curS:      100,
			cpus:      1,
			wantApprx: 0,
		},
		{
			name:      "container counter reset",
			prevC:     500_000_000,
			curC:      100_000,
			prevS:     1_000_000_000,
			curS:      2_000_000_000,
			cpus:      2,
			wantApprx: 0,
		},
		{
			name:      "system counter reset",
			prevC:     100_000,
			curC:      500_000_000,
			prevS:     2_000_000_000,
			curS:      100_000,
			cpus:      1,
			wantApprx: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalcCPUPercentDelta(tt.prevC, tt.curC, tt.prevS, tt.curS, tt.cpus)
			if math.Abs(got-tt.wantApprx) > 0.1 {
				t.Errorf("got %f, want ~%f", got, tt.wantApprx)
			}
		})
	}
}

func TestCalcMemUsage(t *testing.T) {
	tests := []struct {
		name    string
		stats   container.StatsResponse
		wantUse uint64
		wantLim uint64
	}{
		{
			name: "basic usage",
			stats: container.StatsResponse{
				MemoryStats: container.MemoryStats{
					Usage: 100_000_000,
					Limit: 512_000_000,
					Stats: map[string]uint64{},
				},
			},
			wantUse: 100_000_000,
			wantLim: 512_000_000,
		},
		{
			name: "subtract inactive_file",
			stats: container.StatsResponse{
				MemoryStats: container.MemoryStats{
					Usage: 100_000_000,
					Limit: 512_000_000,
					Stats: map[string]uint64{"inactive_file": 20_000_000},
				},
			},
			wantUse: 80_000_000,
			wantLim: 512_000_000,
		},
		{
			name: "subtract total_inactive_file (v1)",
			stats: container.StatsResponse{
				MemoryStats: container.MemoryStats{
					Usage: 100_000_000,
					Limit: 512_000_000,
					Stats: map[string]uint64{"total_inactive_file": 30_000_000},
				},
			},
			wantUse: 70_000_000,
			wantLim: 512_000_000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, l, _ := calcMemUsage(&tt.stats)
			if u != tt.wantUse {
				t.Errorf("usage = %d, want %d", u, tt.wantUse)
			}
			if l != tt.wantLim {
				t.Errorf("limit = %d, want %d", l, tt.wantLim)
			}
		})
	}
}

func TestCalcNetIO(t *testing.T) {
	stats := &container.StatsResponse{
		Networks: map[string]container.NetworkStats{
			"eth0": {RxBytes: 1000, TxBytes: 500},
			"eth1": {RxBytes: 2000, TxBytes: 1000},
		},
	}
	rx, tx := calcNetIO(stats)
	if rx != 3000 {
		t.Errorf("rx = %d, want 3000", rx)
	}
	if tx != 1500 {
		t.Errorf("tx = %d, want 1500", tx)
	}
}

func TestCalcBlockIO(t *testing.T) {
	stats := &container.StatsResponse{
		BlkioStats: container.BlkioStats{
			IoServiceBytesRecursive: []container.BlkioStatEntry{
				{Op: "Read", Value: 1000},
				{Op: "Write", Value: 500},
				{Op: "Read", Value: 2000},
				{Op: "Write", Value: 1000},
			},
		},
	}
	r, w := calcBlockIO(stats)
	if r != 3000 {
		t.Errorf("read = %d, want 3000", r)
	}
	if w != 1500 {
		t.Errorf("write = %d, want 1500", w)
	}
}

func TestContainerName(t *testing.T) {
	tests := []struct {
		names []string
		want  string
	}{
		{[]string{"/web"}, "web"},
		{[]string{"/my-app"}, "my-app"},
		{[]string{"noprefix"}, "noprefix"},
		{nil, ""},
	}
	for _, tt := range tests {
		got := containerName(tt.names)
		if got != tt.want {
			t.Errorf("containerName(%v) = %q, want %q", tt.names, got, tt.want)
		}
	}
}

func TestUpdateContainerState(t *testing.T) {
	d := &DockerCollector{prevCPU: make(map[string]cpuPrev)}

	// Add new container via event before first collect.
	d.UpdateContainerState("abc", "running", "web", "nginx", "proj")
	containers := d.Containers()
	if len(containers) != 1 || containers[0].State != "running" {
		t.Fatalf("expected 1 running container, got %v", containers)
	}

	// Update existing container state.
	d.UpdateContainerState("abc", "exited", "web", "nginx", "proj")
	containers = d.Containers()
	if len(containers) != 1 || containers[0].State != "exited" {
		t.Fatalf("expected exited state, got %v", containers)
	}

	// Destroy removes from list.
	d.UpdateContainerState("abc", "", "web", "nginx", "proj")
	containers = d.Containers()
	if len(containers) != 0 {
		t.Fatalf("expected empty list after destroy, got %v", containers)
	}

	// Destroy on non-existent container is a no-op.
	d.UpdateContainerState("nonexistent", "", "", "", "")
	if len(d.Containers()) != 0 {
		t.Fatal("destroy of nonexistent should be no-op")
	}
}

func TestMatchFilterExported(t *testing.T) {
	d := &DockerCollector{include: []string{"web-*"}, exclude: []string{"web-test"}}
	if !d.MatchFilter("web-prod") {
		t.Error("web-prod should match")
	}
	if d.MatchFilter("web-test") {
		t.Error("web-test should be excluded")
	}
	if d.MatchFilter("api") {
		t.Error("api should not match include")
	}
}

func TestMatchFilter(t *testing.T) {
	tests := []struct {
		name    string
		include []string
		exclude []string
		input   string
		want    bool
	}{
		{"no filters", nil, nil, "web", true},
		{"include match", []string{"web-*"}, nil, "web-app", true},
		{"include no match", []string{"web-*"}, nil, "api-server", false},
		{"exclude match", nil, []string{"test-*"}, "test-runner", false},
		{"exclude no match", nil, []string{"test-*"}, "web", true},
		{"include+exclude", []string{"web-*"}, []string{"web-test"}, "web-test", false},
		{"include+exclude pass", []string{"web-*"}, []string{"web-test"}, "web-prod", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &DockerCollector{include: tt.include, exclude: tt.exclude}
			got := d.matchFilter(tt.input)
			if got != tt.want {
				t.Errorf("matchFilter(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
