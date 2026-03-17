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

func TestShouldAutoTrack(t *testing.T) {
	tests := []struct {
		name         string
		trackByDefault bool
		include      []string
		exclude      []string
		input        string
		want         bool
	}{
		{"default false no filters", false, nil, nil, "web", false},
		{"default false include match", false, []string{"web-*"}, nil, "web-app", true},
		{"default false include no match", false, []string{"web-*"}, nil, "api-server", false},
		{"default false include+exclude hit", false, []string{"web-*"}, []string{"web-test"}, "web-test", false},
		{"default false include+exclude pass", false, []string{"web-*"}, []string{"web-test"}, "web-prod", true},
		{"default true no filters", true, nil, nil, "anything", true},
		{"default true exclude match", true, nil, []string{"noisy-*"}, "noisy-logs", false},
		{"default true exclude no match", true, nil, []string{"noisy-*"}, "web", true},
		{"default true include+exclude", true, []string{"web-*"}, []string{"web-test"}, "web-test", false},
		{"default true include+exclude pass", true, []string{"web-*"}, []string{"web-test"}, "web-prod", true},
		{"default true include no match", true, []string{"web-*"}, nil, "api-server", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldAutoTrack(tt.input, tt.trackByDefault, tt.include, tt.exclude)
			if got != tt.want {
				t.Errorf("shouldAutoTrack(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
