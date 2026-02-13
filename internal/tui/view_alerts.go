package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/thobiasn/rook/internal/protocol"
)

// AlertViewState holds state for the full-screen alert history view.
type AlertViewState struct {
	alerts   []protocol.AlertMsg
	cursor   int
	scroll   int
	expanded int // -1 = none
	stale    bool

	// Filters (empty string = show all).
	filterSeverity string // "", "warning", "critical"
	filterState    string // "", "active", "acknowledged", "resolved"

	// Silence picker.
	silenceMode    bool
	silenceCursor  int
	silenceAlertID int64
	silenceRule    string
}

type alertQueryMsg struct {
	alerts []protocol.AlertMsg
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
	return AlertViewState{stale: true, expanded: -1}
}

// filteredAlerts returns alerts matching the current severity and state filters.
func (s *AlertViewState) filteredAlerts() []protocol.AlertMsg {
	if s.filterSeverity == "" && s.filterState == "" {
		return s.alerts
	}
	var out []protocol.AlertMsg
	for _, a := range s.alerts {
		if s.filterSeverity != "" && a.Severity != s.filterSeverity {
			continue
		}
		if s.filterState != "" {
			switch s.filterState {
			case "active":
				if a.ResolvedAt != 0 || a.Acknowledged {
					continue
				}
			case "acknowledged":
				if !a.Acknowledged {
					continue
				}
			case "resolved":
				if a.ResolvedAt == 0 {
					continue
				}
			}
		}
		out = append(out, a)
	}
	return out
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

// renderAlertView renders the full-screen alert history.
func renderAlertView(a *App, s *Session, width, height int) string {
	theme := &a.theme
	av := &s.Alertv
	innerH := height - 3
	if innerH < 1 {
		innerH = 1
	}
	innerW := width - 2

	// Determine visible slice.
	alerts := av.filteredAlerts()
	if len(alerts) == 0 {
		msg := "  No alerts in the last 24 hours"
		if len(av.alerts) > 0 {
			msg = "  No alerts match the current filter"
		}
		title := alertTitle(av, alerts)
		return Box(title, msg, width, height-1, theme) + "\n" + renderAlertFooter(av, width, theme)
	}

	start := av.scroll
	if start > len(alerts) {
		start = len(alerts)
	}
	end := start + innerH
	if end > len(alerts) {
		end = len(alerts)
	}
	visible := alerts[start:end]

	// Count expansion lines for the expanded alert.
	expandIdx := av.expanded - start // relative index within visible
	var expandLines int
	if av.expanded >= start && expandIdx < len(visible) {
		al := visible[expandIdx]
		expandLines = 1 // condition
		if al.InstanceKey != "" {
			expandLines++
		}
		expandLines++ // fired
		if al.ResolvedAt > 0 {
			expandLines++
		}
		if al.Message != "" {
			expandLines += len(wrapText(al.Message, innerW-4))
		}
	}

	// If expansion would overflow, trim entries from the top.
	if expandLines > 0 && len(visible)+expandLines > innerH {
		trim := len(visible) + expandLines - innerH
		if trim > len(visible) {
			trim = len(visible)
		}
		visible = visible[trim:]
		start += trim
	}

	muted := lipgloss.NewStyle().Foreground(theme.Muted)
	var lines []string
	for i, alert := range visible {
		globalIdx := start + i
		sev := severityTag(alert.Severity, theme)
		ts := time.Unix(alert.FiredAt, 0).Format("15:04")
		rule := Truncate(alert.RuleName, 16)
		msg := Truncate(alert.Message, innerW-40)

		var status string
		if alert.ResolvedAt > 0 {
			status = lipgloss.NewStyle().Foreground(theme.Healthy).Render("RESOLVED")
		} else if alert.Acknowledged {
			status = lipgloss.NewStyle().Foreground(theme.Muted).Render("ACK")
		} else {
			status = lipgloss.NewStyle().Foreground(theme.Critical).Render("ACTIVE")
		}

		row := fmt.Sprintf(" %s  %s  %-16s %-*s %s", sev, ts, rule, innerW-42, msg, status)
		if globalIdx == av.cursor {
			row = lipgloss.NewStyle().Reverse(true).Render(Truncate(stripANSI(row), innerW))
		}
		lines = append(lines, TruncateStyled(row, innerW))

		if globalIdx == av.expanded {
			firedStr := time.Unix(alert.FiredAt, 0).Format("2006-01-02 15:04:05")
			lines = append(lines, muted.Render("   Condition: ")+alert.Condition)
			if alert.InstanceKey != "" {
				lines = append(lines, muted.Render("   Instance:  ")+alert.InstanceKey)
			}
			lines = append(lines, muted.Render("   Fired:     ")+firedStr)
			if alert.ResolvedAt > 0 {
				resolvedStr := time.Unix(alert.ResolvedAt, 0).Format("2006-01-02 15:04:05")
				lines = append(lines, muted.Render("   Resolved:  ")+resolvedStr)
			}
			if alert.Message != "" {
				for _, wl := range wrapText(alert.Message, innerW-4) {
					lines = append(lines, "   "+wl)
				}
			}
		}
	}

	title := alertTitle(av, alerts)
	boxH := height - 1
	content := strings.Join(lines, "\n")
	box := Box(title, content, width, boxH, theme)

	// Silence picker overlay.
	if av.silenceMode {
		box = renderSilencePicker(av, width, boxH, theme)
	}

	return box + "\n" + renderAlertFooter(av, width, theme)
}

func alertTitle(av *AlertViewState, filtered []protocol.AlertMsg) string {
	if av.filterSeverity == "" && av.filterState == "" {
		return fmt.Sprintf("Alerts (%d)", len(filtered))
	}
	title := fmt.Sprintf("Alerts (%d/%d)", len(filtered), len(av.alerts))
	if av.filterSeverity != "" {
		title += " [" + av.filterSeverity + "]"
	}
	if av.filterState != "" {
		title += " [" + av.filterState + "]"
	}
	return title
}

func renderSilencePicker(s *AlertViewState, width, height int, theme *Theme) string {
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
	picker := Box("Silence", content, pickerW, pickerH, theme)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, picker)
}

func renderAlertFooter(s *AlertViewState, width int, theme *Theme) string {
	_ = theme
	if s.silenceMode {
		return Truncate(" j/k navigate  Enter confirm  Esc cancel", width)
	}
	return Truncate(" j/k navigate  f filter  a ack  s silence  Enter expand  Esc back  ? Help", width)
}

// updateAlertView handles keys in the alert view.
func updateAlertView(a *App, s *Session, msg tea.KeyMsg) tea.Cmd {
	av := &s.Alertv
	key := msg.String()

	if av.silenceMode {
		return updateSilencePicker(s, av, key)
	}

	filtered := av.filteredAlerts()

	switch key {
	case "j", "down":
		if av.cursor < len(filtered)-1 {
			av.cursor++
			av.expanded = -1
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
		if av.cursor > 0 {
			av.cursor--
			av.expanded = -1
		}
		if av.cursor < av.scroll {
			av.scroll = av.cursor
		}
	case "f":
		// Cycle severity filter: all → warning → critical → all.
		switch av.filterSeverity {
		case "":
			av.filterSeverity = "warning"
		case "warning":
			av.filterSeverity = "critical"
		default:
			av.filterSeverity = ""
		}
		av.cursor = 0
		av.scroll = 0
		av.expanded = -1
	case "F":
		// Cycle state filter: all → active → acknowledged → resolved → all.
		switch av.filterState {
		case "":
			av.filterState = "active"
		case "active":
			av.filterState = "acknowledged"
		case "acknowledged":
			av.filterState = "resolved"
		default:
			av.filterState = ""
		}
		av.cursor = 0
		av.scroll = 0
		av.expanded = -1
	case "a":
		// Acknowledge selected alert.
		if s.Client != nil && av.cursor < len(filtered) {
			alert := filtered[av.cursor]
			if alert.ResolvedAt == 0 && !alert.Acknowledged {
				return ackAlertCmd(s.Client, alert.ID)
			}
		}
	case "s":
		// Open silence picker.
		if av.cursor < len(filtered) {
			alert := filtered[av.cursor]
			av.silenceMode = true
			av.silenceCursor = 0
			av.silenceAlertID = alert.ID
			av.silenceRule = alert.RuleName
		}
	case "enter":
		if av.cursor < len(filtered) {
			if av.expanded == av.cursor {
				av.expanded = -1
			} else {
				av.expanded = av.cursor
			}
		}
	case "esc":
		if av.expanded >= 0 {
			av.expanded = -1
		} else {
			av.cursor = 0
			av.scroll = 0
		}
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
