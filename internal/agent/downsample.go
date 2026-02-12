package agent

import (
	"sort"

	"github.com/thobiasn/rook/internal/protocol"
)

// downsampleHost reduces a host metric slice to exactly n points using
// time-aware max-per-bucket aggregation. The [start, end] time range is
// divided into n equal buckets; data points are assigned by timestamp.
// Empty buckets produce zero-value entries so partial data doesn't stretch
// across the full graph width.
func downsampleHost(data []protocol.TimedHostMetrics, n int, start, end int64) []protocol.TimedHostMetrics {
	if n <= 0 || len(data) == 0 {
		return data
	}
	bucketDur := float64(end-start) / float64(n)
	if bucketDur <= 0 {
		return data
	}
	out := make([]protocol.TimedHostMetrics, n)
	filled := make([]bool, n)
	for i := range out {
		out[i].Timestamp = start + int64(float64(i+1)*bucketDur)
	}
	for _, d := range data {
		idx := int(float64(d.Timestamp-start) / bucketDur)
		if idx < 0 {
			idx = 0
		}
		if idx >= n {
			idx = n - 1
		}
		if !filled[idx] {
			filled[idx] = true
			out[idx].HostMetrics = d.HostMetrics
			out[idx].Timestamp = start + int64(float64(idx+1)*bucketDur)
		} else {
			b := &out[idx]
			if d.CPUPercent > b.CPUPercent {
				b.CPUPercent = d.CPUPercent
			}
			if d.MemPercent > b.MemPercent {
				b.MemPercent = d.MemPercent
			}
			if d.MemUsed > b.MemUsed {
				b.MemUsed = d.MemUsed
			}
			if d.Load1 > b.Load1 {
				b.Load1 = d.Load1
			}
			if d.Load5 > b.Load5 {
				b.Load5 = d.Load5
			}
			if d.Load15 > b.Load15 {
				b.Load15 = d.Load15
			}
		}
	}
	return out
}

// downsampleContainers reduces container metrics to exactly n points per
// container using time-aware max-per-bucket aggregation. Empty buckets are
// zero-filled so the TUI receives ready-to-display time series.
func downsampleContainers(data []protocol.TimedContainerMetrics, n int, start, end int64) []protocol.TimedContainerMetrics {
	if n <= 0 || len(data) == 0 {
		return data
	}
	bucketDur := float64(end-start) / float64(n)
	if bucketDur <= 0 {
		return data
	}
	// Group by container ID.
	byID := make(map[string][]protocol.TimedContainerMetrics)
	var order []string
	for _, m := range data {
		if _, seen := byID[m.ID]; !seen {
			order = append(order, m.ID)
		}
		byID[m.ID] = append(byID[m.ID], m)
	}
	var out []protocol.TimedContainerMetrics
	for _, id := range order {
		series := byID[id]
		if len(series) <= n {
			// Zero-fill: expand sparse series to exactly n points.
			buckets := make([]protocol.TimedContainerMetrics, n)
			filled := make([]bool, n)
			for i := range buckets {
				buckets[i].Timestamp = start + int64(float64(i+1)*bucketDur)
				buckets[i].ID = id
			}
			for _, d := range series {
				idx := int(float64(d.Timestamp-start) / bucketDur)
				if idx < 0 {
					idx = 0
				}
				if idx >= n {
					idx = n - 1
				}
				if !filled[idx] || d.CPUPercent > buckets[idx].CPUPercent {
					buckets[idx].ContainerMetrics = d.ContainerMetrics
					buckets[idx].Timestamp = start + int64(float64(idx+1)*bucketDur)
					filled[idx] = true
				}
			}
			out = append(out, buckets...)
			continue
		}
		buckets := make([]protocol.TimedContainerMetrics, n)
		for i := range buckets {
			buckets[i].Timestamp = start + int64(float64(i+1)*bucketDur)
			buckets[i].ID = id
		}
		for _, d := range series {
			idx := int(float64(d.Timestamp-start) / bucketDur)
			if idx < 0 {
				idx = 0
			}
			if idx >= n {
				idx = n - 1
			}
			b := &buckets[idx]
			if d.CPUPercent > b.CPUPercent {
				b.CPUPercent = d.CPUPercent
			}
			if d.MemUsage > b.MemUsage {
				b.MemUsage = d.MemUsage
			}
			if d.MemPercent > b.MemPercent {
				b.MemPercent = d.MemPercent
			}
		}
		out = append(out, buckets...)
	}
	return out
}

// mergeContainersByService collapses data from containers sharing the same
// compose service identity {project, service} into the most recent container's
// ID. This enables cross-deploy graph continuity. It returns deploy boundary
// markers (timestamps where the container ID changed within a service).
//
// Scaled services (multiple running containers with the same identity) and
// non-compose containers (empty Service field) are left untouched.
func mergeContainersByService(data []protocol.TimedContainerMetrics) ([]protocol.TimedContainerMetrics, map[string][]int64) {
	if len(data) == 0 {
		return data, nil
	}

	// Build {project, service} -> []containerID mapping.
	type svcKey struct{ project, service string }
	type cidInfo struct {
		latestTS int64
		running  bool
	}

	svcContainers := make(map[svcKey]map[string]*cidInfo)
	for _, d := range data {
		if d.Service == "" {
			continue
		}
		key := svcKey{d.Project, d.Service}
		cids, ok := svcContainers[key]
		if !ok {
			cids = make(map[string]*cidInfo)
			svcContainers[key] = cids
		}
		info, ok := cids[d.ID]
		if !ok {
			info = &cidInfo{}
			cids[d.ID] = info
		}
		if d.Timestamp > info.latestTS {
			info.latestTS = d.Timestamp
			info.running = d.State == "running"
		}
	}

	// Determine which services need merging: must have 2+ container IDs
	// and at most 1 currently running.
	type mergeTarget struct {
		currentID string
		oldIDs    map[string]bool
	}
	merges := make(map[svcKey]*mergeTarget)
	for key, cids := range svcContainers {
		if len(cids) < 2 {
			continue
		}
		runningCount := 0
		for _, info := range cids {
			if info.running {
				runningCount++
			}
		}
		if runningCount > 1 {
			continue // Scaled service — don't merge.
		}

		// Find the most recent container ID.
		var bestID string
		var bestTS int64
		for id, info := range cids {
			if info.latestTS > bestTS {
				bestTS = info.latestTS
				bestID = id
			}
		}
		mt := &mergeTarget{currentID: bestID, oldIDs: make(map[string]bool)}
		for id := range cids {
			if id != bestID {
				mt.oldIDs[id] = true
			}
		}
		merges[key] = mt
	}

	if len(merges) == 0 {
		return data, nil
	}

	// Build a rewrite map: oldID -> currentID.
	rewrite := make(map[string]string)
	for _, mt := range merges {
		for oldID := range mt.oldIDs {
			rewrite[oldID] = mt.currentID
		}
	}

	// Rewrite container IDs and collect all points per merged ID.
	out := make([]protocol.TimedContainerMetrics, len(data))
	for i, d := range data {
		out[i] = d
		if newID, ok := rewrite[d.ID]; ok {
			out[i].ContainerMetrics.ID = newID
		}
	}

	// Sort by timestamp for deploy marker detection.
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Timestamp < out[j].Timestamp
	})

	// Detect deploy boundaries: track the last-seen original container ID
	// per merged service and record transitions.
	deployMarkers := make(map[string][]int64)
	lastCID := make(map[string]string) // mergedID -> last original container ID seen
	for _, d := range data {
		mergedID := d.ID
		if newID, ok := rewrite[d.ID]; ok {
			mergedID = newID
		} else {
			// Only track containers that were part of a merge.
			found := false
			for _, mt := range merges {
				if mt.currentID == d.ID {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		prev, seen := lastCID[mergedID]
		if seen && prev != d.ID {
			deployMarkers[mergedID] = append(deployMarkers[mergedID], d.Timestamp)
		}
		lastCID[mergedID] = d.ID
	}

	return out, deployMarkers
}
