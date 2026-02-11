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

	// Runtime tracking state: names/projects that are untracked.
	untracked         map[string]bool // container names
	untrackedProjects map[string]bool // compose project names
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
		client:            c,
		include:           cfg.Include,
		exclude:           cfg.Exclude,
		prevCPU:           make(map[string]cpuPrev),
		inspectCache:      make(map[string]inspectResult),
		untracked:         make(map[string]bool),
		untrackedProjects: make(map[string]bool),
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

// RestartContainer restarts a container by ID with a 10-second timeout.
func (d *DockerCollector) RestartContainer(ctx context.Context, containerID string) error {
	timeout := 10
	return d.client.ContainerRestart(ctx, containerID, container.StopOptions{Timeout: &timeout})
}

// SetTracking updates the runtime tracking state for a container name or project.
// Exactly one of name or project should be non-empty.
func (d *DockerCollector) SetTracking(name, project string, tracked bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if name != "" {
		if tracked {
			delete(d.untracked, name)
		} else {
			d.untracked[name] = true
		}
	}
	if project != "" {
		if tracked {
			delete(d.untrackedProjects, project)
		} else {
			d.untrackedProjects[project] = true
		}
	}
}

// IsTracked returns whether a container should be tracked (metrics, logs, alerts).
func (d *DockerCollector) IsTracked(name, project string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.untracked[name] {
		return false
	}
	if project != "" && d.untrackedProjects[project] {
		return false
	}
	return true
}

// GetTrackingState returns the lists of untracked container names and project names.
func (d *DockerCollector) GetTrackingState() (containers, projects []string) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	for name := range d.untracked {
		containers = append(containers, name)
	}
	for name := range d.untrackedProjects {
		projects = append(projects, name)
	}
	return
}

// Container represents a discovered container with basic info.
type Container struct {
	ID           string
	Name         string
	Image        string
	State        string
	Project      string // compose project from label
	Health       string
	StartedAt    int64
	RestartCount int
	ExitCode     int
}

// UpdateContainerState updates a single container's state in the cached list.
// If state is empty (destroy), the container is removed. If the container
// isn't in the list yet (event before first collect), it is appended.
func (d *DockerCollector) UpdateContainerState(id, state, name, image, project string) {
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
	})
}

// MatchFilter checks if a container name passes the include/exclude filters.
func (d *DockerCollector) MatchFilter(name string) bool {
	return d.matchFilter(name)
}

// Collect lists containers, gets stats for each, and returns metrics.
// The returned containers slice contains only tracked containers (for log sync
// and alert evaluation). All discovered containers (including untracked) are
// cached and available via Containers() for TUI visibility.
func (d *DockerCollector) Collect(ctx context.Context) ([]ContainerMetrics, []Container, error) {
	containers, err := d.client.ContainerList(ctx, container.ListOptions{All: true})
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
		var health string
		var startedAt int64
		var restartCount, exitCode int
		if c.State != "running" {
			if cached, ok := d.inspectCache[c.ID]; ok {
				health = cached.health
				startedAt = cached.startedAt
				restartCount = cached.restartCount
				exitCode = cached.exitCode
			} else {
				health, startedAt, restartCount, exitCode = d.inspectContainer(ctx, c.ID)
				d.inspectCache[c.ID] = inspectResult{health, startedAt, restartCount, exitCode}
			}
		} else {
			delete(d.inspectCache, c.ID)
			health, startedAt, restartCount, exitCode = d.inspectContainer(ctx, c.ID)
		}

		ctr := Container{
			ID:           c.ID,
			Name:         name,
			Image:        image,
			State:        c.State,
			Project:      project,
			Health:       health,
			StartedAt:    startedAt,
			RestartCount: restartCount,
			ExitCode:     exitCode,
		}
		all = append(all, ctr)

		// Skip metrics collection for untracked containers.
		if !d.IsTracked(name, project) {
			continue
		}
		tracked = append(tracked, ctr)

		// Only get stats for running containers.
		if c.State != "running" {
			metrics = append(metrics, ContainerMetrics{
				ID:           c.ID,
				Name:         name,
				Image:        image,
				State:        c.State,
				Health:       health,
				StartedAt:    startedAt,
				RestartCount: restartCount,
				ExitCode:     exitCode,
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
				Health:       health,
				StartedAt:    startedAt,
				RestartCount: restartCount,
				ExitCode:     exitCode,
			})
			continue
		}
		m.Health = health
		m.StartedAt = startedAt
		m.RestartCount = restartCount
		m.ExitCode = exitCode
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

	return metrics, tracked, nil
}

const maxHealthLen = 64

// inspectContainer calls ContainerInspect and extracts health, startedAt, restartCount, exitCode.
func (d *DockerCollector) inspectContainer(ctx context.Context, id string) (health string, startedAt int64, restartCount int, exitCode int) {
	health = "none"
	inspect, err := d.client.ContainerInspect(ctx, id)
	if err != nil {
		return
	}
	if inspect.State != nil {
		if inspect.State.Health != nil {
			health = truncate(inspect.State.Health.Status, maxHealthLen)
		}
		if t, err := time.Parse(time.RFC3339Nano, inspect.State.StartedAt); err == nil {
			startedAt = t.Unix()
		}
		exitCode = inspect.State.ExitCode
	}
	restartCount = inspect.RestartCount
	return
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
