package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/client"
	"github.com/thobiasn/rook/internal/protocol"
)

// fakeEventSource provides injectable event channels for testing.
type fakeEventSource struct {
	mu    sync.Mutex
	calls int
	msgCh chan events.Message
	errCh chan error
}

func newFakeEventSource() *fakeEventSource {
	return &fakeEventSource{
		msgCh: make(chan events.Message, 16),
		errCh: make(chan error, 1),
	}
}

func (f *fakeEventSource) fn(ctx context.Context, opts events.ListOptions) (<-chan events.Message, <-chan error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.msgCh, f.errCh
}

func (f *fakeEventSource) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// testEventWatcher creates an EventWatcher with fake dependencies.
func testEventWatcher(t *testing.T, include, exclude []string) (*EventWatcher, *DockerCollector, *Hub, *fakeEventSource) {
	t.Helper()

	dc := &DockerCollector{
		include:           include,
		exclude:           exclude,
		prevCPU:           make(map[string]cpuPrev),
		untracked:         make(map[string]bool),
		untrackedProjects: make(map[string]bool),
	}
	hub := NewHub()
	// Use a Docker client with a dummy socket so ContainerLogs fails
	// gracefully (returns error) instead of panicking on nil client.
	dummyClient, _ := client.NewClientWithOpts(client.WithHost("unix:///dev/null"))
	lt := &LogTailer{
		client:  dummyClient,
		tailers: make(map[string]context.CancelFunc),
	}

	ew := &EventWatcher{
		docker: dc,
		logs:   lt,
		hub:    hub,
		done:   make(chan struct{}),
	}

	src := newFakeEventSource()
	ew.eventsFn = src.fn
	return ew, dc, hub, src
}

func TestEventStart(t *testing.T) {
	ew, dc, hub, src := testEventWatcher(t, nil, nil)

	// Subscribe to containers topic.
	_, ch := hub.Subscribe(TopicContainers)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ew.watch(ctx)

	src.msgCh <- events.Message{
		Action: events.ActionStart,
		Actor: events.Actor{
			ID: "abc123",
			Attributes: map[string]string{
				"name":                       "web",
				"image":                      "nginx:latest",
				"com.docker.compose.project": "myproject",
			},
		},
		Time: 1700000000,
	}

	// Wait for hub message.
	select {
	case msg := <-ch:
		event, ok := msg.(*protocol.ContainerEvent)
		if !ok {
			t.Fatalf("unexpected message type: %T", msg)
		}
		if event.ContainerID != "abc123" {
			t.Errorf("container_id = %q, want abc123", event.ContainerID)
		}
		if event.State != "running" {
			t.Errorf("state = %q, want running", event.State)
		}
		if event.Action != "start" {
			t.Errorf("action = %q, want start", event.Action)
		}
		if event.Name != "web" {
			t.Errorf("name = %q, want web", event.Name)
		}
		if event.Project != "myproject" {
			t.Errorf("project = %q, want myproject", event.Project)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for hub message")
	}

	// Verify container was added to list.
	containers := dc.Containers()
	if len(containers) != 1 {
		t.Fatalf("containers = %d, want 1", len(containers))
	}
	if containers[0].State != "running" {
		t.Errorf("container state = %q, want running", containers[0].State)
	}
}

func TestEventDie(t *testing.T) {
	ew, dc, hub, src := testEventWatcher(t, nil, nil)

	// Pre-populate container list.
	dc.mu.Lock()
	dc.lastContainers = []Container{{ID: "abc123", Name: "web", State: "running"}}
	dc.mu.Unlock()

	_, ch := hub.Subscribe(TopicContainers)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ew.watch(ctx)

	src.msgCh <- events.Message{
		Action: events.ActionDie,
		Actor:  events.Actor{ID: "abc123", Attributes: map[string]string{"name": "web", "image": "nginx"}},
		Time:   1700000001,
	}

	select {
	case msg := <-ch:
		event := msg.(*protocol.ContainerEvent)
		if event.State != "exited" {
			t.Errorf("state = %q, want exited", event.State)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}

	// Verify state updated.
	containers := dc.Containers()
	if len(containers) != 1 || containers[0].State != "exited" {
		t.Errorf("container state = %v, want exited", containers)
	}
}

func TestEventDestroy(t *testing.T) {
	ew, dc, hub, src := testEventWatcher(t, nil, nil)

	dc.mu.Lock()
	dc.lastContainers = []Container{{ID: "abc123", Name: "web", State: "exited"}}
	dc.mu.Unlock()

	// Subscribe to hub to synchronize on event processing.
	_, ch := hub.Subscribe(TopicContainers)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ew.watch(ctx)

	src.msgCh <- events.Message{
		Action: events.ActionDestroy,
		Actor:  events.Actor{ID: "abc123", Attributes: map[string]string{"name": "web", "image": "nginx"}},
	}

	// Wait for the hub message to confirm event was processed.
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}

	containers := dc.Containers()
	if len(containers) != 0 {
		t.Errorf("containers = %d, want 0 after destroy", len(containers))
	}
}

func TestEventFiltered(t *testing.T) {
	ew, dc, hub, src := testEventWatcher(t, nil, []string{"internal-*"})

	_, ch := hub.Subscribe(TopicContainers)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ew.watch(ctx)

	// Send event for excluded container.
	src.msgCh <- events.Message{
		Action: events.ActionStart,
		Actor:  events.Actor{ID: "xyz", Attributes: map[string]string{"name": "internal-monitor", "image": "prom"}},
	}

	// Send event for allowed container so we can verify ordering.
	src.msgCh <- events.Message{
		Action: events.ActionStart,
		Actor:  events.Actor{ID: "abc", Attributes: map[string]string{"name": "web", "image": "nginx"}},
	}

	select {
	case msg := <-ch:
		event := msg.(*protocol.ContainerEvent)
		if event.ContainerID != "abc" {
			t.Errorf("expected abc (allowed), got %s", event.ContainerID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}

	// Excluded container should not be in the list.
	for _, c := range dc.Containers() {
		if c.Name == "internal-monitor" {
			t.Error("excluded container should not be in list")
		}
	}
}

func TestEventIgnoredActions(t *testing.T) {
	ew, _, hub, src := testEventWatcher(t, nil, nil)

	_, ch := hub.Subscribe(TopicContainers)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ew.watch(ctx)

	// Send ignored actions.
	for _, action := range []events.Action{events.ActionAttach, events.ActionResize, events.ActionTop} {
		src.msgCh <- events.Message{
			Action: action,
			Actor:  events.Actor{ID: "abc", Attributes: map[string]string{"name": "web", "image": "nginx"}},
		}
	}

	// Send a real event so we can verify it's processed.
	src.msgCh <- events.Message{
		Action: events.ActionStart,
		Actor:  events.Actor{ID: "abc", Attributes: map[string]string{"name": "web", "image": "nginx"}},
	}

	select {
	case msg := <-ch:
		event := msg.(*protocol.ContainerEvent)
		if event.Action != "start" {
			t.Errorf("expected first hub message to be start, got %s", event.Action)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

func TestEventReconnectOnError(t *testing.T) {
	ew, _, _, src := testEventWatcher(t, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())

	// watch() should return on error.
	go func() {
		ew.watch(ctx)
		cancel()
	}()

	src.errCh <- context.DeadlineExceeded

	select {
	case <-ctx.Done():
		// watch exited as expected.
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("watch did not exit on error")
	}

	if src.callCount() != 1 {
		t.Errorf("eventsFn called %d times, want 1", src.callCount())
	}
}

func TestEventContextCancellation(t *testing.T) {
	ew, _, _, _ := testEventWatcher(t, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		ew.watch(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// Exited cleanly.
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not exit on context cancellation")
	}
}

func TestEventChannelClose(t *testing.T) {
	ew, _, _, src := testEventWatcher(t, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- ew.watch(ctx)
	}()

	close(src.msgCh)

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("expected nil error on channel close, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not exit on channel close")
	}
}

func TestEventDieTriggersAlertEvaluation(t *testing.T) {
	ew, dc, hub, src := testEventWatcher(t, nil, nil)

	// Set up alerter with a container state rule.
	alerts := map[string]AlertConfig{
		"exited": {
			Condition: "container.state == 'exited'",
			Severity:  "critical",
			Actions:   []string{"notify"},
		},
	}
	a, _ := testAlerter(t, alerts)
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	ew.alerter = a

	// Pre-populate container.
	dc.mu.Lock()
	dc.lastContainers = []Container{{ID: "abc123", Name: "web", State: "running"}}
	dc.mu.Unlock()

	// Subscribe to hub for synchronization.
	_, ch := hub.Subscribe(TopicContainers)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ew.watch(ctx)

	src.msgCh <- events.Message{
		Action: events.ActionDie,
		Actor:  events.Actor{ID: "abc123", Attributes: map[string]string{"name": "web", "image": "nginx"}},
	}

	// Wait for hub message to confirm event was fully processed.
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}

	a.mu.Lock()
	inst := a.instances["exited:abc123"]
	a.mu.Unlock()

	if inst == nil || inst.state != stateFiring {
		t.Error("expected exited:abc123 to be firing after die event")
	}
}

func TestEventExecCreateIgnored(t *testing.T) {
	ew, _, hub, src := testEventWatcher(t, nil, nil)

	_, ch := hub.Subscribe(TopicContainers)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ew.watch(ctx)

	// exec_create actions have a suffix with the command.
	src.msgCh <- events.Message{
		Action: "exec_create: /bin/sh -c 'echo hello'",
		Actor:  events.Actor{ID: "abc", Attributes: map[string]string{"name": "web", "image": "nginx"}},
	}

	// Send a real event to verify ordering.
	src.msgCh <- events.Message{
		Action: events.ActionStart,
		Actor:  events.Actor{ID: "abc", Attributes: map[string]string{"name": "web", "image": "nginx"}},
	}

	select {
	case msg := <-ch:
		event := msg.(*protocol.ContainerEvent)
		if event.Action != "start" {
			t.Errorf("expected start, got %s", event.Action)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

func TestEventPauseUnpause(t *testing.T) {
	ew, dc, hub, src := testEventWatcher(t, nil, nil)

	dc.mu.Lock()
	dc.lastContainers = []Container{{ID: "abc123", Name: "web", State: "running"}}
	dc.mu.Unlock()

	_, ch := hub.Subscribe(TopicContainers)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ew.watch(ctx)

	// Pause.
	src.msgCh <- events.Message{
		Action: events.ActionPause,
		Actor:  events.Actor{ID: "abc123", Attributes: map[string]string{"name": "web", "image": "nginx"}},
	}

	select {
	case msg := <-ch:
		event := msg.(*protocol.ContainerEvent)
		if event.State != "paused" {
			t.Errorf("state = %q, want paused", event.State)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}

	if dc.Containers()[0].State != "paused" {
		t.Errorf("container state = %q, want paused", dc.Containers()[0].State)
	}

	// Unpause.
	src.msgCh <- events.Message{
		Action: events.ActionUnPause,
		Actor:  events.Actor{ID: "abc123", Attributes: map[string]string{"name": "web", "image": "nginx"}},
	}

	select {
	case msg := <-ch:
		event := msg.(*protocol.ContainerEvent)
		if event.State != "running" {
			t.Errorf("state = %q, want running", event.State)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}

	if dc.Containers()[0].State != "running" {
		t.Errorf("container state = %q, want running", dc.Containers()[0].State)
	}
}

func TestEventRestartAction(t *testing.T) {
	ew, dc, hub, src := testEventWatcher(t, nil, nil)

	dc.mu.Lock()
	dc.lastContainers = []Container{{ID: "abc123", Name: "web", State: "running"}}
	dc.mu.Unlock()

	_, ch := hub.Subscribe(TopicContainers)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ew.watch(ctx)

	src.msgCh <- events.Message{
		Action: events.ActionRestart,
		Actor:  events.Actor{ID: "abc123", Attributes: map[string]string{"name": "web", "image": "nginx"}},
	}

	select {
	case msg := <-ch:
		event := msg.(*protocol.ContainerEvent)
		if event.State != "restarting" {
			t.Errorf("state = %q, want restarting", event.State)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}

	if dc.Containers()[0].State != "restarting" {
		t.Errorf("container state = %q, want restarting", dc.Containers()[0].State)
	}
}

func TestEventCreateAction(t *testing.T) {
	ew, dc, hub, src := testEventWatcher(t, nil, nil)

	_, ch := hub.Subscribe(TopicContainers)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ew.watch(ctx)

	src.msgCh <- events.Message{
		Action: events.ActionCreate,
		Actor:  events.Actor{ID: "abc123", Attributes: map[string]string{"name": "web", "image": "nginx"}},
	}

	select {
	case msg := <-ch:
		event := msg.(*protocol.ContainerEvent)
		if event.State != "created" {
			t.Errorf("state = %q, want created", event.State)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}

	containers := dc.Containers()
	if len(containers) != 1 || containers[0].State != "created" {
		t.Errorf("expected created container, got %v", containers)
	}
}

func TestEventNilAlerterNoPanic(t *testing.T) {
	ew, dc, hub, src := testEventWatcher(t, nil, nil)
	// alerter is nil by default in testEventWatcher

	dc.mu.Lock()
	dc.lastContainers = []Container{{ID: "abc123", Name: "web", State: "running"}}
	dc.mu.Unlock()

	_, ch := hub.Subscribe(TopicContainers)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ew.watch(ctx)

	// Die event with nil alerter should not panic.
	src.msgCh <- events.Message{
		Action: events.ActionDie,
		Actor:  events.Actor{ID: "abc123", Attributes: map[string]string{"name": "web", "image": "nginx"}},
	}

	select {
	case msg := <-ch:
		event := msg.(*protocol.ContainerEvent)
		if event.State != "exited" {
			t.Errorf("state = %q, want exited", event.State)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}

	// Container state should still be updated.
	if dc.Containers()[0].State != "exited" {
		t.Errorf("container state = %q, want exited", dc.Containers()[0].State)
	}
}
