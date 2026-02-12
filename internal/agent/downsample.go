package agent

import (
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

// serviceKey returns a grouping key for container metrics: "project\x00service".
func serviceKey(project, service string) string {
	return project + "\x00" + service
}

// downsampleContainers reduces container metrics to exactly n points per
// service using time-aware max-per-bucket aggregation. Empty buckets are
// zero-filled so the TUI receives ready-to-display time series.
// Data is grouped by synthetic identity (project, service).
func downsampleContainers(data []protocol.TimedContainerMetrics, n int, start, end int64) []protocol.TimedContainerMetrics {
	if n <= 0 || len(data) == 0 {
		return data
	}
	bucketDur := float64(end-start) / float64(n)
	if bucketDur <= 0 {
		return data
	}
	// Group by service identity.
	type svcEntry struct {
		project string
		service string
	}
	bySvc := make(map[string][]protocol.TimedContainerMetrics)
	var order []string
	var svcInfo []svcEntry
	for _, m := range data {
		key := serviceKey(m.Project, m.Service)
		if _, seen := bySvc[key]; !seen {
			order = append(order, key)
			svcInfo = append(svcInfo, svcEntry{m.Project, m.Service})
		}
		bySvc[key] = append(bySvc[key], m)
	}
	var out []protocol.TimedContainerMetrics
	for i, key := range order {
		series := bySvc[key]
		info := svcInfo[i]
		if len(series) <= n {
			// Zero-fill: expand sparse series to exactly n points.
			buckets := make([]protocol.TimedContainerMetrics, n)
			filled := make([]bool, n)
			for j := range buckets {
				buckets[j].Timestamp = start + int64(float64(j+1)*bucketDur)
				buckets[j].Project = info.project
				buckets[j].Service = info.service
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
		for j := range buckets {
			buckets[j].Timestamp = start + int64(float64(j+1)*bucketDur)
			buckets[j].Project = info.project
			buckets[j].Service = info.service
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
