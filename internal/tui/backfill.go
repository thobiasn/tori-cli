package tui

import (
	"context"
	"sort"
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

// handleMetricsBackfill populates ring buffers from historical metrics so
// graphs show data immediately on connect rather than starting empty.
// For historical windows (rangeHist=true), container data that arrived
// sparse (fewer than ringBufSize points) is time-aligned into ringBufSize
// buckets with zero-fill so partial data doesn't stretch across the graph.
func handleMetricsBackfill(s *Session, resp *protocol.QueryMetricsResp, start, end int64, rangeHist bool) {
	for _, h := range resp.Host {
		s.HostCPUHistory.Push(h.CPUPercent)
		s.HostMemHistory.Push(h.MemPercent)
		// Push directly instead of calling pushMemHistories, which skips
		// when MemTotal==0. Zero-fill entries from time-aware downsampling
		// have MemTotal==0 but still need a push to keep buffers in sync.
		s.HostMemUsedHistory.Push(h.MemPercent)
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

	// Merge cross-deploy data so the current container's buffer includes
	// history from previous containers with the same compose service.
	mergeByServiceIdentity(byID, &order)

	n := ringBufSize
	bucketDur := float64(end-start) / float64(n)
	needAlign := rangeHist && bucketDur > 0

	for _, id := range order {
		if _, ok := s.CPUHistory[id]; !ok {
			s.CPUHistory[id] = NewRingBuffer[float64](ringBufSize)
		}
		if _, ok := s.MemHistory[id]; !ok {
			s.MemHistory[id] = NewRingBuffer[float64](ringBufSize)
		}
		series := byID[id]

		// If series already has n points (agent downsampled it), or this
		// is a live backfill, push directly without rebucketing.
		if !needAlign || len(series) >= n {
			for _, c := range series {
				s.CPUHistory[id].Push(c.CPUPercent)
				s.MemHistory[id].Push(float64(c.MemUsage))
			}
			continue
		}

		// Time-align sparse data into n buckets with zero-fill.
		cpuBuckets := make([]float64, n)
		memBuckets := make([]float64, n)
		for _, d := range series {
			idx := int(float64(d.Timestamp-start) / bucketDur)
			if idx < 0 {
				idx = 0
			}
			if idx >= n {
				idx = n - 1
			}
			if d.CPUPercent > cpuBuckets[idx] {
				cpuBuckets[idx] = d.CPUPercent
			}
			if float64(d.MemUsage) > memBuckets[idx] {
				memBuckets[idx] = float64(d.MemUsage)
			}
		}
		for i := 0; i < n; i++ {
			s.CPUHistory[id].Push(cpuBuckets[i])
			s.MemHistory[id].Push(memBuckets[i])
		}
	}
}

// mergeByServiceIdentity collapses data from containers sharing the same
// compose service identity (project+service) into the most recent container's
// entry. This ensures the dashboard graph shows full cross-deploy history
// instead of only data since the last redeploy.
func mergeByServiceIdentity(byID map[string][]protocol.TimedContainerMetrics, order *[]string) {
	// Build {project, service} -> []containerID mapping.
	type svcKey struct{ project, service string }
	svcContainers := make(map[svcKey][]string)
	for id, points := range byID {
		if len(points) == 0 {
			continue
		}
		p := points[0]
		if p.Service == "" {
			continue // Non-compose containers can't be matched across deploys.
		}
		key := svcKey{p.Project, p.Service}
		svcContainers[key] = append(svcContainers[key], id)
	}

	removed := make(map[string]bool)
	for _, ids := range svcContainers {
		if len(ids) < 2 {
			continue
		}

		// Check for scaled services: if multiple containers have "running"
		// as their last state, they're concurrent instances — don't merge.
		runningCount := 0
		for _, id := range ids {
			pts := byID[id]
			if pts[len(pts)-1].State == "running" {
				runningCount++
			}
		}
		if runningCount > 1 {
			continue
		}

		// Collect all points, sort by timestamp, find the "current" container
		// (the one with the latest data point).
		var all []protocol.TimedContainerMetrics
		var latestTS int64
		currentID := ids[0]
		for _, id := range ids {
			pts := byID[id]
			all = append(all, pts...)
			last := pts[len(pts)-1].Timestamp
			if last > latestTS {
				latestTS = last
				currentID = id
			}
		}
		sort.Slice(all, func(i, j int) bool {
			return all[i].Timestamp < all[j].Timestamp
		})

		// Store merged data under the current container ID.
		byID[currentID] = all
		for _, id := range ids {
			if id != currentID {
				delete(byID, id)
				removed[id] = true
			}
		}
	}

	if len(removed) == 0 {
		return
	}

	// Rebuild order without removed IDs.
	filtered := (*order)[:0]
	for _, id := range *order {
		if !removed[id] {
			filtered = append(filtered, id)
		}
	}
	*order = filtered
}

// handleDetailMetricsBackfill merges service-scoped metric data from possibly
// multiple container IDs into the current container's ring buffers. It records
// container ID transition timestamps as deploy boundaries for graph markers.
func handleDetailMetricsBackfill(s *Session, det *DetailState, resp *protocol.QueryMetricsResp) {
	det.metricsBackfilled = true
	if len(resp.Containers) == 0 {
		return
	}

	// Sort all data points by timestamp.
	sorted := make([]protocol.TimedContainerMetrics, len(resp.Containers))
	copy(sorted, resp.Containers)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Timestamp < sorted[j].Timestamp
	})

	if det.containerID != "" {
		// Single-container mode: merge all data into one buffer (cross-deploy).
		handleSingleMetricsBackfill(s, det, sorted)
	} else {
		// Group mode: distribute data into per-container buffers.
		handleGroupMetricsBackfill(s, sorted)
	}
}

func handleSingleMetricsBackfill(s *Session, det *DetailState, sorted []protocol.TimedContainerMetrics) {
	id := det.containerID

	// Detect deploy boundaries (container ID transitions).
	det.deployTimestamps = nil
	det.deployEndTS = sorted[len(sorted)-1].Timestamp
	if len(sorted) > 1 {
		prevCID := sorted[0].ID
		for _, d := range sorted[1:] {
			if d.ID != prevCID {
				prevCID = d.ID
				det.deployTimestamps = append(det.deployTimestamps, d.Timestamp)
			}
		}
	}

	// Save existing live data to re-append after history.
	var existingCPU, existingMem []float64
	if buf, ok := s.CPUHistory[id]; ok {
		existingCPU = buf.Data()
	}
	if buf, ok := s.MemHistory[id]; ok {
		existingMem = buf.Data()
	}

	cpuBuf := NewRingBuffer[float64](ringBufSize)
	memBuf := NewRingBuffer[float64](ringBufSize)
	for _, d := range sorted {
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

func handleGroupMetricsBackfill(s *Session, sorted []protocol.TimedContainerMetrics) {
	// Group data points by container ID.
	type perContainer struct {
		cpu []float64
		mem []float64
	}
	byID := make(map[string]*perContainer)
	for _, d := range sorted {
		pc, ok := byID[d.ID]
		if !ok {
			pc = &perContainer{}
			byID[d.ID] = pc
		}
		pc.cpu = append(pc.cpu, d.CPUPercent)
		pc.mem = append(pc.mem, float64(d.MemUsage))
	}

	// Merge each container's history into its ring buffer.
	for id, pc := range byID {
		var existingCPU, existingMem []float64
		if buf, ok := s.CPUHistory[id]; ok {
			existingCPU = buf.Data()
		}
		if buf, ok := s.MemHistory[id]; ok {
			existingMem = buf.Data()
		}

		cpuBuf := NewRingBuffer[float64](ringBufSize)
		memBuf := NewRingBuffer[float64](ringBufSize)
		for _, v := range pc.cpu {
			cpuBuf.Push(v)
		}
		for _, v := range existingCPU {
			cpuBuf.Push(v)
		}
		for _, v := range pc.mem {
			memBuf.Push(v)
		}
		for _, v := range existingMem {
			memBuf.Push(v)
		}
		s.CPUHistory[id] = cpuBuf
		s.MemHistory[id] = memBuf
	}
}

// pushMemHistories pushes memory usage percentage to the history buffer.
func pushMemHistories(s *Session, h *protocol.HostMetrics) {
	if h.MemTotal == 0 {
		return
	}
	s.HostMemUsedHistory.Push(h.MemPercent)
}
