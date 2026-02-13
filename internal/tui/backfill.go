package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/thobiasn/tori-cli/internal/protocol"
)

type backfillRetryMsg struct {
	server  string
	seconds int64
}
type backfillRetryTickMsg backfillRetryMsg

type metricsBackfillMsg struct {
	server    string
	resp      *protocol.QueryMetricsResp
	start     int64 // query window start (unix seconds)
	end       int64 // query window end (unix seconds)
	rangeHist bool  // true if this was a historical (non-live) request
}

// backfillMetrics fetches historical metrics for the given time range.
// seconds=0 uses the default 30-minute live backfill. For fixed windows,
// points=600 requests server-side downsampling.
func backfillMetrics(c *Client, seconds int64) tea.Cmd {
	return func() tea.Msg {
		timeout := 5 * time.Second
		hist := seconds > 0
		if hist {
			timeout = 15 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		now := time.Now().Unix()
		rangeSec := int64(ringBufSize * 10) // 600 points × 10s default interval
		points := 0
		if hist {
			rangeSec = seconds
			points = ringBufSize
		}
		start := now - rangeSec
		resp, err := c.QueryMetrics(ctx, &protocol.QueryMetricsReq{Start: start, End: now, Points: points})
		if err != nil {
			if hist {
				return backfillRetryMsg{server: c.server, seconds: seconds}
			}
			return nil // Live backfill non-critical: streaming fills graphs.
		}
		return metricsBackfillMsg{server: c.server, resp: resp, start: start, end: now, rangeHist: hist}
	}
}

// svcKey returns a grouping key for container metrics: "project\x00service".
func svcKey(project, service string) string {
	return project + "\x00" + service
}

// handleMetricsBackfill populates ring buffers from historical metrics.
// The agent returns data keyed by synthetic identity (project, service).
// We map service keys to current container IDs via ContInfo so the dashboard
// sparklines work correctly.
func handleMetricsBackfill(s *Session, resp *protocol.QueryMetricsResp, start, end int64, rangeHist bool) {
	if rangeHist {
		// Historical: replace host buffers atomically.
		cpuBuf := NewRingBuffer[float64](ringBufSize)
		memBuf := NewRingBuffer[float64](ringBufSize)
		usedBuf := NewRingBuffer[float64](ringBufSize)
		for _, h := range resp.Host {
			cpuBuf.Push(h.CPUPercent)
			memBuf.Push(h.MemPercent)
			usedBuf.Push(h.MemPercent)
		}
		s.HostCPUHistory = cpuBuf
		s.HostMemHistory = memBuf
		s.HostMemUsedHistory = usedBuf
	} else {
		for _, h := range resp.Host {
			s.HostCPUHistory.Push(h.CPUPercent)
			s.HostMemHistory.Push(h.MemPercent)
			s.HostMemUsedHistory.Push(h.MemPercent)
		}
	}

	// Build service key → current container ID mapping from live container info.
	svcToID := make(map[string]string)
	for _, ci := range s.ContInfo {
		key := svcKey(ci.Project, ci.Service)
		if key == "\x00" {
			// Non-compose: use ("", name) as service identity.
			key = svcKey("", ci.Name)
		}
		if _, exists := svcToID[key]; !exists {
			svcToID[key] = ci.ID
		}
	}

	// Group container points by service key.
	bySvc := make(map[string][]protocol.TimedContainerMetrics)
	var order []string
	for _, c := range resp.Containers {
		key := svcKey(c.Project, c.Service)
		if _, seen := bySvc[key]; !seen {
			order = append(order, key)
		}
		bySvc[key] = append(bySvc[key], c)
	}

	for _, key := range order {
		id := svcToID[key]
		if id == "" {
			continue // No current container for this service — skip.
		}

		// Skip the container being viewed in detail — it gets richer
		// service-scoped data from the detail backfill.
		if s.Detail.metricsBackfillPending && id == s.Detail.containerID {
			continue
		}

		series := bySvc[key]

		if rangeHist {
			// Historical: create new buffers and swap in.
			cpuBuf := NewRingBuffer[float64](ringBufSize)
			memBuf := NewRingBuffer[float64](ringBufSize)
			for _, c := range series {
				cpuBuf.Push(c.CPUPercent)
				memBuf.Push(float64(c.MemUsage))
			}
			s.CPUHistory[id] = cpuBuf
			s.MemHistory[id] = memBuf
		} else {
			// Live: push to existing buffers.
			if _, ok := s.CPUHistory[id]; !ok {
				s.CPUHistory[id] = NewRingBuffer[float64](ringBufSize)
			}
			if _, ok := s.MemHistory[id]; !ok {
				s.MemHistory[id] = NewRingBuffer[float64](ringBufSize)
			}
			for _, c := range series {
				s.CPUHistory[id].Push(c.CPUPercent)
				s.MemHistory[id].Push(float64(c.MemUsage))
			}
		}
	}
}

// handleDetailMetricsBackfill pushes service-scoped metric data into the
// appropriate ring buffers. Data is keyed by (project, service) and mapped
// to container IDs via ContInfo.
func handleDetailMetricsBackfill(s *Session, det *DetailState, resp *protocol.QueryMetricsResp, start, end, windowSec int64) {
	det.metricsBackfilled = true
	if len(resp.Containers) == 0 {
		return
	}

	// Build service key → current container ID mapping.
	svcToID := make(map[string]string)
	for _, ci := range s.ContInfo {
		key := svcKey(ci.Project, ci.Service)
		if key == "\x00" {
			key = svcKey("", ci.Name)
		}
		if _, exists := svcToID[key]; !exists {
			svcToID[key] = ci.ID
		}
	}

	// Group by service key.
	bySvc := make(map[string][]protocol.TimedContainerMetrics)
	var order []string
	for _, c := range resp.Containers {
		key := svcKey(c.Project, c.Service)
		if _, seen := bySvc[key]; !seen {
			order = append(order, key)
		}
		bySvc[key] = append(bySvc[key], c)
	}

	hist := windowSec > 0

	for _, key := range order {
		id := svcToID[key]
		if id == "" {
			continue
		}
		series := bySvc[key]

		if hist {
			// Historical: create new buffers and swap in.
			cpuBuf := NewRingBuffer[float64](ringBufSize)
			memBuf := NewRingBuffer[float64](ringBufSize)
			for _, d := range series {
				cpuBuf.Push(d.CPUPercent)
				memBuf.Push(float64(d.MemUsage))
			}
			s.CPUHistory[id] = cpuBuf
			s.MemHistory[id] = memBuf
		} else {
			// Live: prepend backfill data before existing streaming data.
			var existingCPU, existingMem []float64
			if buf, ok := s.CPUHistory[id]; ok {
				existingCPU = buf.Data()
			}
			if buf, ok := s.MemHistory[id]; ok {
				existingMem = buf.Data()
			}
			cpuBuf := NewRingBuffer[float64](ringBufSize)
			memBuf := NewRingBuffer[float64](ringBufSize)
			for _, d := range series {
				cpuBuf.Push(d.CPUPercent)
				memBuf.Push(float64(d.MemUsage))
			}
			for _, v := range existingCPU {
				cpuBuf.Push(v)
			}
			for _, v := range existingMem {
				memBuf.Push(v)
			}
			s.CPUHistory[id] = cpuBuf
			s.MemHistory[id] = memBuf
		}
	}
}

// pushMemHistories pushes memory usage percentage to the history buffer.
func pushMemHistories(s *Session, h *protocol.HostMetrics) {
	if h.MemTotal == 0 {
		return
	}
	s.HostMemUsedHistory.Push(h.MemPercent)
}
