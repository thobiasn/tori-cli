package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/thobiasn/rook/internal/protocol"
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

// handleMetricsBackfill populates ring buffers from historical metrics.
// The agent provides ready-to-display time series (already merged across
// deploys and zero-filled). For historical windows, new buffers are created
// and swapped in atomically so old data stays visible until the response arrives.
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

	// Group container points by ID.
	byID := make(map[string][]protocol.TimedContainerMetrics)
	var order []string
	for _, c := range resp.Containers {
		if _, seen := byID[c.ID]; !seen {
			order = append(order, c.ID)
		}
		byID[c.ID] = append(byID[c.ID], c)
	}

	for _, id := range order {
		// Skip the container being viewed in detail — it gets richer
		// service-scoped data from the detail backfill.
		if s.Detail.metricsBackfillPending && id == s.Detail.containerID {
			continue
		}

		series := byID[id]

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
// appropriate ring buffers. Deploy markers come from the agent response.
func handleDetailMetricsBackfill(s *Session, det *DetailState, resp *protocol.QueryMetricsResp, start, end, windowSec int64) {
	det.metricsBackfilled = true
	if len(resp.Containers) == 0 {
		return
	}

	// Read deploy markers from agent response.
	det.deployTimestamps = nil
	det.deployEndTS = 0
	if det.containerID != "" && resp.DeployMarkers != nil {
		det.deployTimestamps = resp.DeployMarkers[det.containerID]
	}
	if len(resp.Containers) > 0 {
		det.deployEndTS = resp.Containers[len(resp.Containers)-1].Timestamp
	}

	// Group by container ID.
	byID := make(map[string][]protocol.TimedContainerMetrics)
	var order []string
	for _, c := range resp.Containers {
		if _, seen := byID[c.ID]; !seen {
			order = append(order, c.ID)
		}
		byID[c.ID] = append(byID[c.ID], c)
	}

	hist := windowSec > 0

	for _, id := range order {
		series := byID[id]

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
