package agent

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/thobiasn/tori-cli/internal/protocol"
)

// Agent orchestrates metric collection, log tailing, and storage.
type Agent struct {
	cfg     *Config
	cfgPath string
	version string
	store   *Store
	host    *HostCollector
	docker  *DockerCollector
	logs    *LogTailer
	alerter *Alerter
	events  *EventWatcher
	hub     *Hub
	socket  *SocketServer

	reload    chan *Config
	lastPrune time.Time
}

// New creates an Agent from the given config. cfgPath is stored for reload.
func New(cfg *Config, cfgPath string, version string) (*Agent, error) {
	store, err := OpenStore(cfg.Storage.Path)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}

	docker, err := NewDockerCollector(&cfg.Docker)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("docker collector: %w", err)
	}

	// Load persisted tracking state. Non-fatal if it fails.
	tracked, err := store.LoadTracking(context.Background())
	if err != nil {
		slog.Warn("failed to load tracking state", "error", err)
	} else if len(tracked) > 0 {
		docker.LoadTrackingState(tracked)
		slog.Info("loaded tracking state", "containers", len(tracked))
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
			Level:         e.Level,
			DisplayMsg:    e.DisplayMsg,
		})
	}

	a := &Agent{
		cfg:     cfg,
		cfgPath: cfgPath,
		version: version,
		store:   store,
		host:    NewHostCollector(&cfg.Host),
		docker:  docker,
		logs:    lt,
		hub:     hub,
		reload:  make(chan *Config, 1),
	}

	if len(cfg.Alerts) > 0 {
		notifier := NewNotifier(&cfg.Notify)
		alerter, err := NewAlerter(cfg.Alerts, store, notifier)
		if err != nil {
			store.Close()
			docker.Close()
			return nil, fmt.Errorf("alerter: %w", err)
		}
		alerter.onStateChange = a.makeOnStateChange()
		a.alerter = alerter

		// Adopt firing alerts from a previous run into the alerter's state.
		if err := alerter.AdoptFiring(context.Background()); err != nil {
			slog.Warn("failed to adopt firing alerts", "error", err)
		}
	} else {
		// No alerter — bulk-resolve any leftover unresolved alerts.
		if err := store.ResolveOrphanedAlerts(context.Background(), time.Now()); err != nil {
			slog.Warn("failed to resolve orphaned alerts", "error", err)
		}
	}

	a.events = NewEventWatcher(docker, hub)
	a.events.SetAlerter(a.alerter)
	a.socket = NewSocketServer(hub, store, docker, a.alerter, cfg.Storage.RetentionDays, version)
	return a, nil
}

// Reload re-reads the config file and sends it to the Run loop for application.
// Safe to call from any goroutine (e.g. SIGHUP handler). If a reload is already
// pending, the new one is dropped.
func (a *Agent) Reload() error {
	cfg, err := LoadConfig(a.cfgPath)
	if err != nil {
		return fmt.Errorf("reload config: %w", err)
	}
	select {
	case a.reload <- cfg:
		slog.Info("config reload queued")
	default:
		slog.Warn("config reload already pending, skipping")
	}
	return nil
}

// Run starts the collection loop and blocks until the context is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	slog.Info("agent starting",
		"interval", a.cfg.Collect.Interval.Duration,
		"db", a.cfg.Storage.Path,
		"retention_days", a.cfg.Storage.RetentionDays,
	)

	if err := a.socket.Start(a.cfg.Socket.Path, a.cfg.Socket.Mode.FileMode); err != nil {
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
		case newCfg := <-a.reload:
			a.applyConfig(ctx, newCfg)
			ticker.Reset(a.cfg.Collect.Interval.Duration)
		}
	}
}

// nonReloadableFields logs warnings if non-reloadable config fields have changed.
func nonReloadableFields(old, updated *Config) {
	if old.Storage.Path != updated.Storage.Path {
		slog.Warn("config reload: storage.path cannot be changed at runtime", "old", old.Storage.Path, "new", updated.Storage.Path)
	}
	if old.Socket.Path != updated.Socket.Path {
		slog.Warn("config reload: socket.path cannot be changed at runtime", "old", old.Socket.Path, "new", updated.Socket.Path)
	}
	if old.Socket.Mode.FileMode != updated.Socket.Mode.FileMode {
		slog.Warn("config reload: socket.mode cannot be changed at runtime", "old", fmt.Sprintf("%#o", old.Socket.Mode.FileMode), "new", fmt.Sprintf("%#o", updated.Socket.Mode.FileMode))
	}
	if old.Host.Proc != updated.Host.Proc {
		slog.Warn("config reload: host.proc cannot be changed at runtime", "old", old.Host.Proc, "new", updated.Host.Proc)
	}
	if old.Host.Sys != updated.Host.Sys {
		slog.Warn("config reload: host.sys cannot be changed at runtime", "old", old.Host.Sys, "new", updated.Host.Sys)
	}
	if old.Docker.Socket != updated.Docker.Socket {
		slog.Warn("config reload: docker.socket cannot be changed at runtime", "old", old.Docker.Socket, "new", updated.Docker.Socket)
	}
}

func (a *Agent) applyConfig(ctx context.Context, newCfg *Config) {
	nonReloadableFields(a.cfg, newCfg)

	// Reloadable fields.
	a.cfg.Storage.RetentionDays = newCfg.Storage.RetentionDays
	a.cfg.Collect.Interval = newCfg.Collect.Interval
	a.cfg.Docker.Include = newCfg.Docker.Include
	a.cfg.Docker.Exclude = newCfg.Docker.Exclude

	// Docker filters.
	a.docker.SetFilters(newCfg.Docker.Include, newCfg.Docker.Exclude)
	a.socket.SetRetentionDays(newCfg.Storage.RetentionDays)

	// Rebuild alerter + notifier if alert/notify config changed.
	if len(newCfg.Alerts) > 0 {
		notifier := NewNotifier(&newCfg.Notify)
		alerter, err := NewAlerter(newCfg.Alerts, a.store, notifier)
		if err != nil {
			slog.Error("config reload: failed to create alerter, keeping old", "error", err)
			return
		}
		alerter.onStateChange = a.makeOnStateChange()
		if a.alerter != nil {
			a.alerter.ResolveAll(ctx)
			a.alerter.Stop()
		}
		a.alerter = alerter
		a.socket.SetAlerter(alerter)
		a.events.SetAlerter(alerter)
	} else {
		if a.alerter != nil {
			a.alerter.ResolveAll(ctx)
			a.alerter.Stop()
		}
		a.alerter = nil
		a.socket.SetAlerter(nil)
		a.events.SetAlerter(nil)
	}

	a.cfg.Alerts = newCfg.Alerts
	a.cfg.Notify = newCfg.Notify

	slog.Info("config reloaded",
		"interval", a.cfg.Collect.Interval.Duration,
		"alert_rules", len(a.cfg.Alerts),
		"retention_days", a.cfg.Storage.RetentionDays,
	)
}

// makeOnStateChange returns the onStateChange callback for the alerter.
func (a *Agent) makeOnStateChange() func(alert *Alert, state string) {
	return func(alert *Alert, state string) {
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
		a.hub.Publish(TopicAlerts, event)
	}
}

func (a *Agent) collect(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}

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
	if ctx.Err() != nil {
		return
	}
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
			CPUPercent: hostMetrics.CPUPercent, CPUs: hostMetrics.CPUs, MemTotal: hostMetrics.MemTotal,
			MemUsed: hostMetrics.MemUsed, MemPercent: hostMetrics.MemPercent,
			MemCached: hostMetrics.MemCached, MemFree: hostMetrics.MemFree,
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
			Project: c.Project, Service: c.Service,
			Health: c.Health, StartedAt: c.StartedAt, RestartCount: c.RestartCount, ExitCode: c.ExitCode,
			CPUPercent: c.CPUPercent, CPULimit: c.CPULimit,
			MemUsage: c.MemUsage, MemLimit: c.MemLimit, MemPercent: c.MemPercent,
			NetRx: c.NetRx, NetTx: c.NetTx, BlockRead: c.BlockRead, BlockWrite: c.BlockWrite, PIDs: c.PIDs,
			DiskUsage: c.DiskUsage,
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
	if a.alerter != nil {
		a.alerter.Stop()
	}

	if err := a.store.Close(); err != nil {
		slog.Error("close store", "error", err)
	}
	if err := a.docker.Close(); err != nil {
		slog.Error("close docker", "error", err)
	}

	slog.Info("agent stopped")
	return nil
}
