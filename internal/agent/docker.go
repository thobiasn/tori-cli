package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

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

	// Cached container list from last Collect, protected by mu.
	lastContainers []Container
	mu             sync.RWMutex

	// Cached inspect results for non-running containers to avoid redundant API calls.
	inspectCache map[string]inspectResult

	// Runtime tracking state: container names that are tracked.
	tracked map[string]bool

	// Periodic container disk size collection (Size: true is expensive).
	sizeCollectN int            // counter for periodic size requests
	cachedSizes  map[string]int64 // container ID → SizeRw (writable layer bytes)
}

type inspectResult struct {
	health       string
	startedAt    int64
	restartCount int
	exitCode     int
	cpuLimit     float64   // configured CPU limit in cores (0 = no limit)
	memLimit     int64     // configured memory limit in bytes (0 = no limit)
	cachedAt     time.Time
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
		client:          c,
		include:         cfg.Include,
		exclude:         cfg.Exclude,
		prevCPU:         make(map[string]cpuPrev),
		inspectCache:    make(map[string]inspectResult),
		tracked:      make(map[string]bool),
		cachedSizes:     make(map[string]int64),
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

// Containers returns a copy of the most recently discovered containers.
func (d *DockerCollector) Containers() []Container {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]Container, len(d.lastContainers))
	copy(out, d.lastContainers)
	return out
}

// SetTracking updates the runtime tracking state.
// If project is set, all known containers in that project are toggled.
// If name is set, that single container is toggled.
func (d *DockerCollector) SetTracking(name, project string, tracked bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if project != "" {
		for _, c := range d.lastContainers {
			if c.Project == project {
				if tracked {
					d.tracked[c.Name] = true
				} else {
					delete(d.tracked, c.Name)
				}
			}
		}
	}
	if name != "" {
		if tracked {
			d.tracked[name] = true
		} else {
			delete(d.tracked, name)
		}
	}
}

// IsTracked returns whether a container should be tracked (metrics, logs, alerts).
func (d *DockerCollector) IsTracked(name string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.tracked[name]
}

// GetTrackingState returns the list of tracked container names.
func (d *DockerCollector) GetTrackingState() []string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	names := make([]string, 0, len(d.tracked))
	for name := range d.tracked {
		names = append(names, name)
	}
	return names
}

// LoadTrackingState bulk-loads persisted tracking state.
func (d *DockerCollector) LoadTrackingState(containers []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, name := range containers {
		d.tracked[name] = true
	}
}

// Container represents a discovered container with basic info.
type Container struct {
	ID           string
	Name         string
	Image        string
	State        string
	Project      string // compose project from label
	Service      string // compose service from label
	Health       string
	StartedAt    int64
	RestartCount int
	ExitCode     int
}

// SetFilters updates the include/exclude filter lists at runtime.
func (d *DockerCollector) SetFilters(include, exclude []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.include = include
	d.exclude = exclude
}

// MatchFilter checks if a container name passes the include/exclude filters.
func (d *DockerCollector) MatchFilter(name string) bool {
	return d.matchFilter(name)
}

// Collect lists containers, gets stats for each, and returns metrics.
// The returned containers slice contains only tracked containers (for log sync
// and alert evaluation). All discovered containers (including untracked) are
// cached and available via Containers() for TUI visibility.
// sizeCollectInterval controls how often we pass Size: true to ContainerList.
// Size: true is expensive (requires diff per container), so we only do it
// every 12th call (~1 min at 5s collect interval).
const sizeCollectInterval = 12

// inspectCacheTTL controls how long inspect results are cached before refresh.
const inspectCacheTTL = 30 * time.Second

func (d *DockerCollector) Collect(ctx context.Context) ([]ContainerMetrics, []Container, error) {
	collectSize := d.sizeCollectN%sizeCollectInterval == 0
	d.sizeCollectN++

	containers, err := d.client.ContainerList(ctx, container.ListOptions{All: true, Size: collectSize})
	if err != nil {
		return nil, nil, fmt.Errorf("container list: %w", err)
	}

	var metrics []ContainerMetrics
	var all []Container     // for lastContainers (TUI visibility)
	var tracked []Container // returned for log sync / alert eval

	for _, c := range containers {
		name := containerName(c.Names)
		if !d.matchFilter(name) {
			continue
		}

		// Inspect for health, startedAt, restartCount, exitCode.
		// Cache results for non-running containers; evict running ones for fresh data.
		image := truncate(c.Image, maxImageLen)
		project := c.Labels["com.docker.compose.project"]
		service := c.Labels["com.docker.compose.service"]
		// Cache inspect results with a TTL. Running containers refresh every
		// inspectCacheTTL; non-running containers rarely change state so
		// their cached results are reused until eviction.
		var ir inspectResult
		if cached, ok := d.inspectCache[c.ID]; ok && time.Since(cached.cachedAt) < inspectCacheTTL {
			ir = cached
		} else {
			ir = d.inspectContainer(ctx, c.ID)
			ir.cachedAt = time.Now()
			d.inspectCache[c.ID] = ir
		}

		// Cache container disk size when collected; use cached value otherwise.
		if collectSize && c.SizeRw > 0 {
			d.cachedSizes[c.ID] = c.SizeRw
		}
		var diskUsage uint64
		if sz, ok := d.cachedSizes[c.ID]; ok && sz > 0 {
			diskUsage = uint64(sz)
		}

		ctr := Container{
			ID:           c.ID,
			Name:         name,
			Image:        image,
			State:        c.State,
			Project:      project,
			Service:      service,
			Health:       ir.health,
			StartedAt:    ir.startedAt,
			RestartCount: ir.restartCount,
			ExitCode:     ir.exitCode,
		}
		all = append(all, ctr)

		// Skip metrics collection for untracked containers.
		if !d.IsTracked(name) {
			continue
		}
		tracked = append(tracked, ctr)

		idProject, idService := serviceIdentity(project, service, name)

		// Only get stats for running containers.
		if c.State != "running" {
			metrics = append(metrics, ContainerMetrics{
				ID:           c.ID,
				Name:         name,
				Image:        image,
				State:        c.State,
				Project:      idProject,
				Service:      idService,
				Health:       ir.health,
				StartedAt:    ir.startedAt,
				RestartCount: ir.restartCount,
				ExitCode:     ir.exitCode,
				CPULimit:     ir.cpuLimit,
				DiskUsage:    diskUsage,
			})
			continue
		}

		m, err := d.containerStats(ctx, c.ID, name, image, c.State)
		if err != nil {
			slog.Warn("failed to get container stats", "container", name, "error", err)
			metrics = append(metrics, ContainerMetrics{
				ID:           c.ID,
				Name:         name,
				Image:        image,
				State:        c.State,
				Project:      idProject,
				Service:      idService,
				Health:       ir.health,
				StartedAt:    ir.startedAt,
				RestartCount: ir.restartCount,
				ExitCode:     ir.exitCode,
				CPULimit:     ir.cpuLimit,
				DiskUsage:    diskUsage,
			})
			continue
		}
		m.Project = idProject
		m.Service = idService
		m.Health = ir.health
		m.StartedAt = ir.startedAt
		m.RestartCount = ir.restartCount
		m.ExitCode = ir.exitCode
		m.CPULimit = ir.cpuLimit
		m.DiskUsage = diskUsage
		// Override MemLimit: use configured limit from inspect (0 = no configured limit).
		// The stats-based MemLimit equals host total memory when uncapped, which can't
		// distinguish "has limit" from "no limit". MemPercent is already computed from
		// stats before this override, so it stays correct in both cases.
		if ir.memLimit > 0 {
			m.MemLimit = uint64(ir.memLimit)
		} else {
			m.MemLimit = 0
		}
		metrics = append(metrics, *m)
	}

	d.mu.Lock()
	d.lastContainers = all
	d.mu.Unlock()

	// Evict stale inspect cache entries for containers no longer present.
	seen := make(map[string]bool, len(all))
	for _, c := range all {
		seen[c.ID] = true
	}
	for id := range d.inspectCache {
		if !seen[id] {
			delete(d.inspectCache, id)
		}
	}
	for id := range d.cachedSizes {
		if !seen[id] {
			delete(d.cachedSizes, id)
		}
	}
	for id := range d.prevCPU {
		if !seen[id] {
			delete(d.prevCPU, id)
		}
	}

	return metrics, tracked, nil
}

const maxHealthLen = 64

// inspectContainer calls ContainerInspect and extracts health, startedAt, restartCount, exitCode.
func (d *DockerCollector) inspectContainer(ctx context.Context, id string) inspectResult {
	r := inspectResult{health: "none"}
	inspect, err := d.client.ContainerInspect(ctx, id)
	if err != nil {
		return r
	}
	if inspect.State != nil {
		if inspect.State.Health != nil {
			r.health = truncate(inspect.State.Health.Status, maxHealthLen)
		}
		if t, err := time.Parse(time.RFC3339Nano, inspect.State.StartedAt); err == nil {
			r.startedAt = t.Unix()
		}
		r.exitCode = inspect.State.ExitCode
	}
	r.restartCount = inspect.RestartCount
	if inspect.HostConfig != nil {
		if inspect.HostConfig.NanoCPUs > 0 {
			r.cpuLimit = float64(inspect.HostConfig.NanoCPUs) / 1e9
		} else if inspect.HostConfig.CPUQuota > 0 && inspect.HostConfig.CPUPeriod > 0 {
			r.cpuLimit = float64(inspect.HostConfig.CPUQuota) / float64(inspect.HostConfig.CPUPeriod)
		}
		r.memLimit = inspect.HostConfig.Memory // 0 = no limit
	}
	return r
}

func (d *DockerCollector) containerStats(ctx context.Context, id, name, image, state string) (*ContainerMetrics, error) {
	resp, err := d.client.ContainerStatsOneShot(ctx, id)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var stats container.StatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
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
