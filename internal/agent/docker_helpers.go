package agent

import (
	"path/filepath"

	"github.com/docker/docker/api/types/container"
)

// CalcCPUPercentDelta computes CPU percent from counter deltas.
// Returns 0 if counters have reset (e.g. container restart).
func CalcCPUPercentDelta(prevContainer, curContainer, prevSystem, curSystem uint64, onlineCPUs uint32) float64 {
	// Guard against counter resets (container restart, system reboot).
	// Unsigned subtraction would wrap to a huge value.
	if curContainer < prevContainer || curSystem < prevSystem {
		return 0
	}

	containerDelta := float64(curContainer - prevContainer)
	systemDelta := float64(curSystem - prevSystem)

	if systemDelta <= 0 || containerDelta <= 0 {
		return 0
	}

	cpus := float64(onlineCPUs)
	if cpus == 0 {
		cpus = 1
	}

	return (containerDelta / systemDelta) * cpus * 100
}

// calcMemUsage returns memory usage, limit, and percent.
// Handles both cgroup v1 (stats.usage - inactive_file in stats) and v2 (usage_in_bytes from stats).
func calcMemUsage(stats *container.StatsResponse) (usage, limit uint64, pct float64) {
	limit = stats.MemoryStats.Limit
	usage = stats.MemoryStats.Usage

	// Subtract inactive file cache (cgroup v1 has it in Stats, v2 in Stats directly)
	if v, ok := stats.MemoryStats.Stats["inactive_file"]; ok && v > 0 {
		if usage > v {
			usage -= v
		}
	} else if v, ok := stats.MemoryStats.Stats["total_inactive_file"]; ok && v > 0 {
		if usage > v {
			usage -= v
		}
	}

	if limit > 0 {
		pct = float64(usage) / float64(limit) * 100
	}
	return
}

// calcNetIO sums rx/tx bytes across all container network interfaces.
func calcNetIO(stats *container.StatsResponse) (rx, tx uint64) {
	for _, n := range stats.Networks {
		rx += n.RxBytes
		tx += n.TxBytes
	}
	return
}

// calcBlockIO sums read/write bytes from block I/O stats.
func calcBlockIO(stats *container.StatsResponse) (read, write uint64) {
	for _, entry := range stats.BlkioStats.IoServiceBytesRecursive {
		switch entry.Op {
		case "read", "Read":
			read += entry.Value
		case "write", "Write":
			write += entry.Value
		}
	}
	return
}

// containerName extracts a clean name from Docker's name list.
func containerName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	// Docker prefixes names with "/", strip it.
	name := names[0]
	if len(name) > 0 && name[0] == '/' {
		name = name[1:]
	}
	return name
}

// serviceIdentity returns a stable (project, service) pair for cross-container
// history queries. Compose containers use their labels; non-compose named
// containers use ("", name) so queries match by name across recreations.
func serviceIdentity(project, service, name string) (identProject, identService string) {
	if project != "" && service != "" {
		return project, service
	}
	return "", name
}

// matchFilter checks if a container name matches include/exclude patterns.
func (d *DockerCollector) matchFilter(name string) bool {
	d.mu.RLock()
	include := d.include
	exclude := d.exclude
	d.mu.RUnlock()

	if len(include) > 0 {
		matched := false
		for _, pattern := range include {
			if ok, _ := filepath.Match(pattern, name); ok {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	for _, pattern := range exclude {
		if ok, _ := filepath.Match(pattern, name); ok {
			return false
		}
	}

	return true
}
