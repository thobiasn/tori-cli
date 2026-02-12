package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
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

	// Runtime tracking state: names/projects that are tracked (positive list).
	tracked         map[string]bool // container names
	trackedProjects map[string]bool // compose project names

	// Periodic container disk size collection (Size: true is expensive).
	sizeCollectN int            // counter for periodic size requests
	cachedSizes  map[string]int64 // container ID → SizeRw (writable layer bytes)
}

type inspectResult struct {
	health       string
	startedAt    int64
	restartCount int
	exitCode     int
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
		tracked:         make(map[string]bool),
		trackedProjects: make(map[string]bool),
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

// SetTracking updates the runtime tracking state for a container name or project.
// Exactly one of name or project should be non-empty.
func (d *DockerCollector) SetTracking(name, project string, tracked bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if name != "" {
		if tracked {
			d.tracked[name] = true
		} else {
			delete(d.tracked, name)
		}
	}
	if project != "" {
		if tracked {
			d.trackedProjects[project] = true
		} else {
			delete(d.trackedProjects, project)
		}
	}
}

// IsTracked returns whether a container should be tracked (metrics, logs, alerts).
// A container is tracked if its name or its project is in the tracked set.
func (d *DockerCollector) IsTracked(name, project string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.tracked[name] {
		return true
	}
	if project != "" && d.trackedProjects[project] {
		return true
	}
	return false
}

// GetTrackingState returns the lists of tracked container names and project names.
func (d *DockerCollector) GetTrackingState() (containers, projects []string) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	for name := range d.tracked {
		containers = append(containers, name)
	}
	for name := range d.trackedProjects {
		projects = append(projects, name)
	}
	return
}

// LoadTrackingState bulk-loads persisted tracking state into the maps.
func (d *DockerCollector) LoadTrackingState(containers, projects []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, name := range containers {
		d.tracked[name] = true
	}
	for _, name := range projects {
		d.trackedProjects[name] = true
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

// serviceIdentity returns a stable (project, service) pair for cross-container
// history queries. Compose containers use their labels; non-compose named
// containers use ("", name) so queries match by name across recreations.
func serviceIdentity(project, service, name string) (identProject, identService string) {
	if project != "" && service != "" {
		return project, service
	}
	return "", name
}

// UpdateContainerState updates a single container's state in the cached list.
// If state is empty (destroy), the container is removed. If the container
// isn't in the list yet (event before first collect), it is appended.
func (d *DockerCollector) UpdateContainerState(id, state, name, image, project, service string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if state == "" {
		// Destroy: remove from list.
		for i, c := range d.lastContainers {
			if c.ID == id {
				d.lastContainers = append(d.lastContainers[:i], d.lastContainers[i+1:]...)
				return
			}
		}
		return
	}

	for i, c := range d.lastContainers {
		if c.ID == id {
			d.lastContainers[i].State = state
			return
		}
	}

	// Not found — event arrived before first collect.
	d.lastContainers = append(d.lastContainers, Container{
		ID:      id,
		Name:    name,
		Image:   image,
		State:   state,
		Project: project,
		Service: service,
	})
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
		var ir inspectResult
		if c.State != "running" {
			if cached, ok := d.inspectCache[c.ID]; ok {
				ir = cached
			} else {
				ir = d.inspectContainer(ctx, c.ID)
				d.inspectCache[c.ID] = ir
			}
		} else {
			delete(d.inspectCache, c.ID)
			ir = d.inspectContainer(ctx, c.ID)
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
		if !d.IsTracked(name, project) {
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
		m.DiskUsage = diskUsage
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
