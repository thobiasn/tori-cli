package tui

import (
	"context"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/thobiasn/tori-cli/internal/protocol"
)

// alertsSection selects which section has focus.
type alertsSection int

const (
	sectionAlerts alertsSection = iota
	sectionRules
)

// AlertsState holds the state for the alerts view.
type AlertsState struct {
	rules        []protocol.AlertRuleInfo
	resolved     []protocol.AlertMsg
	focus        alertsSection
	alertCursor  int
	ruleCursor   int
	showResolved bool
	alertDialog  bool // true when alert detail dialog is open
	ruleDialog   bool // true when rule detail dialog is open
	silenceModal *silenceModalState
	loaded       bool
}

type silenceModalState struct {
	ruleName string
	cursor   int // 0=15m, 1=1h, 2=6h, 3=24h
}

var silenceDurations = []struct {
	label   string
	seconds int64
}{
	{"15m", 15 * 60},
	{"1h", 3600},
	{"6h", 6 * 3600},
	{"24h", 24 * 3600},
}

// alertItem is a unified alert for rendering both firing and resolved.
type alertItem struct {
	id          int64
	firedAt     int64
	resolvedAt  int64
	ruleName    string
	severity    string
	condition   string
	instanceKey string
	message     string
	acked       bool
	resolved    bool
}

// Message types.
type alertsDataMsg struct {
	server   string
	rules    []protocol.AlertRuleInfo
	resolved []protocol.AlertMsg
}
type alertAckDoneMsg struct{ server string }
type alertSilenceDoneMsg struct{ server string }

// queryAlertsData fetches alert rules and recent historical alerts.
func queryAlertsData(c *Client, server string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		var rules []protocol.AlertRuleInfo
		var resolved []protocol.AlertMsg

		if r, err := c.QueryAlertRules(ctx); err == nil {
			rules = r
		}

		now := time.Now().Unix()
		start := now - 7*86400
		if alerts, err := c.QueryAlerts(ctx, start, now); err == nil {
			for _, a := range alerts {
				if a.ResolvedAt > 0 {
					resolved = append(resolved, a)
				}
			}
		}

		return alertsDataMsg{server: server, rules: rules, resolved: resolved}
	}
}

// buildAlertList merges firing alerts and historical resolved alerts into
// a unified list. Firing sorted newest-first, then resolved newest-first.
// Historical alerts that are still firing (present in the live map) are
// excluded to avoid duplicates.
func buildAlertList(firing map[int64]*protocol.AlertEvent, resolved []protocol.AlertMsg, showResolved bool) []alertItem {
	var items []alertItem

	// Firing alerts from the live stream.
	for _, e := range firing {
		items = append(items, alertItem{
			id:          e.ID,
			firedAt:     e.FiredAt,
			ruleName:    e.RuleName,
			severity:    e.Severity,
			condition:   e.Condition,
			instanceKey: e.InstanceKey,
			message:     e.Message,
			acked:       e.Acknowledged,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].firedAt != items[j].firedAt {
			return items[i].firedAt > items[j].firedAt
		}
		return items[i].id < items[j].id
	})

	if !showResolved {
		return items
	}

	// Resolved alerts from historical query, excluding duplicates.
	var resolvedItems []alertItem
	for _, a := range resolved {
		if _, dup := firing[a.ID]; dup {
			continue
		}
		resolvedItems = append(resolvedItems, alertItem{
			id:          a.ID,
			firedAt:     a.FiredAt,
			resolvedAt:  a.ResolvedAt,
			ruleName:    a.RuleName,
			severity:    a.Severity,
			condition:   a.Condition,
			instanceKey: a.InstanceKey,
			message:     a.Message,
			acked:       a.Acknowledged,
			resolved:    true,
		})
	}
	sort.Slice(resolvedItems, func(i, j int) bool {
		if resolvedItems[i].resolvedAt != resolvedItems[j].resolvedAt {
			return resolvedItems[i].resolvedAt > resolvedItems[j].resolvedAt
		}
		return resolvedItems[i].id < resolvedItems[j].id
	})

	items = append(items, resolvedItems...)
	return items
}

// enterAlerts switches to the alerts view and loads data.
func (a *App) enterAlerts() tea.Cmd {
	s := a.session()
	if s == nil || s.Client == nil {
		return nil
	}
	a.view = viewAlerts
	av := &s.AlertsView
	av.alertCursor = 0
	av.ruleCursor = 0
	av.alertDialog = false
	av.ruleDialog = false
	av.silenceModal = nil
	av.focus = sectionAlerts
	return queryAlertsData(s.Client, s.Name)
}

// leaveAlerts returns to the dashboard.
func (a *App) leaveAlerts() {
	a.view = viewDashboard
}

// handleAlertsKey handles keys when the alerts view is active.
func (a *App) handleAlertsKey(msg tea.KeyMsg) (App, tea.Cmd) {
	s := a.session()
	if s == nil {
		return *a, nil
	}
	av := &s.AlertsView
	key := msg.String()

	// Silence dialog captures keys first.
	if av.silenceModal != nil {
		return a.handleSilenceDialogKey(key)
	}

	// Alert/rule dialog captures keys.
	if av.alertDialog {
		return a.handleAlertDialogKey(key)
	}
	if av.ruleDialog {
		return a.handleRuleDialogKey(key)
	}

	// Zoom (shared with dashboard/detail).
	if key == "+" || key == "=" || key == "-" {
		if cmd := a.handleZoom(key); cmd != nil {
			return *a, cmd
		}
		return *a, nil
	}

	switch key {
	case "esc":
		a.leaveAlerts()
		return *a, nil

	case "1":
		a.leaveAlerts()
		return *a, nil

	case "2":
		return *a, nil // already here

	case "tab":
		if av.focus == sectionAlerts {
			av.focus = sectionRules
		} else {
			av.focus = sectionAlerts
		}
		return *a, nil

	case "j", "down":
		a.alertsNavigate(1)
		return *a, nil

	case "k", "up":
		a.alertsNavigate(-1)
		return *a, nil

	case "r":
		av.showResolved = !av.showResolved
		if !av.showResolved {
			av.alertDialog = false
		}
		a.clampAlertsCursor()
		return *a, nil

	case "enter":
		if av.focus == sectionAlerts {
			items := buildAlertList(s.Alerts, av.resolved, av.showResolved)
			if av.alertCursor >= 0 && av.alertCursor < len(items) {
				av.alertDialog = true
			}
		} else {
			if av.ruleCursor >= 0 && av.ruleCursor < len(av.rules) {
				av.ruleDialog = true
			}
		}
		return *a, nil

	case "a":
		if av.focus != sectionAlerts {
			return *a, nil
		}
		return a.ackCurrentAlert()

	case "s":
		return a.handleAlertsSilence()

	case "g":
		return a.goToAlertContainer()
	}

	return *a, nil
}

// handleAlertDialogKey handles keys within the alert detail dialog.
func (a *App) handleAlertDialogKey(key string) (App, tea.Cmd) {
	s := a.session()
	if s == nil {
		return *a, nil
	}
	av := &s.AlertsView

	switch key {
	case "esc", "enter":
		av.alertDialog = false
	case "j", "down":
		a.alertsNavigate(1)
	case "k", "up":
		a.alertsNavigate(-1)
	case "a":
		return a.ackCurrentAlert()
	case "s":
		return a.handleAlertsSilence()
	case "g":
		return a.goToAlertContainer()
	}
	return *a, nil
}

// handleRuleDialogKey handles keys within the rule detail dialog.
func (a *App) handleRuleDialogKey(key string) (App, tea.Cmd) {
	s := a.session()
	if s == nil {
		return *a, nil
	}
	av := &s.AlertsView

	switch key {
	case "esc", "enter":
		av.ruleDialog = false
	case "j", "down":
		a.alertsNavigate(1)
	case "k", "up":
		a.alertsNavigate(-1)
	case "s":
		return a.handleAlertsSilence()
	}
	return *a, nil
}

// ackCurrentAlert sends an ack for the alert at the current cursor.
func (a *App) ackCurrentAlert() (App, tea.Cmd) {
	s := a.session()
	if s == nil || s.Client == nil {
		return *a, nil
	}
	av := &s.AlertsView
	items := buildAlertList(s.Alerts, av.resolved, av.showResolved)
	if av.alertCursor < 0 || av.alertCursor >= len(items) {
		return *a, nil
	}
	item := items[av.alertCursor]
	if item.resolved || item.acked {
		return *a, nil
	}
	// Optimistically mark as acked so the UI updates immediately.
	if e, ok := s.Alerts[item.id]; ok {
		e.Acknowledged = true
	}
	client := s.Client
	server := s.Name
	alertID := item.id
	return *a, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		client.AckAlert(ctx, alertID)
		return alertAckDoneMsg{server: server}
	}
}

// goToAlertContainer navigates to the detail view for the alert's container.
func (a *App) goToAlertContainer() (App, tea.Cmd) {
	s := a.session()
	if s == nil {
		return *a, nil
	}
	av := &s.AlertsView
	if av.focus != sectionAlerts {
		return *a, nil
	}
	items := buildAlertList(s.Alerts, av.resolved, av.showResolved)
	if av.alertCursor < 0 || av.alertCursor >= len(items) {
		return *a, nil
	}
	item := items[av.alertCursor]
	containerID := instanceKeyContainerID(item.instanceKey)
	if containerID == "" {
		return *a, nil
	}
	return a.enterDetailByContainerID(containerID)
}

// handleAlertsSilence handles the "s" key: toggle silence or open dialog.
func (a *App) handleAlertsSilence() (App, tea.Cmd) {
	s := a.session()
	if s == nil || s.Client == nil {
		return *a, nil
	}
	av := &s.AlertsView

	var ruleName string
	var silencedUntil int64

	if av.focus == sectionRules {
		if av.ruleCursor < 0 || av.ruleCursor >= len(av.rules) {
			return *a, nil
		}
		rule := av.rules[av.ruleCursor]
		ruleName = rule.Name
		silencedUntil = rule.SilencedUntil
	} else {
		items := buildAlertList(s.Alerts, av.resolved, av.showResolved)
		if av.alertCursor < 0 || av.alertCursor >= len(items) {
			return *a, nil
		}
		ruleName = items[av.alertCursor].ruleName
		// Check if this rule is already silenced.
		for _, r := range av.rules {
			if r.Name == ruleName {
				silencedUntil = r.SilencedUntil
				break
			}
		}
	}

	// If already silenced, unsilence immediately (duration=0).
	if silencedUntil > 0 && time.Unix(silencedUntil, 0).After(time.Now()) {
		client := s.Client
		server := s.Name
		name := ruleName
		return *a, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client.SilenceAlert(ctx, name, 0)
			return alertSilenceDoneMsg{server: server}
		}
	}

	// Open silence dialog.
	av.silenceModal = &silenceModalState{
		ruleName: ruleName,
		cursor:   0,
	}
	return *a, nil
}

// handleSilenceDialogKey handles keys within the silence duration dialog.
func (a *App) handleSilenceDialogKey(key string) (App, tea.Cmd) {
	s := a.session()
	if s == nil {
		return *a, nil
	}
	av := &s.AlertsView
	m := av.silenceModal

	switch key {
	case "h", "left":
		if m.cursor > 0 {
			m.cursor--
		}
	case "l", "right":
		if m.cursor < len(silenceDurations)-1 {
			m.cursor++
		}
	case "enter":
		dur := silenceDurations[m.cursor].seconds
		client := s.Client
		server := s.Name
		name := m.ruleName
		av.silenceModal = nil
		return *a, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client.SilenceAlert(ctx, name, dur)
			return alertSilenceDoneMsg{server: server}
		}
	case "esc":
		av.silenceModal = nil
	}
	return *a, nil
}

// alertsNavigate moves the cursor in the focused section.
func (a *App) alertsNavigate(delta int) {
	s := a.session()
	if s == nil {
		return
	}
	av := &s.AlertsView

	if av.focus == sectionAlerts {
		items := buildAlertList(s.Alerts, av.resolved, av.showResolved)
		max := len(items) - 1
		if max < 0 {
			max = 0
		}
		av.alertCursor += delta
		if av.alertCursor < 0 {
			av.alertCursor = 0
		}
		if av.alertCursor > max {
			av.alertCursor = max
		}
	} else {
		max := len(av.rules) - 1
		if max < 0 {
			max = 0
		}
		av.ruleCursor += delta
		if av.ruleCursor < 0 {
			av.ruleCursor = 0
		}
		if av.ruleCursor > max {
			av.ruleCursor = max
		}
	}
}

// clampAlertsCursor ensures the cursor doesn't exceed the list size.
func (a *App) clampAlertsCursor() {
	s := a.session()
	if s == nil {
		return
	}
	av := &s.AlertsView
	items := buildAlertList(s.Alerts, av.resolved, av.showResolved)
	if av.alertCursor >= len(items) {
		av.alertCursor = len(items) - 1
	}
	if av.alertCursor < 0 {
		av.alertCursor = 0
	}
}

// enterDetailByContainerID navigates to the detail view for a specific container.
func (a *App) enterDetailByContainerID(containerID string) (App, tea.Cmd) {
	s := a.session()
	if s == nil || s.Client == nil {
		return *a, nil
	}

	det := &s.Detail
	det.reset()
	det.containerID = containerID

	for _, ci := range s.ContInfo {
		if ci.ID == containerID {
			if ci.Service != "" {
				det.svcProject = ci.Project
				det.svcService = ci.Service
			} else {
				det.svcService = ci.Name
			}
			break
		}
	}

	a.view = viewDetail
	cmd := det.onSwitch(s.Client, a.windowSeconds(), s.RetentionDays)
	return *a, cmd
}

// instanceKeyContainerID extracts the container ID from an instance key.
// Instance keys are formatted as "rulename:containerID" for container-scoped alerts.
func instanceKeyContainerID(key string) string {
	idx := strings.Index(key, ":")
	if idx < 0 {
		return ""
	}
	return key[idx+1:]
}

// instanceDisplayName resolves an instance key to a human-readable name.
func instanceDisplayName(key string, contInfo []protocol.ContainerInfo) string {
	containerID := instanceKeyContainerID(key)
	if containerID == "" {
		// Host-scoped or disk-scoped — use the raw key.
		return key
	}
	name := serviceNameByID(containerID, contInfo)
	if name != "" {
		return name
	}
	// Truncated container ID as fallback.
	if len(containerID) > 12 {
		return containerID[:12]
	}
	return containerID
}
