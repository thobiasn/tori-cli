package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/thobiasn/tori-cli/internal/protocol"
)

// AlertViewState holds state for the full-screen alert history view.
type AlertViewState struct {
	alerts      []protocol.AlertMsg
	cursor      int
	scroll      int
	expandModal *alertExpandModal // nil = closed
	stale       bool

	// Sub-views: 0 = alerts, 1 = rules.
	subView      int
	showResolved bool

	// Rules sub-view.
	rules       []protocol.AlertRuleInfo
	rulesCursor int
	rulesStale  bool

	// Silence picker.
	silenceMode    bool
	silenceCursor  int
	silenceAlertID int64
	silenceRule    string
}

// alertExpandModal holds state for the alert detail overlay.
type alertExpandModal struct {
	alert  protocol.AlertMsg
	server string
	scroll int
}

type alertQueryMsg struct {
	alerts []protocol.AlertMsg
}

type alertRulesQueryMsg struct {
	rules []protocol.AlertRuleInfo
}

type alertActionDoneMsg struct{}

var silenceDurations = []struct {
	label string
	secs  int64
}{
	{"5m", 300},
	{"15m", 900},
	{"1h", 3600},
	{"6h", 21600},
	{"24h", 86400},
}

func newAlertViewState() AlertViewState {
	return AlertViewState{stale: true, rulesStale: true}
}

// sectionItem represents either a section header or an alert row in the
// flattened list used for rendering and navigation.
type sectionItem struct {
	isHeader bool
	header   string // section title, e.g. "FIRING (2)"
	alert    protocol.AlertMsg
}

// alertSections splits alerts into firing, acknowledged, and resolved groups.
func alertSections(alerts []protocol.AlertMsg) (firing, acked, resolved []protocol.AlertMsg) {
	for _, a := range alerts {
		switch {
		case a.ResolvedAt > 0:
			resolved = append(resolved, a)
		case a.Acknowledged:
			acked = append(acked, a)
		default:
			firing = append(firing, a)
		}
	}
	return
}

// buildSectionItems builds the flat list of headers + alert rows for rendering.
func buildSectionItems(alerts []protocol.AlertMsg, showResolved bool) []sectionItem {
	firing, acked, resolved := alertSections(alerts)
	var items []sectionItem

	if len(firing) > 0 {
		items = append(items, sectionItem{isHeader: true, header: fmt.Sprintf("FIRING (%d)", len(firing))})
		for _, a := range firing {
			items = append(items, sectionItem{alert: a})
		}
	}
	if len(acked) > 0 {
		items = append(items, sectionItem{isHeader: true, header: fmt.Sprintf("ACKNOWLEDGED (%d)", len(acked))})
		for _, a := range acked {
			items = append(items, sectionItem{alert: a})
		}
	}
	if len(resolved) > 0 {
		if showResolved {
			items = append(items, sectionItem{isHeader: true, header: fmt.Sprintf("RESOLVED (%d)", len(resolved))})
			for _, a := range resolved {
				items = append(items, sectionItem{alert: a})
			}
		} else {
			items = append(items, sectionItem{isHeader: true, header: fmt.Sprintf("RESOLVED (%d) — press r to expand", len(resolved))})
		}
	}
	return items
}

// clampCursorToItems ensures cursor points to a non-header row.
func clampCursorToItems(av *AlertViewState, items []sectionItem) {
	if len(items) == 0 {
		av.cursor = 0
		return
	}
	if av.cursor >= len(items) {
		av.cursor = len(items) - 1
	}
	if av.cursor < 0 {
		av.cursor = 0
	}
	// If cursor is on a header, move to next data row.
	if items[av.cursor].isHeader {
		for i := av.cursor; i < len(items); i++ {
			if !items[i].isHeader {
				av.cursor = i
				return
			}
		}
		// All remaining items are headers — search backwards.
		for i := av.cursor - 1; i >= 0; i-- {
			if !items[i].isHeader {
				av.cursor = i
				return
			}
		}
	}
}

// relativeTime formats a Unix timestamp as a relative time string.
func relativeTime(now time.Time, ts int64) string {
	if ts == 0 {
		return ""
	}
	d := now.Sub(time.Unix(ts, 0))
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// formatDurationShort formats seconds into a short human-readable string.
func formatDurationShort(seconds int64) string {
	if seconds <= 0 {
		return ""
	}
	d := time.Duration(seconds) * time.Second
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func (s *AlertViewState) onSwitch(c *Client) tea.Cmd {
	if !s.stale {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		start := time.Now().Add(-24 * time.Hour).Unix()
		end := time.Now().Unix()
		alerts, err := c.QueryAlerts(ctx, start, end)
		if err != nil {
			return alertQueryMsg{}
		}
		return alertQueryMsg{alerts: alerts}
	}
}

func queryAlertRulesCmd(c *Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		rules, err := c.QueryAlertRules(ctx)
		if err != nil {
			return alertRulesQueryMsg{}
		}
		return alertRulesQueryMsg{rules: rules}
	}
}

// renderAlertView renders the full-screen alert history.
func renderAlertView(a *App, s *Session, width, height int) string {
	av := &s.Alertv

	if av.subView == 1 {
		return renderRulesSubView(a, s, width, height)
	}
	return renderAlertsSubView(a, s, width, height)
}

func renderAlertsSubView(a *App, s *Session, width, height int) string {
	theme := &a.theme
	av := &s.Alertv
	innerH := height - 3
	if innerH < 1 {
		innerH = 1
	}
	innerW := width - 2

	items := buildSectionItems(av.alerts, av.showResolved)

	if len(av.alerts) == 0 {
		title := alertTabTitle(av)
		msg := "  No alerts in the last 24 hours"
		return Box(title, msg, width, height-1, theme) + "\n" + renderAlertFooter(av, width, theme)
	}

	if len(items) == 0 {
		title := alertTabTitle(av)
		return Box(title, "", width, height-1, theme) + "\n" + renderAlertFooter(av, width, theme)
	}

	clampCursorToItems(av, items)

	start := av.scroll
	if start > len(items) {
		start = len(items)
	}
	end := start + innerH
	if end > len(items) {
		end = len(items)
	}
	visible := items[start:end]

	now := time.Now()
	accent := lipgloss.NewStyle().Foreground(theme.Accent).Bold(true)
	muted := lipgloss.NewStyle().Foreground(theme.Muted)

	var lines []string
	for i, item := range visible {
		globalIdx := start + i

		if item.isHeader {
			line := " " + accent.Render(item.header)
			lines = append(lines, TruncateStyled(line, innerW))
			continue
		}

		alert := item.alert
		sev := severityTag(alert.Severity, theme)

		// Build status text.
		var status string
		if alert.ResolvedAt > 0 {
			status = muted.Render(relativeTime(now, alert.ResolvedAt))
		} else if alert.Acknowledged {
			status = muted.Render("ACK")
		} else {
			status = lipgloss.NewStyle().Foreground(theme.Critical).Render("ACTIVE")
		}

		// Calculate available space for the message. This prevents the row
		// from exceeding innerW, which was causing TruncateStyled to strip
		// all ANSI (the "color bug" from the plan).
		rule := Truncate(alert.RuleName, 16)
		prefix := fmt.Sprintf(" %s  %-16s ", sev, rule)
		prefixW := lipgloss.Width(prefix) + lipgloss.Width(status) + 2
		msgW := innerW - prefixW
		if msgW < 0 {
			msgW = 0
		}
		msg := Truncate(alert.Message, msgW)
		// Pad msg to fill available space so status aligns right.
		padded := msg + strings.Repeat(" ", max(0, msgW-len(msg)))

		row := prefix + padded + "  " + status
		if globalIdx == av.cursor {
			row = lipgloss.NewStyle().Reverse(true).Render(Truncate(stripANSI(row), innerW))
		}
		lines = append(lines, TruncateStyled(row, innerW))
	}

	title := alertTabTitle(av)
	boxH := height - 1
	content := strings.Join(lines, "\n")
	box := Box(title, content, width, boxH, theme)
	return box + "\n" + renderAlertFooter(av, width, theme)
}

func renderRulesSubView(a *App, s *Session, width, height int) string {
	theme := &a.theme
	av := &s.Alertv
	innerH := height - 3
	if innerH < 1 {
		innerH = 1
	}
	innerW := width - 2

	title := alertTabTitle(av)

	if len(av.rules) == 0 {
		msg := "  No alert rules configured"
		return Box(title, msg, width, height-1, theme) + "\n" + renderAlertFooter(av, width, theme)
	}

	now := time.Now()

	var lines []string
	for i, rule := range av.rules {
		sev := severityTag(rule.Severity, theme)

		// Status.
		var status string
		if rule.SilencedUntil > 0 && time.Unix(rule.SilencedUntil, 0).After(now) {
			remaining := time.Unix(rule.SilencedUntil, 0).Sub(now).Seconds()
			status = lipgloss.NewStyle().Foreground(theme.Warning).Render("silenced " + formatDurationShort(int64(remaining)))
		} else if rule.FiringCount > 0 {
			status = lipgloss.NewStyle().Foreground(theme.Critical).Render(fmt.Sprintf("%d firing", rule.FiringCount))
		} else {
			status = lipgloss.NewStyle().Foreground(theme.Healthy).Render("ok")
		}

		name := Truncate(rule.Name, 20)
		prefix := fmt.Sprintf(" %s  %-20s ", sev, name)
		prefixW := lipgloss.Width(prefix) + lipgloss.Width(status) + 2
		condW := innerW - prefixW
		if condW < 0 {
			condW = 0
		}
		cond := Truncate(rule.Condition, condW)
		padded := cond + strings.Repeat(" ", max(0, condW-len(cond)))

		row := prefix + padded + "  " + status
		if i == av.rulesCursor {
			row = lipgloss.NewStyle().Reverse(true).Render(Truncate(stripANSI(row), innerW))
		}
		lines = append(lines, TruncateStyled(row, innerW))
	}

	boxH := height - 1
	content := strings.Join(lines, "\n")
	box := Box(title, content, width, boxH, theme)
	return box + "\n" + renderAlertFooter(av, width, theme)
}

func alertTabTitle(av *AlertViewState) string {
	if av.subView == 0 {
		return fmt.Sprintf("[Alerts] | Rules")
	}
	return fmt.Sprintf("Alerts | [Rules]")
}

func renderSilencePicker(s *AlertViewState, theme *Theme) string {
	var lines []string
	lines = append(lines, fmt.Sprintf(" Silence %s for:", Truncate(s.silenceRule, 20)))
	for i, d := range silenceDurations {
		marker := "  "
		if i == s.silenceCursor {
			marker = "> "
		}
		lines = append(lines, marker+d.label)
	}
	content := strings.Join(lines, "\n")
	pickerW := 30
	pickerH := len(silenceDurations) + 3
	return Box("Silence", content, pickerW, pickerH, theme)
}

// updateAlertExpandModal handles keys inside the alert expand modal.
func updateAlertExpandModal(av *AlertViewState, key string) tea.Cmd {
	m := av.expandModal
	switch key {
	case "esc", "enter":
		av.expandModal = nil
	case "j", "down":
		m.scroll++
	case "k", "up":
		if m.scroll > 0 {
			m.scroll--
		}
	}
	return nil
}

// renderAlertExpandModal renders a centered modal overlay showing alert details.
func renderAlertExpandModal(m *alertExpandModal, width, height int, theme *Theme, tsFormat string) string {
	modalW := width * 3 / 4
	if modalW < 40 {
		modalW = 40
	}
	if modalW > width-4 {
		modalW = width - 4
	}
	modalH := height - 4
	if modalH < 10 {
		modalH = 10
	}
	innerW := modalW - 2
	innerH := modalH - 2
	if innerH < 1 {
		innerH = 1
	}

	label := lipgloss.NewStyle().Foreground(theme.Muted)

	// Status text.
	var status string
	if m.alert.ResolvedAt > 0 {
		status = lipgloss.NewStyle().Foreground(theme.Healthy).Render("RESOLVED")
	} else if m.alert.Acknowledged {
		status = lipgloss.NewStyle().Foreground(theme.Muted).Render("ACK")
	} else {
		status = lipgloss.NewStyle().Foreground(theme.Critical).Render("ACTIVE")
	}

	// Metadata header.
	var header []string
	header = append(header, label.Render(" server:    ")+m.server)
	header = append(header, label.Render(" rule:      ")+m.alert.RuleName)
	header = append(header, label.Render(" severity:  ")+m.alert.Severity)
	header = append(header, label.Render(" status:    ")+status)
	header = append(header, "")
	header = append(header, label.Render(" condition: ")+m.alert.Condition)
	if m.alert.InstanceKey != "" {
		header = append(header, label.Render(" instance:  ")+m.alert.InstanceKey)
	}
	header = append(header, label.Render(" fired:     ")+FormatTimestamp(m.alert.FiredAt, tsFormat))
	if m.alert.ResolvedAt > 0 {
		header = append(header, label.Render(" resolved:  ")+FormatTimestamp(m.alert.ResolvedAt, tsFormat))
	}

	// If there's a message, add separator and scrollable content.
	if m.alert.Message == "" {
		content := strings.Join(header, "\n")
		return Box("Alert", content, modalW, modalH, theme)
	}

	muted := lipgloss.NewStyle().Foreground(theme.Muted)
	header = append(header, " "+muted.Render(strings.Repeat("─", innerW-2)))

	contentH := innerH - len(header)
	if contentH < 1 {
		contentH = 1
	}

	wrapped := wrapText(m.alert.Message, innerW-2)
	if len(wrapped) == 0 {
		wrapped = []string{""}
	}

	// Clamp scroll.
	maxScroll := len(wrapped) - contentH
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.scroll > maxScroll {
		m.scroll = maxScroll
	}

	start := m.scroll
	end := start + contentH
	if end > len(wrapped) {
		end = len(wrapped)
	}

	var lines []string
	lines = append(lines, header...)
	for _, l := range wrapped[start:end] {
		lines = append(lines, " "+l)
	}

	content := strings.Join(lines, "\n")
	return Box("Alert", content, modalW, modalH, theme)
}

func renderAlertFooter(s *AlertViewState, width int, theme *Theme) string {
	_ = theme
	if s.silenceMode {
		return Truncate(" j/k navigate  Enter confirm  Esc cancel", width)
	}
	if s.expandModal != nil {
		return Truncate(" j/k scroll  Esc/Enter close", width)
	}
	if s.subView == 1 {
		return Truncate(" j/k navigate  s silence  Tab alerts  Esc back  ? Help", width)
	}
	return Truncate(" j/k navigate  r resolved  a ack  s silence  Enter expand  Tab rules  Esc back  ? Help", width)
}

// updateAlertView handles keys in the alert view.
func updateAlertView(a *App, s *Session, msg tea.KeyMsg) tea.Cmd {
	av := &s.Alertv
	key := msg.String()

	// Modal captures all keys when open.
	if av.expandModal != nil {
		return updateAlertExpandModal(av, key)
	}
	if av.silenceMode {
		return updateSilencePicker(s, av, key)
	}

	switch key {
	case "tab":
		// Toggle sub-view.
		if av.subView == 0 {
			av.subView = 1
			if s.Client != nil && av.rulesStale {
				return queryAlertRulesCmd(s.Client)
			}
		} else {
			av.subView = 0
		}
		return nil
	}

	if av.subView == 1 {
		return updateRulesSubView(a, s, key)
	}
	return updateAlertsSubView(a, s, key)
}

func updateAlertsSubView(a *App, s *Session, key string) tea.Cmd {
	av := &s.Alertv
	items := buildSectionItems(av.alerts, av.showResolved)

	switch key {
	case "j", "down":
		// Move to next non-header item.
		for next := av.cursor + 1; next < len(items); next++ {
			if !items[next].isHeader {
				av.cursor = next
				break
			}
		}
		// Auto-scroll.
		innerH := a.height - 4
		if innerH < 1 {
			innerH = 1
		}
		if av.cursor >= av.scroll+innerH {
			av.scroll = av.cursor - innerH + 1
		}
	case "k", "up":
		// Move to previous non-header item.
		for prev := av.cursor - 1; prev >= 0; prev-- {
			if !items[prev].isHeader {
				av.cursor = prev
				break
			}
		}
		if av.cursor < av.scroll {
			av.scroll = av.cursor
		}
	case "r":
		av.showResolved = !av.showResolved
		// Rebuild and re-clamp.
		items = buildSectionItems(av.alerts, av.showResolved)
		clampCursorToItems(av, items)
	case "a":
		// Acknowledge selected alert.
		if s.Client != nil && av.cursor < len(items) && !items[av.cursor].isHeader {
			alert := items[av.cursor].alert
			if alert.ResolvedAt == 0 && !alert.Acknowledged {
				return ackAlertCmd(s.Client, alert.ID)
			}
		}
	case "s":
		// Open silence picker.
		if av.cursor < len(items) && !items[av.cursor].isHeader {
			alert := items[av.cursor].alert
			av.silenceMode = true
			av.silenceCursor = 0
			av.silenceAlertID = alert.ID
			av.silenceRule = alert.RuleName
		}
	case "enter":
		if av.cursor < len(items) && !items[av.cursor].isHeader {
			av.expandModal = &alertExpandModal{
				alert:  items[av.cursor].alert,
				server: s.Name,
			}
		}
	case "esc":
		av.cursor = 0
		av.scroll = 0
	}
	return nil
}

func updateRulesSubView(a *App, s *Session, key string) tea.Cmd {
	av := &s.Alertv
	switch key {
	case "j", "down":
		if av.rulesCursor < len(av.rules)-1 {
			av.rulesCursor++
		}
	case "k", "up":
		if av.rulesCursor > 0 {
			av.rulesCursor--
		}
	case "s":
		if av.rulesCursor < len(av.rules) {
			rule := av.rules[av.rulesCursor]
			av.silenceMode = true
			av.silenceCursor = 0
			av.silenceRule = rule.Name
		}
	case "esc":
		av.rulesCursor = 0
	}
	return nil
}

func updateSilencePicker(s *Session, av *AlertViewState, key string) tea.Cmd {
	switch key {
	case "j", "down":
		if av.silenceCursor < len(silenceDurations)-1 {
			av.silenceCursor++
		}
	case "k", "up":
		if av.silenceCursor > 0 {
			av.silenceCursor--
		}
	case "enter":
		dur := silenceDurations[av.silenceCursor].secs
		rule := av.silenceRule
		av.silenceMode = false
		if s.Client == nil {
			return nil
		}
		return silenceAlertCmd(s.Client, rule, dur)
	case "esc":
		av.silenceMode = false
	}
	return nil
}

func ackAlertCmd(c *Client, alertID int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c.AckAlert(ctx, alertID)
		return alertActionDoneMsg{}
	}
}

func silenceAlertCmd(c *Client, rule string, dur int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c.SilenceAlert(ctx, rule, dur)
		return alertActionDoneMsg{}
	}
}

