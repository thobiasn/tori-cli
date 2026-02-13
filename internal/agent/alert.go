package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

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
}

type alertRule struct {
	name      string
	condition Condition
	forDur    time.Duration
	severity  string
	actions   []string
}

// evalContext bundles the per-evaluation arguments shared by transition and fire.
type evalContext struct {
	rule        *alertRule
	key         string
	containerID string
	label       string
}

// Alerter evaluates alert rules against metric snapshots.
type Alerter struct {
	mu        sync.Mutex // protects instances and deferred; held during Evaluate and EvaluateContainerEvent
	rules     []alertRule
	instances map[string]*alertInstance
	deferred  []func()   // slow side effects collected under mu, executed after release
	store     *Store
	notifier  *Notifier
	now       func() time.Time

	onStateChange func(a *Alert, state string) // called on "firing" / "resolved"

	silences   map[string]time.Time // rule name -> silenced until
	silencesMu sync.Mutex
}

// NewAlerter creates an Alerter from the config's alert rules.
func NewAlerter(alerts map[string]AlertConfig, store *Store, notifier *Notifier) (*Alerter, error) {
	a := &Alerter{
		instances: make(map[string]*alertInstance),
		deferred:  make([]func(), 0, 8),
		store:     store,
		notifier:  notifier,
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
			name:      name,
			condition: cond,
			forDur:    ac.For.Duration,
			severity:  ac.Severity,
			actions:   ac.Actions,
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

	a.runDeferred()
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
		// Skip numeric rules — events don't carry metric data (CPU/mem = 0),
		// which would cause false resolution of numeric alerts.
		if !r.condition.IsStr {
			continue
		}
		ec := &evalContext{
			rule:        r,
			key:         r.name + ":" + cm.ID,
			containerID: cm.ID,
			label:       cm.Name,
		}
		matched := compareStr(containerFieldStr(&cm, r.condition.Field), r.condition.Op, r.condition.StrVal)
		a.transition(ctx, ec, matched, now)
	}

	a.runDeferred()
}

// runDeferred copies deferred side effects, releases a.mu, then executes them.
// Caller must hold a.mu.
func (a *Alerter) runDeferred() {
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
	a.transition(ctx, &evalContext{rule: r, key: key}, matched, now)
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
		a.transition(ctx, &evalContext{rule: r, key: key, label: d.Mountpoint}, matched, now)
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
		a.transition(ctx, &evalContext{rule: r, key: key, containerID: c.ID, label: c.Name}, matched, now)
	}
}

func (a *Alerter) transition(ctx context.Context, ec *evalContext, matched bool, now time.Time) {
	inst := a.instances[ec.key]
	if inst == nil {
		inst = &alertInstance{}
		a.instances[ec.key] = inst
	}

	switch inst.state {
	case stateInactive:
		if matched {
			if ec.rule.forDur == 0 {
				inst.state = stateFiring
				a.fire(ctx, ec, inst, now)
			} else {
				inst.state = statePending
				inst.pendingSince = now
			}
		}
	case statePending:
		if !matched {
			inst.state = stateInactive
		} else if now.Sub(inst.pendingSince) >= ec.rule.forDur {
			inst.state = stateFiring
			a.fire(ctx, ec, inst, now)
		}
	case stateFiring:
		if !matched {
			a.resolve(ctx, ec.rule, ec.key, inst, now)
		}
	}
}

func (a *Alerter) fire(ctx context.Context, ec *evalContext, inst *alertInstance, now time.Time) {
	inst.firedAt = now
	r := ec.rule

	condStr := r.condition.Scope + "." + r.condition.Field + " " + r.condition.Op + " " + conditionValue(&r.condition)
	msg := fmt.Sprintf("[%s] %s: %s", r.severity, r.name, r.condition.Scope+"."+r.condition.Field)
	if ec.label != "" {
		msg += " (" + ec.label + ")"
	}
	slog.Warn("alert firing", "rule", r.name, "key", ec.key)

	alert := &Alert{
		RuleName:    r.name,
		Severity:    r.severity,
		Condition:   condStr,
		InstanceKey: ec.key,
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

	// Defer slow side effects (notify) to execute after mutex release.
	silenced := a.isSilenced(r.name)
	for _, action := range r.actions {
		if action == "notify" && !silenced {
			ruleName := r.name
			a.deferred = append(a.deferred, func() {
				a.notifier.Send(ctx, "Alert: "+ruleName, msg)
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

// AdoptFiring loads unresolved alerts from the store and adopts those whose
// instance_key matches a current rule into the instances map. Alerts that no
// longer match any rule are resolved. This lets alerts survive agent restarts
// without the resolve/re-fire noise.
func (a *Alerter) AdoptFiring(ctx context.Context) error {
	firing, err := a.store.QueryFiringAlerts(ctx)
	if err != nil {
		return fmt.Errorf("query firing alerts: %w", err)
	}

	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()

	for _, alert := range firing {
		r := a.ruleForKey(alert.InstanceKey)
		if r == nil {
			// Rule was removed — resolve the orphan.
			if err := a.store.ResolveAlert(ctx, alert.ID, now); err != nil {
				slog.Error("resolve orphaned alert", "id", alert.ID, "error", err)
			}
			continue
		}
		a.instances[alert.InstanceKey] = &alertInstance{
			state:   stateFiring,
			firedAt: alert.FiredAt,
			dbID:    alert.ID,
		}
		slog.Info("adopted firing alert", "rule", r.name, "key", alert.InstanceKey, "id", alert.ID)
	}
	return nil
}

// ResolveAll resolves all firing alerts. Called before replacing the alerter on config reload.
func (a *Alerter) ResolveAll() {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	for key, inst := range a.instances {
		if inst.state == stateFiring {
			a.resolve(context.Background(), a.ruleForKey(key), key, inst, now)
		}
	}
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
