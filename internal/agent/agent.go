package agent

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Agent orchestrates metric collection, log tailing, and storage.
type Agent struct {
	cfg    *Config
	store  *Store
	host   *HostCollector
	docker *DockerCollector
	logs   *LogTailer

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

	return &Agent{
		cfg:    cfg,
		store:  store,
		host:   NewHostCollector(&cfg.Host),
		docker: docker,
		logs:   NewLogTailer(docker.Client(), store),
	}, nil
}

// Run starts the collection loop and blocks until the context is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	slog.Info("agent starting",
		"interval", a.cfg.Collect.Interval.Duration,
		"db", a.cfg.Storage.Path,
		"retention_days", a.cfg.Storage.RetentionDays,
	)

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
// 1. Log tailers flush remaining batches
// 2. Store closes
// 3. Docker client closes
func (a *Agent) shutdown() error {
	slog.Info("agent shutting down")

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
