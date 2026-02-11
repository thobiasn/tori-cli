package agent

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/thobiasn/rook/internal/protocol"
)

// EventWatcher listens for Docker container lifecycle events and updates
// the agent's state in real time. It is an optimization for latency — the
// regular collect loop remains the consistency reconciliation point.
type EventWatcher struct {
	docker  *DockerCollector
	logs    *LogTailer
	hub     *Hub

	alerterMu sync.RWMutex
	alerter   *Alerter

	// Injectable for tests; production uses docker.client.Events.
	eventsFn func(ctx context.Context, opts events.ListOptions) (<-chan events.Message, <-chan error)

	done chan struct{} // closed when Run() exits
}

// NewEventWatcher creates an EventWatcher wired to the agent's components.
func NewEventWatcher(docker *DockerCollector, logs *LogTailer, alerter *Alerter, hub *Hub) *EventWatcher {
	ew := &EventWatcher{
		docker:  docker,
		logs:    logs,
		alerter: alerter,
		hub:     hub,
		done:    make(chan struct{}),
	}
	ew.eventsFn = docker.Client().Events
	return ew
}

// SetAlerter replaces the alerter used for evaluating container events.
func (ew *EventWatcher) SetAlerter(a *Alerter) {
	ew.alerterMu.Lock()
	defer ew.alerterMu.Unlock()
	ew.alerter = a
}

// Wait blocks until Run() has exited.
func (ew *EventWatcher) Wait() {
	<-ew.done
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
	// Docker exec actions have suffixes like "exec_create: /bin/sh".
	// Normalize by taking the prefix before ": ".
	if i := strings.Index(string(action), ": "); i >= 0 {
		action = events.Action(string(action)[:i])
	}

	isDestroy := action == events.ActionDestroy
	state, known := actionStateMap[action]
	if !known && !isDestroy {
		return // Ignore actions we don't care about.
	}

	id := truncate(msg.Actor.ID, maxContainerIDLen)
	attrs := msg.Actor.Attributes
	name := truncate(attrs["name"], maxNameLen)
	image := truncate(attrs["image"], maxImageLen)
	project := truncate(attrs["com.docker.compose.project"], maxLabelLen)

	if !ew.docker.MatchFilter(name) {
		return
	}

	// Update container list so TUI sees state changes (tracked or not).
	if isDestroy {
		ew.docker.UpdateContainerState(id, "", name, image, project)
	} else {
		ew.docker.UpdateContainerState(id, state, name, image, project)
	}

	// Skip untracked containers for log sync, alerts, and event publishing.
	if !ew.docker.IsTracked(name, project) {
		return
	}

	// Sync log tailers on state transitions that affect running state.
	syncActions := action == events.ActionStart || action == events.ActionDie ||
		action == events.ActionStop || action == events.ActionDestroy
	if syncActions {
		ew.logs.Sync(ctx, ew.docker.Containers())
	}

	// Evaluate container alerts on relevant state changes.
	alertActions := action == events.ActionStart || action == events.ActionDie ||
		action == events.ActionStop || action == events.ActionKill
	if alertActions {
		ew.alerterMu.RLock()
		alerter := ew.alerter
		ew.alerterMu.RUnlock()
		if alerter != nil {
			alerter.EvaluateContainerEvent(ctx, ContainerMetrics{
				ID:    id,
				Name:  name,
				Image: image,
				State: state,
			})
		}
	}

	// Publish event to hub.
	publishState := state
	if isDestroy {
		publishState = "destroyed"
	}
	ew.hub.Publish(TopicContainers, &protocol.ContainerEvent{
		Timestamp:   msg.Time,
		ContainerID: id,
		Name:        name,
		Image:       image,
		State:       publishState,
		Action:      truncate(string(action), maxActionLen),
		Project:     project,
	})
}
