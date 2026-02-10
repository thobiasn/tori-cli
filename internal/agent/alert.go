package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// Condition represents a parsed alert condition like "host.cpu_percent > 90".
type Condition struct {
	Scope  string  // "host" or "container"
	Field  string  // "cpu_percent", "memory_percent", "disk_percent", "state"
	Op     string  // ">", "<", ">=", "<=", "==", "!="
	NumVal float64 // numeric threshold (when IsStr is false)
	StrVal string  // string value (when IsStr is true)
	IsStr  bool
}

func parseCondition(s string) (Condition, error) {
	tokens := strings.Fields(s)
	if len(tokens) != 3 {
		return Condition{}, fmt.Errorf("condition must be 3 tokens (got %d): %q", len(tokens), s)
	}

	parts := strings.SplitN(tokens[0], ".", 2)
	if len(parts) != 2 {
		return Condition{}, fmt.Errorf("condition target must be scope.field: %q", tokens[0])
	}

	c := Condition{
		Scope: parts[0],
		Field: parts[1],
		Op:    tokens[1],
	}

	switch c.Scope {
	case "host", "container":
	default:
		return Condition{}, fmt.Errorf("unknown scope %q (must be host or container)", c.Scope)
	}

	switch c.Op {
	case ">", "<", ">=", "<=", "==", "!=":
	default:
		return Condition{}, fmt.Errorf("unknown operator %q", c.Op)
	}

	val := tokens[2]
	if strings.HasPrefix(val, "'") && strings.HasSuffix(val, "'") && len(val) >= 2 {
		c.IsStr = true
		c.StrVal = val[1 : len(val)-1]
	} else {
		v, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return Condition{}, fmt.Errorf("invalid numeric value %q: %w", val, err)
		}
		c.NumVal = v
	}

	return c, nil
}

func compareNum(actual float64, op string, threshold float64) bool {
	switch op {
	case ">":
		return actual > threshold
	case "<":
		return actual < threshold
	case ">=":
		return actual >= threshold
	case "<=":
		return actual <= threshold
	case "==":
		return actual == threshold
	case "!=":
		return actual != threshold
	}
	return false
}

func compareStr(actual, op, expected string) bool {
	switch op {
	case "==":
		return actual == expected
	case "!=":
		return actual != expected
	}
	return false
}

// MetricSnapshot holds the data collected in one cycle, passed to the alerter.
type MetricSnapshot struct {
	Host       *HostMetrics
	Disks      []DiskMetrics
	Containers []ContainerMetrics
}

type alertState int

const (
	stateInactive alertState = iota
	statePending
	stateFiring
)

type alertInstance struct {
	state        alertState
	pendingSince time.Time
	firedAt      time.Time
	dbID         int64
	restarts     int
}

type alertRule struct {
	name      string
	condition Condition
	forDur    time.Duration
	severity  string
	actions   []string
	maxRstrt  int
}

// Alerter evaluates alert rules against metric snapshots.
type Alerter struct {
	rules     []alertRule
	instances map[string]*alertInstance
	store     *Store
	notifier  *Notifier
	docker    *DockerCollector
	now       func() time.Time
}

// NewAlerter creates an Alerter from the config's alert rules.
func NewAlerter(alerts map[string]AlertConfig, store *Store, notifier *Notifier, docker *DockerCollector) (*Alerter, error) {
	a := &Alerter{
		instances: make(map[string]*alertInstance),
		store:     store,
		notifier:  notifier,
		docker:    docker,
		now:       time.Now,
	}
	for name, ac := range alerts {
		cond, err := parseCondition(ac.Condition)
		if err != nil {
			return nil, fmt.Errorf("alert %q: %w", name, err)
		}
		a.rules = append(a.rules, alertRule{
			name:      name,
			condition: cond,
			forDur:    ac.For.Duration,
			severity:  ac.Severity,
			actions:   ac.Actions,
			maxRstrt:  ac.MaxRestarts,
		})
	}
	return a, nil
}

// Evaluate checks all rules against the current snapshot and transitions state.
func (a *Alerter) Evaluate(ctx context.Context, snap *MetricSnapshot) {
	now := a.now()
	seen := make(map[string]bool)

	for i := range a.rules {
		r := &a.rules[i]
		switch {
		case r.condition.Scope == "host" && r.condition.Field == "disk_percent":
			a.evalDiskRule(ctx, r, snap, now, seen)
		case r.condition.Scope == "host":
			a.evalHostRule(ctx, r, snap, now, seen)
		case r.condition.Scope == "container":
			a.evalContainerRule(ctx, r, snap, now, seen)
		}
	}

	// Resolve stale instances (disappeared containers/disks).
	for key, inst := range a.instances {
		if !seen[key] && inst.state == stateFiring {
			a.resolve(ctx, key, inst, now)
		}
	}
}

func (a *Alerter) evalHostRule(ctx context.Context, r *alertRule, snap *MetricSnapshot, now time.Time, seen map[string]bool) {
	if snap.Host == nil {
		return
	}

	val := hostFieldValue(snap.Host, r.condition.Field)
	key := r.name
	seen[key] = true
	matched := compareNum(val, r.condition.Op, r.condition.NumVal)
	a.transition(ctx, r, key, matched, now, "", "")
}

func (a *Alerter) evalDiskRule(ctx context.Context, r *alertRule, snap *MetricSnapshot, now time.Time, seen map[string]bool) {
	for _, d := range snap.Disks {
		key := r.name + ":" + d.Mountpoint
		seen[key] = true
		matched := compareNum(d.Percent, r.condition.Op, r.condition.NumVal)
		a.transition(ctx, r, key, matched, now, "", d.Mountpoint)
	}
}

func (a *Alerter) evalContainerRule(ctx context.Context, r *alertRule, snap *MetricSnapshot, now time.Time, seen map[string]bool) {
	for _, c := range snap.Containers {
		key := r.name + ":" + c.ID
		seen[key] = true

		var matched bool
		if r.condition.IsStr {
			matched = compareStr(containerFieldStr(&c, r.condition.Field), r.condition.Op, r.condition.StrVal)
		} else {
			matched = compareNum(containerFieldNum(&c, r.condition.Field), r.condition.Op, r.condition.NumVal)
		}
		a.transition(ctx, r, key, matched, now, c.ID, c.Name)
	}
}

func (a *Alerter) transition(ctx context.Context, r *alertRule, key string, matched bool, now time.Time, containerID, label string) {
	inst := a.instances[key]
	if inst == nil {
		inst = &alertInstance{}
		a.instances[key] = inst
	}

	switch inst.state {
	case stateInactive:
		if matched {
			if r.forDur == 0 {
				inst.state = stateFiring
				a.fire(ctx, r, key, inst, now, containerID, label)
			} else {
				inst.state = statePending
				inst.pendingSince = now
			}
		}
	case statePending:
		if !matched {
			inst.state = stateInactive
		} else if now.Sub(inst.pendingSince) >= r.forDur {
			inst.state = stateFiring
			a.fire(ctx, r, key, inst, now, containerID, label)
		}
	case stateFiring:
		if !matched {
			a.resolve(ctx, key, inst, now)
		}
	}
}

func (a *Alerter) fire(ctx context.Context, r *alertRule, key string, inst *alertInstance, now time.Time, containerID, label string) {
	inst.firedAt = now

	msg := fmt.Sprintf("[%s] %s: %s", r.severity, r.name, r.condition.Scope+"."+r.condition.Field)
	if label != "" {
		msg += " (" + label + ")"
	}
	slog.Warn("alert firing", "rule", r.name, "key", key)

	id, err := a.store.InsertAlert(ctx, &Alert{
		RuleName:    r.name,
		Severity:    r.severity,
		Condition:   r.condition.Scope + "." + r.condition.Field + " " + r.condition.Op + " " + conditionValue(&r.condition),
		InstanceKey: key,
		FiredAt:     now,
		Message:     msg,
	})
	if err != nil {
		slog.Error("insert alert", "error", err)
	}
	inst.dbID = id

	for _, action := range r.actions {
		switch action {
		case "notify":
			a.notifier.Send(ctx, "Alert: "+r.name, msg)
		case "restart":
			a.doRestart(ctx, r, inst, containerID)
		}
	}
}

func (a *Alerter) resolve(ctx context.Context, key string, inst *alertInstance, now time.Time) {
	slog.Info("alert resolved", "key", key)
	inst.state = stateInactive

	if inst.dbID > 0 {
		if err := a.store.ResolveAlert(ctx, inst.dbID, now); err != nil {
			slog.Error("resolve alert", "error", err)
		}
	}

	// Reset restarts counter on resolution.
	inst.restarts = 0
	inst.dbID = 0
}

func (a *Alerter) doRestart(ctx context.Context, r *alertRule, inst *alertInstance, containerID string) {
	if containerID == "" || a.docker == nil {
		return
	}
	if inst.restarts >= r.maxRstrt {
		slog.Warn("restart limit reached", "rule", r.name, "container", containerID, "restarts", inst.restarts)
		return
	}
	if err := a.docker.RestartContainer(ctx, containerID); err != nil {
		slog.Error("restart container", "container", containerID, "error", err)
		return
	}
	inst.restarts++
	slog.Info("restarted container", "rule", r.name, "container", containerID, "restarts", inst.restarts)
}

func conditionValue(c *Condition) string {
	if c.IsStr {
		return "'" + c.StrVal + "'"
	}
	return strconv.FormatFloat(c.NumVal, 'f', -1, 64)
}

func hostFieldValue(m *HostMetrics, field string) float64 {
	switch field {
	case "cpu_percent":
		return m.CPUPercent
	case "memory_percent":
		return m.MemPercent
	}
	return 0
}

func containerFieldNum(c *ContainerMetrics, field string) float64 {
	switch field {
	case "cpu_percent":
		return c.CPUPercent
	case "memory_percent":
		return c.MemPercent
	}
	return 0
}

func containerFieldStr(c *ContainerMetrics, field string) string {
	switch field {
	case "state":
		return c.State
	}
	return ""
}
