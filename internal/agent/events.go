package agent

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/thobiasn/tori-cli/internal/protocol"
)

// EventWatcher listens for Docker container lifecycle events and publishes
// them to the hub for real-time TUI updates. The regular collect loop remains
// the consistency reconciliation point for container state, log sync, and alerts.
type EventWatcher struct {
	docker *DockerCollector
	hub    *Hub

	alerterMu sync.RWMutex
	alerter   *Alerter

	// Injectable for tests; production uses docker.client.Events.
	eventsFn func(ctx context.Context, opts events.ListOptions) (<-chan events.Message, <-chan error)

	done chan struct{} // closed when Run() exits
}

// NewEventWatcher creates an EventWatcher wired to the agent's components.
func NewEventWatcher(docker *DockerCollector, hub *Hub) *EventWatcher {
	ew := &EventWatcher{
		docker: docker,
		hub:    hub,
		done:   make(chan struct{}),
	}
	ew.eventsFn = docker.Client().Events
	return ew
}

// Wait blocks until Run() has exited.
func (ew *EventWatcher) Wait() {
	<-ew.done
}

// SetAlerter replaces the alerter used for event-driven alert evaluation.
func (ew *EventWatcher) SetAlerter(a *Alerter) {
	ew.alerterMu.Lock()
	defer ew.alerterMu.Unlock()
	ew.alerter = a
}

// Run starts the event watcher. It reconnects with exponential backoff on
// stream errors and exits when ctx is cancelled.
func (ew *EventWatcher) Run(ctx context.Context) {
	defer close(ew.done)
	backoff := 1 * time.Second
	const maxBackoff = 30 * time.Second

	for {
		start := time.Now()
		err := ew.watch(ctx)
		if ctx.Err() != nil {
			return
		}

		// Reset backoff after a long-lived healthy connection.
		if time.Since(start) > maxBackoff {
			backoff = 1 * time.Second
		}

		if err != nil {
			slog.Warn("docker events stream error, reconnecting", "error", err, "backoff", backoff)
		} else {
			slog.Info("docker events stream closed, reconnecting", "backoff", backoff)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (ew *EventWatcher) watch(ctx context.Context) error {
	opts := events.ListOptions{
		Filters: filters.NewArgs(filters.Arg("type", "container")),
	}
	msgCh, errCh := ew.eventsFn(ctx, opts)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errCh:
			return err
		case msg, ok := <-msgCh:
			if !ok {
				return nil
			}
			ew.handleEvent(ctx, msg)
		}
	}
}

// actionStateMap maps Docker actions to the container state they produce.
var actionStateMap = map[events.Action]string{
	events.ActionCreate:  "created",
	events.ActionStart:   "running",
	events.ActionDie:     "exited",
	events.ActionStop:    "exited",
	events.ActionKill:    "exited",
	events.ActionRestart: "restarting",
	events.ActionPause:   "paused",
	events.ActionUnPause: "running",
}

// truncate returns s limited to max bytes.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

const (
	maxContainerIDLen = 64
	maxNameLen        = 256
	maxImageLen       = 512
	maxLabelLen       = 256
	maxActionLen      = 64
)

func (ew *EventWatcher) handleEvent(ctx context.Context, msg events.Message) {
	action := msg.Action
	actionStr := string(action)

	// health_status events have a suffix like "health_status: unhealthy".
	// Extract the health value before the generic ": " stripping.
	var health string
	if strings.HasPrefix(actionStr, "health_status") {
		if i := strings.Index(actionStr, ": "); i >= 0 {
			health = actionStr[i+2:]
		}
		action = "health_status"
	} else if i := strings.Index(actionStr, ": "); i >= 0 {
		// Docker exec actions have suffixes like "exec_create: /bin/sh".
		action = events.Action(actionStr[:i])
	}

	isDestroy := action == events.ActionDestroy
	isHealth := action == "health_status"
	state, known := actionStateMap[action]
	if !known && !isDestroy && !isHealth {
		return // Ignore actions we don't care about.
	}

	id := truncate(msg.Actor.ID, maxContainerIDLen)
	attrs := msg.Actor.Attributes
	name := truncate(attrs["name"], maxNameLen)
	image := truncate(attrs["image"], maxImageLen)
	project := truncate(attrs["com.docker.compose.project"], maxLabelLen)
	service := truncate(attrs["com.docker.compose.service"], maxLabelLen)

	if !ew.docker.MatchFilter(name) {
		return
	}

	// Publish event to hub.
	publishState := state
	if isDestroy {
		publishState = "destroyed"
	} else if isHealth {
		publishState = "running" // container is still running during health checks
	}
	event := &protocol.ContainerEvent{
		Timestamp:   msg.Time,
		ContainerID: id,
		Name:        name,
		Image:       image,
		State:       publishState,
		Action:      truncate(string(action), maxActionLen),
		Project:     project,
		Service:     service,
		Health:      truncate(health, maxNameLen),
	}
	ew.hub.Publish(TopicContainers, event)

	// Trigger alert evaluation for state/health changes.
	if !isDestroy {
		ew.alerterMu.RLock()
		alerter := ew.alerter
		ew.alerterMu.RUnlock()

		if alerter != nil {
			cm := ContainerMetrics{
				ID:      id,
				Name:    name,
				State:   state,
				Health:  health,
				Project: project,
				Service: service,
			}
			if isHealth {
				cm.State = "running"
			}
			alerter.EvaluateContainerEvent(ctx, cm)
		}
	}
}
