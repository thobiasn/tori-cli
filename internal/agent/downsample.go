package agent

import "github.com/thobiasn/rook/internal/protocol"

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

// downsampleContainers reduces container metrics to at most n points per
// container using time-aware max-per-bucket aggregation. Containers with
// <= n data points are returned as-is to keep the response small (the TUI
// handles time-aware zero-fill locally for short series).
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
			out = append(out, series...)
			continue
		}
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
			if !filled[idx] {
				filled[idx] = true
				buckets[idx].ContainerMetrics = d.ContainerMetrics
				buckets[idx].Timestamp = start + int64(float64(idx+1)*bucketDur)
			} else {
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
		}
		out = append(out, buckets...)
	}
	return out
}
