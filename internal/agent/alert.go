package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Known fields per scope, used for validation.
var validFields = map[string]map[string]bool{
	"host": {
		"cpu_percent":    true,
		"memory_percent": true,
		"disk_percent":   true,
		"load1":          true,
		"load5":          true,
		"load15":         true,
		"swap_percent":   true,
	},
	"container": {
		"cpu_percent":    true,
		"memory_percent": true,
		"state":          true,
		"health":         true,
		"restart_count":  true,
		"exit_code":      true,
	},
}

// String-only fields that only support == and != operators.
var stringFields = map[string]bool{
	"state":  true,
	"health": true,
}

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

	fields, ok := validFields[c.Scope]
	if !ok || !fields[c.Field] {
		return Condition{}, fmt.Errorf("unknown field %q for scope %q", c.Field, c.Scope)
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

	// String fields only support == and !=.
	if stringFields[c.Field] && c.Op != "==" && c.Op != "!=" {
		return Condition{}, fmt.Errorf("field %q only supports == and != operators, got %q", c.Field, c.Op)
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
	name        string
	condition   Condition
	forDur      time.Duration
	severity    string
	actions     []string
	maxRestarts int
}

// Alerter evaluates alert rules against metric snapshots.
type Alerter struct {
	mu        sync.Mutex // protects instances and deferred; held during Evaluate and EvaluateContainerEvent
	rules     []alertRule
	instances map[string]*alertInstance
	deferred  []func()   // slow side effects collected under mu, executed after release
	store     *Store
	notifier  *Notifier
	docker    *DockerCollector
	now       func() time.Time
	restartFn func(ctx context.Context, containerID string) error // injectable for tests

	onStateChange func(a *Alert, state string) // called on "firing" / "resolved"

	silences   map[string]time.Time // rule name -> silenced until
	silencesMu sync.Mutex
}

// NewAlerter creates an Alerter from the config's alert rules.
func NewAlerter(alerts map[string]AlertConfig, store *Store, notifier *Notifier, docker *DockerCollector) (*Alerter, error) {
	a := &Alerter{
		instances: make(map[string]*alertInstance),
		deferred:  make([]func(), 0, 8),
		store:     store,
		notifier:  notifier,
		docker:    docker,
		now:       time.Now,
		silences:  make(map[string]time.Time),
	}

	// Sort rule names for deterministic evaluation order.
	names := make([]string, 0, len(alerts))
	for name := range alerts {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		ac := alerts[name]
		cond, err := parseCondition(ac.Condition)
		if err != nil {
			return nil, fmt.Errorf("alert %q: %w", name, err)
		}
		a.rules = append(a.rules, alertRule{
			name:        name,
			condition:   cond,
			forDur:      ac.For.Duration,
			severity:    ac.Severity,
			actions:     ac.Actions,
			maxRestarts: ac.MaxRestarts,
		})
	}
	return a, nil
}

// Evaluate checks all rules against the current snapshot and transitions state.
func (a *Alerter) Evaluate(ctx context.Context, snap *MetricSnapshot) {
	a.mu.Lock()
	a.deferred = a.deferred[:0]

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
	// Also clean up inactive instances to prevent unbounded map growth.
	for key, inst := range a.instances {
		if seen[key] {
			continue
		}
		switch inst.state {
		case stateFiring:
			a.resolve(ctx, a.ruleForKey(key), key, inst, now)
		case statePending:
			inst.state = stateInactive
		}
		if inst.state == stateInactive {
			delete(a.instances, key)
		}
	}

	pending := make([]func(), len(a.deferred))
	copy(pending, a.deferred)
	a.mu.Unlock()

	for _, fn := range pending {
		fn()
	}
}

// EvaluateContainerEvent evaluates container-scoped rules against a single
// container that just changed state. Unlike Evaluate(), this does NOT do stale
// cleanup — that remains the responsibility of the regular collect-loop Evaluate.
func (a *Alerter) EvaluateContainerEvent(ctx context.Context, cm ContainerMetrics) {
	a.mu.Lock()
	a.deferred = a.deferred[:0]

	now := a.now()
	for i := range a.rules {
		r := &a.rules[i]
		if r.condition.Scope != "container" {
			continue
		}
		key := r.name + ":" + cm.ID
		var matched bool
		if r.condition.IsStr {
			matched = compareStr(containerFieldStr(&cm, r.condition.Field), r.condition.Op, r.condition.StrVal)
		} else {
			matched = compareNum(containerFieldNum(&cm, r.condition.Field), r.condition.Op, r.condition.NumVal)
		}
		a.transition(ctx, r, key, matched, now, cm.ID, cm.Name)
	}

	pending := make([]func(), len(a.deferred))
	copy(pending, a.deferred)
	a.mu.Unlock()

	for _, fn := range pending {
		fn()
	}
}

func (a *Alerter) evalHostRule(ctx context.Context, r *alertRule, snap *MetricSnapshot, now time.Time, seen map[string]bool) {
	if snap.Host == nil {
		// Mark key as seen so stale cleanup doesn't falsely resolve
		// an active alert when collection transiently fails.
		seen[r.name] = true
		return
	}

	val := hostFieldValue(snap.Host, r.condition.Field)
	key := r.name
	seen[key] = true
	matched := compareNum(val, r.condition.Op, r.condition.NumVal)
	a.transition(ctx, r, key, matched, now, "", "")
}

func (a *Alerter) evalDiskRule(ctx context.Context, r *alertRule, snap *MetricSnapshot, now time.Time, seen map[string]bool) {
	if snap.Disks == nil {
		// Mark all existing instances for this rule as seen to avoid
		// false resolution on transient collection failure.
		for key := range a.instances {
			if strings.HasPrefix(key, r.name+":") {
				seen[key] = true
			}
		}
		return
	}

	for _, d := range snap.Disks {
		key := r.name + ":" + d.Mountpoint
		seen[key] = true
		matched := compareNum(d.Percent, r.condition.Op, r.condition.NumVal)
		a.transition(ctx, r, key, matched, now, "", d.Mountpoint)
	}
}

func (a *Alerter) evalContainerRule(ctx context.Context, r *alertRule, snap *MetricSnapshot, now time.Time, seen map[string]bool) {
	if snap.Containers == nil {
		// Mark all existing instances for this rule as seen to avoid
		// false resolution on transient collection failure.
		for key := range a.instances {
			if strings.HasPrefix(key, r.name+":") {
				seen[key] = true
			}
		}
		return
	}

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
			a.resolve(ctx, r, key, inst, now)
		}
	}
}

func (a *Alerter) fire(ctx context.Context, r *alertRule, key string, inst *alertInstance, now time.Time, containerID, label string) {
	inst.firedAt = now

	condStr := r.condition.Scope + "." + r.condition.Field + " " + r.condition.Op + " " + conditionValue(&r.condition)
	msg := fmt.Sprintf("[%s] %s: %s", r.severity, r.name, r.condition.Scope+"."+r.condition.Field)
	if label != "" {
		msg += " (" + label + ")"
	}
	slog.Warn("alert firing", "rule", r.name, "key", key)

	alert := &Alert{
		RuleName:    r.name,
		Severity:    r.severity,
		Condition:   condStr,
		InstanceKey: key,
		FiredAt:     now,
		Message:     msg,
	}
	id, err := a.store.InsertAlert(ctx, alert)
	if err != nil {
		slog.Error("insert alert", "error", err)
	}
	inst.dbID = id
	alert.ID = id

	if a.onStateChange != nil {
		a.onStateChange(alert, "firing")
	}

	// Defer slow side effects (notify, restart) to execute after mutex release.
	silenced := a.isSilenced(r.name)
	for _, action := range r.actions {
		switch action {
		case "notify":
			if !silenced {
				ruleName := r.name
				a.deferred = append(a.deferred, func() {
					a.notifier.Send(ctx, "Alert: "+ruleName, msg)
				})
			}
		case "restart":
			rule := r
			a.deferred = append(a.deferred, func() {
				a.doRestart(ctx, rule, inst, containerID)
			})
		}
	}
}

func (a *Alerter) resolve(ctx context.Context, r *alertRule, key string, inst *alertInstance, now time.Time) {
	slog.Info("alert resolved", "key", key)
	inst.state = stateInactive

	if inst.dbID > 0 {
		if err := a.store.ResolveAlert(ctx, inst.dbID, now); err != nil {
			slog.Error("resolve alert", "error", err)
		}
		if a.onStateChange != nil {
			condStr := ""
			ruleName := ""
			severity := ""
			if r != nil {
				ruleName = r.name
				severity = r.severity
				condStr = r.condition.Scope + "." + r.condition.Field + " " + r.condition.Op + " " + conditionValue(&r.condition)
			}
			a.onStateChange(&Alert{
				ID:          inst.dbID,
				RuleName:    ruleName,
				Severity:    severity,
				Condition:   condStr,
				InstanceKey: key,
				FiredAt:     inst.firedAt,
				ResolvedAt:  &now,
			}, "resolved")
		}
	}

	inst.restarts = 0
	inst.dbID = 0
}

// ruleForKey finds the alertRule for a given instance key (used for stale resolution).
func (a *Alerter) ruleForKey(key string) *alertRule {
	for i := range a.rules {
		if key == a.rules[i].name || strings.HasPrefix(key, a.rules[i].name+":") {
			return &a.rules[i]
		}
	}
	return nil
}

func (a *Alerter) doRestart(ctx context.Context, r *alertRule, inst *alertInstance, containerID string) {
	if containerID == "" {
		return
	}
	if a.restartFn == nil && a.docker == nil {
		return
	}
	if inst.restarts >= r.maxRestarts {
		slog.Warn("restart limit reached", "rule", r.name, "container", containerID, "restarts", inst.restarts)
		return
	}

	var err error
	if a.restartFn != nil {
		err = a.restartFn(ctx, containerID)
	} else {
		err = a.docker.RestartContainer(ctx, containerID)
	}
	if err != nil {
		slog.Error("restart container", "container", containerID, "error", err)
		return
	}
	inst.restarts++
	slog.Info("restarted container", "rule", r.name, "container", containerID, "restarts", inst.restarts)
}

// HasRule returns whether a rule with the given name exists.
func (a *Alerter) HasRule(name string) bool {
	for i := range a.rules {
		if a.rules[i].name == name {
			return true
		}
	}
	return false
}

// Silence suppresses notifications for a rule for the given duration.
func (a *Alerter) Silence(ruleName string, dur time.Duration) {
	a.silencesMu.Lock()
	defer a.silencesMu.Unlock()
	a.silences[ruleName] = a.now().Add(dur)
}

func (a *Alerter) isSilenced(ruleName string) bool {
	a.silencesMu.Lock()
	defer a.silencesMu.Unlock()
	until, ok := a.silences[ruleName]
	if !ok {
		return false
	}
	if a.now().After(until) {
		delete(a.silences, ruleName)
		return false
	}
	return true
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
	case "load1":
		return m.Load1
	case "load5":
		return m.Load5
	case "load15":
		return m.Load15
	case "swap_percent":
		if m.SwapTotal == 0 {
			return 0
		}
		return float64(m.SwapUsed) / float64(m.SwapTotal) * 100
	}
	return 0
}

func containerFieldNum(c *ContainerMetrics, field string) float64 {
	switch field {
	case "cpu_percent":
		return c.CPUPercent
	case "memory_percent":
		return c.MemPercent
	case "restart_count":
		return float64(c.RestartCount)
	case "exit_code":
		return float64(c.ExitCode)
	}
	return 0
}

func containerFieldStr(c *ContainerMetrics, field string) string {
	switch field {
	case "state":
		return c.State
	case "health":
		return c.Health
	}
	return ""
}
