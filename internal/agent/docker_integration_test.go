package agent

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/thobiasn/tori-cli/internal/protocol"
)

const testImage = "busybox:latest"

// skipIfNoDocker pings the Docker daemon and skips the test if unavailable.
func skipIfNoDocker(t *testing.T) {
	t.Helper()
	cli, err := client.NewClientWithOpts(client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("docker client: %v", err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := cli.Ping(ctx); err != nil {
		t.Skipf("docker not available: %v", err)
	}
}

// testDockerClient creates a Docker client with cleanup.
func testDockerClient(t *testing.T) *client.Client {
	t.Helper()
	cli, err := client.NewClientWithOpts(client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cli.Close() })
	return cli
}

type createOpts struct {
	name   string
	cmd    []string
	labels map[string]string
	start  bool
	// Resource limits.
	cpuNanos int64
	memBytes int64
}

// createTestContainer creates a container and registers cleanup.
func createTestContainer(t *testing.T, cli *client.Client, opts createOpts) string {
	t.Helper()
	ctx := context.Background()

	ensureImage(t, cli, testImage)

	cfg := &container.Config{
		Image:  testImage,
		Cmd:    opts.cmd,
		Labels: opts.labels,
	}
	hostCfg := &container.HostConfig{}
	if opts.cpuNanos > 0 {
		hostCfg.NanoCPUs = opts.cpuNanos
	}
	if opts.memBytes > 0 {
		hostCfg.Memory = opts.memBytes
	}

	resp, err := cli.ContainerCreate(ctx, cfg, hostCfg, nil, nil, opts.name)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		cli.ContainerRemove(context.Background(), resp.ID, container.RemoveOptions{Force: true})
	})

	if opts.start {
		if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	return resp.ID
}

// ensureImage pulls the image if not present locally.
func ensureImage(t *testing.T, cli *client.Client, ref string) {
	t.Helper()
	ctx := context.Background()

	_, _, err := cli.ImageInspectWithRaw(ctx, ref)
	if err == nil {
		return // already present
	}

	rc, err := cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		t.Fatalf("image pull %s: %v", ref, err)
	}
	defer rc.Close()
	io.Copy(io.Discard, rc)
}

// testContainerName returns a unique container name for the test.
func testContainerName(t *testing.T, suffix string) string {
	t.Helper()
	return fmt.Sprintf("tori-test-%s-%s-%d", t.Name(), suffix, time.Now().UnixNano())
}

// --- Group A: DockerCollector ---

func TestIntegrationNewDockerCollector(t *testing.T) {
	skipIfNoDocker(t)

	dc, err := NewDockerCollector(&DockerConfig{
		Socket: "/var/run/docker.sock",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dc.Close()

	if dc.Client() == nil {
		t.Error("Client() returned nil")
	}
}

func TestIntegrationCollectRunningContainers(t *testing.T) {
	skipIfNoDocker(t)

	cli := testDockerClient(t)
	project := "tori-inttest"

	name1 := testContainerName(t, "web")
	name2 := testContainerName(t, "api")

	createTestContainer(t, cli, createOpts{
		name:  name1,
		cmd:   []string{"sleep", "300"},
		start: true,
		labels: map[string]string{
			"com.docker.compose.project": project,
			"com.docker.compose.service": "web",
		},
	})
	createTestContainer(t, cli, createOpts{
		name:  name2,
		cmd:   []string{"sleep", "300"},
		start: true,
		labels: map[string]string{
			"com.docker.compose.project": project,
			"com.docker.compose.service": "api",
		},
	})

	dc, err := NewDockerCollector(&DockerConfig{
		Socket:  "/var/run/docker.sock",
		Include: []string{"tori-test-*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dc.Close()

	ctx := context.Background()
	metrics, tracked, err := dc.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Verify both test containers are in the tracked list.
	trackedNames := make(map[string]bool)
	for _, c := range tracked {
		trackedNames[c.Name] = true
	}
	if !trackedNames[name1] {
		t.Errorf("container %s not in tracked list", name1)
	}
	if !trackedNames[name2] {
		t.Errorf("container %s not in tracked list", name2)
	}

	// Verify metrics exist for our containers.
	metricsMap := make(map[string]ContainerMetrics)
	for _, m := range metrics {
		metricsMap[m.Name] = m
	}

	for _, name := range []string{name1, name2} {
		m, ok := metricsMap[name]
		if !ok {
			t.Errorf("no metrics for %s", name)
			continue
		}
		if m.State != "running" {
			t.Errorf("%s: state = %q, want running", name, m.State)
		}
		if m.MemUsage == 0 {
			t.Errorf("%s: MemUsage = 0, want > 0", name)
		}
		// CPU may be 0 for a sleeping container, so just check >= 0.
		if m.CPUPercent < 0 {
			t.Errorf("%s: CPUPercent = %f, want >= 0", name, m.CPUPercent)
		}
	}

	// Verify compose labels were extracted.
	m1 := metricsMap[name1]
	if m1.Project != project {
		t.Errorf("project = %q, want %q", m1.Project, project)
	}
	if m1.Service != "web" {
		t.Errorf("service = %q, want %q", m1.Service, "web")
	}

	// Verify Containers() returns all discovered containers (including our test ones).
	allContainers := dc.Containers()
	allNames := make(map[string]bool)
	for _, c := range allContainers {
		allNames[c.Name] = true
	}
	if !allNames[name1] || !allNames[name2] {
		t.Error("Containers() missing test containers")
	}
}

func TestIntegrationCollectStoppedContainer(t *testing.T) {
	skipIfNoDocker(t)

	cli := testDockerClient(t)
	name := testContainerName(t, "stopped")

	id := createTestContainer(t, cli, createOpts{
		name:  name,
		cmd:   []string{"sh", "-c", "exit 42"},
		start: true,
	})

	// Wait for container to exit.
	ctx := context.Background()
	statusCh, errCh := cli.ContainerWait(ctx, id, container.WaitConditionNotRunning)
	select {
	case <-statusCh:
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for container exit")
	}

	// ContainerWait can return before ContainerList reflects the new state.
	// Poll inspect until the state is confirmed.
	deadline := time.After(10 * time.Second)
	for {
		inspect, err := cli.ContainerInspect(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if inspect.State != nil && !inspect.State.Running {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for container inspect to show exited")
		case <-time.After(100 * time.Millisecond):
		}
	}

	dc, err := NewDockerCollector(&DockerConfig{
		Socket:  "/var/run/docker.sock",
		Include: []string{"tori-test-*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dc.Close()

	metrics, _, err := dc.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, m := range metrics {
		if m.Name == name {
			found = true
			if m.State != "exited" {
				t.Errorf("state = %q, want exited", m.State)
			}
			if m.ExitCode != 42 {
				t.Errorf("exit code = %d, want 42", m.ExitCode)
			}
			if m.MemUsage != 0 {
				t.Errorf("MemUsage = %d, want 0 for stopped container", m.MemUsage)
			}
			break
		}
	}
	if !found {
		t.Error("stopped container not found in metrics")
	}
}

func TestIntegrationCollectWithResourceLimits(t *testing.T) {
	skipIfNoDocker(t)

	cli := testDockerClient(t)
	name := testContainerName(t, "limited")

	createTestContainer(t, cli, createOpts{
		name:     name,
		cmd:      []string{"sleep", "300"},
		start:    true,
		cpuNanos: 1e9,              // 1 CPU
		memBytes: 64 * 1024 * 1024, // 64MB
	})

	dc, err := NewDockerCollector(&DockerConfig{
		Socket:  "/var/run/docker.sock",
		Include: []string{"tori-test-*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dc.Close()

	metrics, _, err := dc.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	for _, m := range metrics {
		if m.Name == name {
			if m.CPULimit < 0.9 || m.CPULimit > 1.1 {
				t.Errorf("CPULimit = %f, want ~1.0", m.CPULimit)
			}
			if m.MemLimit != 64*1024*1024 {
				t.Errorf("MemLimit = %d, want %d", m.MemLimit, 64*1024*1024)
			}
			return
		}
	}
	t.Error("limited container not found in metrics")
}

func TestIntegrationCollectAutoTracking(t *testing.T) {
	skipIfNoDocker(t)

	cli := testDockerClient(t)
	webName := testContainerName(t, "web-app")
	testName := testContainerName(t, "test-runner")

	createTestContainer(t, cli, createOpts{
		name:  webName,
		cmd:   []string{"sleep", "300"},
		start: true,
	})
	createTestContainer(t, cli, createOpts{
		name:  testName,
		cmd:   []string{"sleep", "300"},
		start: true,
	})

	dc, err := NewDockerCollector(&DockerConfig{
		Socket:  "/var/run/docker.sock",
		Include: []string{"*web-app*"},
		Exclude: []string{"*test-runner*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dc.Close()

	_, tracked, err := dc.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	trackedNames := make(map[string]bool)
	for _, c := range tracked {
		trackedNames[c.Name] = true
	}

	if !trackedNames[webName] {
		t.Errorf("%s should be tracked (matches include)", webName)
	}
	if trackedNames[testName] {
		t.Errorf("%s should not be tracked (matches exclude)", testName)
	}

	// Both should be visible via Containers().
	allNames := make(map[string]bool)
	for _, c := range dc.Containers() {
		allNames[c.Name] = true
	}
	if !allNames[webName] {
		t.Errorf("%s not visible in Containers()", webName)
	}
	if !allNames[testName] {
		t.Errorf("%s not visible in Containers()", testName)
	}
}

func TestIntegrationCollectStaleCleanup(t *testing.T) {
	skipIfNoDocker(t)

	cli := testDockerClient(t)
	name := testContainerName(t, "stale")

	id := createTestContainer(t, cli, createOpts{
		name:  name,
		cmd:   []string{"sleep", "300"},
		start: true,
	})

	dc, err := NewDockerCollector(&DockerConfig{
		Socket:  "/var/run/docker.sock",
		Include: []string{"tori-test-*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dc.Close()

	ctx := context.Background()

	// First collect: container exists.
	_, _, err = dc.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !dc.IsTracked(name) {
		t.Fatal("container should be tracked after first collect")
	}

	// Remove container.
	cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})

	// Second collect: container should be gone.
	_, _, err = dc.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Tracking entry should be pruned.
	state := dc.GetTrackingState()
	if _, exists := state[name]; exists {
		t.Error("tracking entry should be pruned after container removal")
	}

	// Should not appear in Containers().
	for _, c := range dc.Containers() {
		if c.Name == name {
			t.Error("removed container should not appear in Containers()")
		}
	}
}

// --- Group B: LogTailer ---

func TestIntegrationLogTailer(t *testing.T) {
	skipIfNoDocker(t)

	cli := testDockerClient(t)
	store := testStore(t)
	name := testContainerName(t, "logger")

	id := createTestContainer(t, cli, createOpts{
		name:  name,
		cmd:   []string{"sh", "-c", "echo hello-stdout; echo hello-stderr >&2; sleep 300"},
		start: true,
	})

	entries := make(chan LogEntry, 10)
	lt := NewLogTailer(cli, store)
	lt.onEntry = func(e LogEntry) {
		entries <- e
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	containers := []Container{
		{ID: id, Name: name, State: "running", StartedAt: time.Now().Unix()},
	}
	lt.Sync(ctx, containers)
	defer lt.Stop()

	// Wait for both stdout and stderr entries.
	var gotStdout, gotStderr bool
	timeout := time.After(10 * time.Second)
	for !gotStdout || !gotStderr {
		select {
		case e := <-entries:
			if e.ContainerID != id {
				continue
			}
			if e.Stream == "stdout" && strings.Contains(e.Message, "hello-stdout") {
				gotStdout = true
			}
			if e.Stream == "stderr" && strings.Contains(e.Message, "hello-stderr") {
				gotStderr = true
			}
		case <-timeout:
			t.Fatalf("timeout: gotStdout=%v gotStderr=%v", gotStdout, gotStderr)
		}
	}

	// Give a moment for the batch flush to persist to SQLite.
	time.Sleep(2 * time.Second)

	// Verify logs were persisted.
	var count int
	err := store.db.QueryRow("SELECT COUNT(*) FROM logs WHERE container_id = ?", id).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Error("no logs persisted to SQLite")
	}
}

func TestIntegrationLogTailerStopOnExit(t *testing.T) {
	skipIfNoDocker(t)

	cli := testDockerClient(t)
	store := testStore(t)
	name := testContainerName(t, "exiter")

	id := createTestContainer(t, cli, createOpts{
		name:  name,
		cmd:   []string{"sh", "-c", "echo done"},
		start: true,
	})

	entries := make(chan LogEntry, 10)
	lt := NewLogTailer(cli, store)
	lt.onEntry = func(e LogEntry) {
		entries <- e
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	containers := []Container{
		{ID: id, Name: name, State: "running", StartedAt: time.Now().Unix()},
	}
	lt.Sync(ctx, containers)

	// Wait for the log entry.
	timeout := time.After(10 * time.Second)
	select {
	case e := <-entries:
		if !strings.Contains(e.Message, "done") {
			t.Errorf("message = %q, want 'done'", e.Message)
		}
	case <-timeout:
		t.Fatal("timeout waiting for log entry")
	}

	// Wait for container to exit.
	statusCh, errCh := cli.ContainerWait(ctx, id, container.WaitConditionNotRunning)
	select {
	case <-statusCh:
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for container exit")
	}

	// Sync with empty list should clean up the tailer.
	lt.Sync(ctx, nil)

	// Stop should return promptly.
	done := make(chan struct{})
	go func() {
		lt.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() did not return promptly")
	}
}

// --- Group C: EventWatcher ---

func TestIntegrationEventWatcher(t *testing.T) {
	skipIfNoDocker(t)

	cli := testDockerClient(t)

	dc, err := NewDockerCollector(&DockerConfig{
		Socket:  "/var/run/docker.sock",
		Include: []string{"tori-test-*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dc.Close()

	hub := NewHub()
	ew := NewEventWatcher(dc, hub)

	sub, ch := hub.Subscribe(TopicContainers)
	defer hub.Unsubscribe(TopicContainers, sub)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ew.Run(ctx)

	// Create and start a container to generate events.
	name := testContainerName(t, "events")
	id := createTestContainer(t, cli, createOpts{
		name:  name,
		cmd:   []string{"sleep", "300"},
		start: true,
	})

	// Wait for a "start" event.
	waitForEvent := func(wantAction string) {
		t.Helper()
		timeout := time.After(10 * time.Second)
		for {
			select {
			case msg := <-ch:
				ev, ok := msg.(*protocol.ContainerEvent)
				if !ok {
					continue
				}
				if ev.ContainerID == id && ev.Action == wantAction {
					return
				}
			case <-timeout:
				t.Fatalf("timeout waiting for %s event on %s", wantAction, name)
			}
		}
	}

	waitForEvent("start")

	// Stop the container and wait for die event.
	stopTimeout := 3
	cli.ContainerStop(ctx, id, container.StopOptions{Timeout: &stopTimeout})
	waitForEvent("die")

	// Cancel and wait for clean shutdown.
	cancel()
	done := make(chan struct{})
	go func() {
		ew.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("EventWatcher.Wait() did not return promptly")
	}
}

// --- Group D: Full pipeline ---

func TestIntegrationFullPipeline(t *testing.T) {
	skipIfNoDocker(t)

	cli := testDockerClient(t)
	store := testStore(t)

	dc, err := NewDockerCollector(&DockerConfig{
		Socket:  "/var/run/docker.sock",
		Include: []string{"tori-test-*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dc.Close()

	hub := NewHub()
	ew := NewEventWatcher(dc, hub)

	name := testContainerName(t, "pipeline")
	createTestContainer(t, cli, createOpts{
		name:  name,
		cmd:   []string{"sh", "-c", "echo pipeline-log; sleep 300"},
		start: true,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Warm up the Docker client's API version negotiation before starting
	// concurrent access. WithAPIVersionNegotiation() causes a lazy write on
	// first request, which races if EventWatcher.Run() and Collect() hit the
	// client simultaneously.
	dc.Collect(ctx)

	// Start event watcher.
	go ew.Run(ctx)

	// Collect metrics.
	metrics, tracked, err := dc.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Verify metrics were collected.
	var foundMetric bool
	for _, m := range metrics {
		if m.Name == name {
			foundMetric = true
			if m.State != "running" {
				t.Errorf("metric state = %q, want running", m.State)
			}
			break
		}
	}
	if !foundMetric {
		t.Error("no metrics for pipeline container")
	}

	// Store metrics.
	ts := time.Now()
	if err := store.InsertContainerMetrics(ctx, ts, metrics); err != nil {
		t.Fatal(err)
	}

	// Start log tailer.
	logEntries := make(chan LogEntry, 10)
	lt := NewLogTailer(dc.Client(), store)
	lt.onEntry = func(e LogEntry) {
		logEntries <- e
	}
	lt.Sync(ctx, tracked)
	defer lt.Stop()

	// Wait for log entry.
	timeout := time.After(10 * time.Second)
	select {
	case e := <-logEntries:
		if !strings.Contains(e.Message, "pipeline-log") {
			t.Errorf("log message = %q, want 'pipeline-log'", e.Message)
		}
	case <-timeout:
		t.Fatal("timeout waiting for pipeline log entry")
	}

	// Verify container metrics in store.
	var storedCount int
	err = store.db.QueryRow("SELECT COUNT(*) FROM container_metrics WHERE service = ?", name).Scan(&storedCount)
	if err != nil {
		t.Fatal(err)
	}
	if storedCount == 0 {
		t.Error("no container metrics stored")
	}

	// Clean shutdown.
	cancel()
	ew.Wait()
}
