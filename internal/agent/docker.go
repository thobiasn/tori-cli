package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// DockerCollector monitors containers via the Docker API.
type DockerCollector struct {
	client  *client.Client
	include []string
	exclude []string

	// Previous CPU readings per container for delta calculation.
	prevCPU map[string]cpuPrev
}

type cpuPrev struct {
	containerCPU uint64
	systemCPU    uint64
}

// NewDockerCollector creates a collector using the given Docker socket path.
func NewDockerCollector(cfg *DockerConfig) (*DockerCollector, error) {
	c, err := client.NewClientWithOpts(
		client.WithHost("unix://"+cfg.Socket),
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &DockerCollector{
		client:  c,
		include: cfg.Include,
		exclude: cfg.Exclude,
		prevCPU: make(map[string]cpuPrev),
	}, nil
}

// Close closes the Docker client.
func (d *DockerCollector) Close() error {
	return d.client.Close()
}

// Client returns the underlying Docker client (used by LogTailer).
func (d *DockerCollector) Client() *client.Client {
	return d.client
}

// Container represents a discovered container with basic info.
type Container struct {
	ID    string
	Name  string
	Image string
	State string
}

// Collect lists containers, gets stats for each, and returns metrics.
func (d *DockerCollector) Collect(ctx context.Context) ([]ContainerMetrics, []Container, error) {
	containers, err := d.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, nil, fmt.Errorf("container list: %w", err)
	}

	var metrics []ContainerMetrics
	var discovered []Container

	for _, c := range containers {
		name := containerName(c.Names)
		if !d.matchFilter(name) {
			continue
		}

		discovered = append(discovered, Container{
			ID:    c.ID,
			Name:  name,
			Image: c.Image,
			State: c.State,
		})

		// Only get stats for running containers.
		if c.State != "running" {
			metrics = append(metrics, ContainerMetrics{
				ID:    c.ID,
				Name:  name,
				Image: c.Image,
				State: c.State,
			})
			continue
		}

		m, err := d.containerStats(ctx, c.ID, name, c.Image, c.State)
		if err != nil {
			slog.Warn("failed to get container stats", "container", name, "error", err)
			metrics = append(metrics, ContainerMetrics{
				ID:    c.ID,
				Name:  name,
				Image: c.Image,
				State: c.State,
			})
			continue
		}
		metrics = append(metrics, *m)
	}

	return metrics, discovered, nil
}

func (d *DockerCollector) containerStats(ctx context.Context, id, name, image, state string) (*ContainerMetrics, error) {
	resp, err := d.client.ContainerStatsOneShot(ctx, id)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var stats container.StatsResponse
	if err := json.Unmarshal(body, &stats); err != nil {
		return nil, err
	}

	cpuPct := d.calcCPUPercent(id, &stats)
	memUsage, memLimit, memPct := calcMemUsage(&stats)
	netRx, netTx := calcNetIO(&stats)
	blockRead, blockWrite := calcBlockIO(&stats)

	return &ContainerMetrics{
		ID:         id,
		Name:       name,
		Image:      image,
		State:      state,
		CPUPercent: cpuPct,
		MemUsage:   memUsage,
		MemLimit:   memLimit,
		MemPercent: memPct,
		NetRx:      netRx,
		NetTx:      netTx,
		BlockRead:  blockRead,
		BlockWrite: blockWrite,
		PIDs:       uint64(stats.PidsStats.Current),
	}, nil
}

// calcCPUPercent computes CPU percent from delta, same formula as `docker stats`.
func (d *DockerCollector) calcCPUPercent(id string, stats *container.StatsResponse) float64 {
	cpuTotal := stats.CPUStats.CPUUsage.TotalUsage
	systemCPU := stats.CPUStats.SystemUsage

	prev, hasPrev := d.prevCPU[id]
	d.prevCPU[id] = cpuPrev{containerCPU: cpuTotal, systemCPU: systemCPU}

	if !hasPrev {
		return CalcCPUPercentDelta(
			stats.PreCPUStats.CPUUsage.TotalUsage,
			cpuTotal,
			stats.PreCPUStats.SystemUsage,
			systemCPU,
			stats.CPUStats.OnlineCPUs,
		)
	}

	return CalcCPUPercentDelta(prev.containerCPU, cpuTotal, prev.systemCPU, systemCPU, stats.CPUStats.OnlineCPUs)
}

// CalcCPUPercentDelta computes CPU percent from counter deltas.
func CalcCPUPercentDelta(prevContainer, curContainer, prevSystem, curSystem uint64, onlineCPUs uint32) float64 {
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

// matchFilter checks if a container name matches include/exclude patterns.
func (d *DockerCollector) matchFilter(name string) bool {
	if len(d.include) > 0 {
		matched := false
		for _, pattern := range d.include {
			if ok, _ := filepath.Match(pattern, name); ok {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	for _, pattern := range d.exclude {
		if ok, _ := filepath.Match(pattern, name); ok {
			return false
		}
	}

	return true
}
