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
	alerts []protocol.AlertMsg
	cursor int
	scroll int
	stale  bool

	// Silence picker.
	silenceMode    bool
	silenceCursor  int
	silenceAlertID int64
	silenceRule    string
}

type alertQueryMsg struct {
	alerts []protocol.AlertMsg
}

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
	return AlertViewState{stale: true}
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
func renderAlertView(a *App, width, height int) string {
	theme := &a.theme
	s := &a.alertv
	innerH := height - 3
	if innerH < 1 {
		innerH = 1
	}
	innerW := width - 2

	// Determine visible slice.
	alerts := s.alerts
	if len(alerts) == 0 {
		content := "  No alerts in the last 24 hours"
		return Box("Alerts", content, width, height-1, theme) + "\n" + renderAlertFooter(s, width, theme)
	}

	start := s.scroll
	if start > len(alerts) {
		start = len(alerts)
	}
	end := start + innerH
	if end > len(alerts) {
		end = len(alerts)
	}
	visible := alerts[start:end]

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
		if globalIdx == s.cursor {
			row = lipgloss.NewStyle().Reverse(true).Render(Truncate(stripANSI(row), innerW))
		}
		lines = append(lines, Truncate(row, innerW))
	}

	title := fmt.Sprintf("Alerts (%d)", len(alerts))
	boxH := height - 1
	content := strings.Join(lines, "\n")
	box := Box(title, content, width, boxH, theme)

	// Silence picker overlay.
	if s.silenceMode {
		box = renderSilencePicker(s, box, width, height, theme)
	}

	return box + "\n" + renderAlertFooter(s, width, theme)
}

func renderSilencePicker(s *AlertViewState, base string, width, height int, theme *Theme) string {
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

	// Center the picker overlay on the base.
	_ = base
	return picker
}

func renderAlertFooter(s *AlertViewState, width int, theme *Theme) string {
	_ = theme
	if s.silenceMode {
		return Truncate(" j/k navigate  Enter confirm  Esc cancel", width)
	}
	return Truncate(" j/k navigate  a ack  s silence  Enter expand  Esc back  ? Help", width)
}

// updateAlertView handles keys in the alert view.
func updateAlertView(a *App, msg tea.KeyMsg) tea.Cmd {
	s := &a.alertv
	key := msg.String()

	if s.silenceMode {
		return updateSilencePicker(a, s, key)
	}

	switch key {
	case "j", "down":
		if s.cursor < len(s.alerts)-1 {
			s.cursor++
		}
		// Auto-scroll.
		innerH := a.height - 4
		if innerH < 1 {
			innerH = 1
		}
		if s.cursor >= s.scroll+innerH {
			s.scroll = s.cursor - innerH + 1
		}
	case "k", "up":
		if s.cursor > 0 {
			s.cursor--
		}
		if s.cursor < s.scroll {
			s.scroll = s.cursor
		}
	case "a":
		// Acknowledge selected alert.
		if s.cursor < len(s.alerts) {
			alert := s.alerts[s.cursor]
			if alert.ResolvedAt == 0 && !alert.Acknowledged {
				return ackAlertCmd(a.client, alert.ID)
			}
		}
	case "s":
		// Open silence picker.
		if s.cursor < len(s.alerts) {
			alert := s.alerts[s.cursor]
			s.silenceMode = true
			s.silenceCursor = 0
			s.silenceAlertID = alert.ID
			s.silenceRule = alert.RuleName
		}
	case "esc":
		// Clear state.
		s.cursor = 0
		s.scroll = 0
	}
	return nil
}

func updateSilencePicker(a *App, s *AlertViewState, key string) tea.Cmd {
	switch key {
	case "j", "down":
		if s.silenceCursor < len(silenceDurations)-1 {
			s.silenceCursor++
		}
	case "k", "up":
		if s.silenceCursor > 0 {
			s.silenceCursor--
		}
	case "enter":
		dur := silenceDurations[s.silenceCursor].secs
		rule := s.silenceRule
		s.silenceMode = false
		return silenceAlertCmd(a.client, rule, dur)
	case "esc":
		s.silenceMode = false
	}
	return nil
}

func ackAlertCmd(c *Client, alertID int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c.AckAlert(ctx, alertID)
		return nil
	}
}

func silenceAlertCmd(c *Client, rule string, dur int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c.SilenceAlert(ctx, rule, dur)
		return nil
	}
}
