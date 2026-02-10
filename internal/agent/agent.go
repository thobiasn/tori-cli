package agent

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/thobiasn/rook/internal/protocol"
)

// Agent orchestrates metric collection, log tailing, and storage.
type Agent struct {
	cfg     *Config
	store   *Store
	host    *HostCollector
	docker  *DockerCollector
	logs    *LogTailer
	alerter *Alerter
	events  *EventWatcher
	hub     *Hub
	socket  *SocketServer

	lastPrune time.Time
}

// New creates an Agent from the given config.
func New(cfg *Config) (*Agent, error) {
	store, err := OpenStore(cfg.Storage.Path)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}

	docker, err := NewDockerCollector(&cfg.Docker)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("docker collector: %w", err)
	}

	hub := NewHub()
	lt := NewLogTailer(docker.Client(), store)
	lt.onEntry = func(e LogEntry) {
		hub.Publish(TopicLogs, &protocol.LogEntryMsg{
			Timestamp:     e.Timestamp.Unix(),
			ContainerID:   e.ContainerID,
			ContainerName: e.ContainerName,
			Stream:        e.Stream,
			Message:       e.Message,
		})
	}

	a := &Agent{
		cfg:    cfg,
		store:  store,
		host:   NewHostCollector(&cfg.Host),
		docker: docker,
		logs:   lt,
		hub:    hub,
	}

	if len(cfg.Alerts) > 0 {
		notifier := NewNotifier(&cfg.Notify)
		alerter, err := NewAlerter(cfg.Alerts, store, notifier, docker)
		if err != nil {
			store.Close()
			docker.Close()
			return nil, fmt.Errorf("alerter: %w", err)
		}
		alerter.onStateChange = func(alert *Alert, state string) {
			event := &protocol.AlertEvent{
				ID:          alert.ID,
				RuleName:    alert.RuleName,
				Severity:    alert.Severity,
				Condition:   alert.Condition,
				InstanceKey: alert.InstanceKey,
				FiredAt:     alert.FiredAt.Unix(),
				Message:     alert.Message,
				State:       state,
			}
			if alert.ResolvedAt != nil {
				event.ResolvedAt = alert.ResolvedAt.Unix()
			}
			hub.Publish(TopicAlerts, event)
		}
		a.alerter = alerter
	}

	a.events = NewEventWatcher(docker, lt, a.alerter, hub)
	a.socket = NewSocketServer(hub, store, docker, a.alerter)
	return a, nil
}

// Run starts the collection loop and blocks until the context is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	slog.Info("agent starting",
		"interval", a.cfg.Collect.Interval.Duration,
		"db", a.cfg.Storage.Path,
		"retention_days", a.cfg.Storage.RetentionDays,
	)

	if err := a.socket.Start(a.cfg.Socket.Path); err != nil {
		return fmt.Errorf("start socket: %w", err)
	}

	go a.events.Run(ctx)

	// Collect immediately on startup.
	a.collect(ctx)

	ticker := time.NewTicker(a.cfg.Collect.Interval.Duration)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return a.shutdown()
		case <-ticker.C:
			a.collect(ctx)
		}
	}
}

func (a *Agent) collect(ctx context.Context) {
	ts := time.Now()

	// Host metrics.
	hostMetrics, diskMetrics, netMetrics, err := a.host.Collect()
	if err != nil {
		slog.Error("host collect failed", "error", err)
	} else {
		if err := a.store.InsertHostMetrics(ctx, ts, hostMetrics); err != nil {
			slog.Error("insert host metrics", "error", err)
		}
		if err := a.store.InsertDiskMetrics(ctx, ts, diskMetrics); err != nil {
			slog.Error("insert disk metrics", "error", err)
		}
		if err := a.store.InsertNetMetrics(ctx, ts, netMetrics); err != nil {
			slog.Error("insert net metrics", "error", err)
		}
	}

	// Docker metrics.
	containerMetrics, containers, err := a.docker.Collect(ctx)
	if err != nil {
		slog.Error("docker collect failed", "error", err)
	} else {
		if err := a.store.InsertContainerMetrics(ctx, ts, containerMetrics); err != nil {
			slog.Error("insert container metrics", "error", err)
		}
		// Sync log tailers with discovered containers.
		a.logs.Sync(ctx, containers)
	}

	// Evaluate alert rules against collected data.
	if a.alerter != nil {
		a.alerter.Evaluate(ctx, &MetricSnapshot{
			Host:       hostMetrics,
			Disks:      diskMetrics,
			Containers: containerMetrics,
		})
	}

	// Publish metrics update to hub.
	update := &protocol.MetricsUpdate{Timestamp: ts.Unix()}
	if hostMetrics != nil {
		update.Host = &protocol.HostMetrics{
			CPUPercent: hostMetrics.CPUPercent, MemTotal: hostMetrics.MemTotal,
			MemUsed: hostMetrics.MemUsed, MemPercent: hostMetrics.MemPercent,
			SwapTotal: hostMetrics.SwapTotal, SwapUsed: hostMetrics.SwapUsed,
			Load1: hostMetrics.Load1, Load5: hostMetrics.Load5, Load15: hostMetrics.Load15,
			Uptime: hostMetrics.Uptime,
		}
	}
	for _, d := range diskMetrics {
		update.Disks = append(update.Disks, protocol.DiskMetrics{
			Mountpoint: d.Mountpoint, Device: d.Device,
			Total: d.Total, Used: d.Used, Free: d.Free, Percent: d.Percent,
		})
	}
	for _, n := range netMetrics {
		update.Networks = append(update.Networks, protocol.NetMetrics{
			Iface: n.Iface, RxBytes: n.RxBytes, TxBytes: n.TxBytes,
			RxPackets: n.RxPackets, TxPackets: n.TxPackets,
			RxErrors: n.RxErrors, TxErrors: n.TxErrors,
		})
	}
	for _, c := range containerMetrics {
		update.Containers = append(update.Containers, protocol.ContainerMetrics{
			ID: c.ID, Name: c.Name, Image: c.Image, State: c.State,
			CPUPercent: c.CPUPercent, MemUsage: c.MemUsage, MemLimit: c.MemLimit, MemPercent: c.MemPercent,
			NetRx: c.NetRx, NetTx: c.NetTx, BlockRead: c.BlockRead, BlockWrite: c.BlockWrite, PIDs: c.PIDs,
		})
	}
	a.hub.Publish(TopicMetrics, update)

	// Prune if >1 hour since last prune.
	if time.Since(a.lastPrune) > 1*time.Hour {
		if err := a.store.Prune(ctx, a.cfg.Storage.RetentionDays); err != nil {
			slog.Error("prune failed", "error", err)
		} else {
			a.lastPrune = time.Now()
			slog.Info("pruned old data", "retention_days", a.cfg.Storage.RetentionDays)
		}
	}
}

// shutdown stops all components in the correct order:
// 1. Event watcher exits (context already cancelled)
// 2. Socket server closes
// 3. Log tailers flush remaining batches
// 4. Store closes
// 5. Docker client closes
func (a *Agent) shutdown() error {
	slog.Info("agent shutting down")

	a.events.Wait()
	a.socket.Stop()
	a.logs.Stop()

	if err := a.store.Close(); err != nil {
		slog.Error("close store", "error", err)
	}
	if err := a.docker.Close(); err != nil {
		slog.Error("close docker", "error", err)
	}

	slog.Info("agent stopped")
	return nil
}
