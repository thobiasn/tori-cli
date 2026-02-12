package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/events"
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
		include:         include,
		exclude:         exclude,
		prevCPU:         make(map[string]cpuPrev),
		tracked:         map[string]bool{"web": true},
		trackedProjects: make(map[string]bool),
	}
	hub := NewHub()

	ew := &EventWatcher{
		docker: dc,
		hub:    hub,
		done:   make(chan struct{}),
	}

	src := newFakeEventSource()
	ew.eventsFn = src.fn
	return ew, dc, hub, src
}

func TestEventStart(t *testing.T) {
	ew, _, hub, src := testEventWatcher(t, nil, nil)

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
}

func TestEventDie(t *testing.T) {
	ew, _, hub, src := testEventWatcher(t, nil, nil)

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
}

func TestEventDestroy(t *testing.T) {
	ew, _, hub, src := testEventWatcher(t, nil, nil)

	_, ch := hub.Subscribe(TopicContainers)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ew.watch(ctx)

	src.msgCh <- events.Message{
		Action: events.ActionDestroy,
		Actor:  events.Actor{ID: "abc123", Attributes: map[string]string{"name": "web", "image": "nginx"}},
	}

	select {
	case msg := <-ch:
		event := msg.(*protocol.ContainerEvent)
		if event.State != "destroyed" {
			t.Errorf("state = %q, want destroyed", event.State)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

func TestEventFiltered(t *testing.T) {
	ew, _, hub, src := testEventWatcher(t, nil, []string{"internal-*"})

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

func TestEventPausePublished(t *testing.T) {
	ew, _, hub, src := testEventWatcher(t, nil, nil)

	_, ch := hub.Subscribe(TopicContainers)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ew.watch(ctx)

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
}
