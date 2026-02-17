package tui

import tea "github.com/charmbracelet/bubbletea"

func (a App) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Global quit.
	if key == "q" || key == "ctrl+c" {
		if a.helpModal {
			a.helpModal = false
			return a, nil
		}
		for _, s := range a.sessions {
			if s.connectCancel != nil {
				s.connectCancel()
			}
		}
		return a, tea.Quit
	}

	// Help modal blocks all input.
	if a.helpModal {
		if key == "?" || key == "esc" {
			a.helpModal = false
		}
		return a, nil
	}

	// Open help modal (unless a sub-modal is active).
	if key == "?" && !a.hasActiveSubModal() {
		a.helpModal = true
		return a, nil
	}

	// Detail view captures its own keys.
	if a.view == viewDetail {
		return a.handleDetailKey(msg)
	}

	// Alerts view captures its own keys.
	if a.view == viewAlerts {
		return a.handleAlertsKey(msg)
	}

	// Server switcher.
	if a.switcher {
		return a.handleSwitcherKey(key)
	}

	// Zoom time window.
	if key == "+" || key == "=" || key == "-" {
		if cmd := a.handleZoom(key); cmd != nil {
			return a, cmd
		}
		return a, nil
	}

	switch key {
	case "S":
		if len(a.sessions) > 1 {
			a.switcher = true
			// Set switcher cursor to current active session.
			for i, name := range a.sessionOrder {
				if name == a.activeSession {
					a.switcherCursor = i
					break
				}
			}
		}
		return a, nil

	case "j", "down":
		items := buildSelectableItems(a.groups, a.collapsed)
		max := len(items) - 1
		if max < 0 {
			max = 0
		}
		if a.cursor < max {
			a.cursor++
		}
		return a, nil

	case "k", "up":
		if a.cursor > 0 {
			a.cursor--
		}
		return a, nil

	case " ":
		items := buildSelectableItems(a.groups, a.collapsed)
		if a.cursor >= 0 && a.cursor < len(items) && items[a.cursor].isProject {
			name := a.groups[items[a.cursor].groupIdx].name
			a.collapsed[name] = !a.collapsed[name]
			// Rebuild items after toggle and clamp cursor.
			newItems := buildSelectableItems(a.groups, a.collapsed)
			if a.cursor >= len(newItems) {
				a.cursor = len(newItems) - 1
			}
			if a.cursor < 0 {
				a.cursor = 0
			}
		}
		return a, nil

	case "t":
		return a, a.toggleTracking()

	case "enter":
		return a.enterDetail()

	case "1":
		// Already on dashboard.
		return a, nil

	case "2":
		return a, a.enterAlerts()
	}

	return a, nil
}

func (a *App) handleSwitcherKey(key string) (App, tea.Cmd) {
	switch key {
	case "j", "down":
		if a.switcherCursor < len(a.sessionOrder)-1 {
			a.switcherCursor++
		}
	case "k", "up":
		if a.switcherCursor > 0 {
			a.switcherCursor--
		}
	case "enter":
		name := a.sessionOrder[a.switcherCursor]
		a.activeSession = name
		// Rebuild groups for newly selected session.
		if s := a.session(); s != nil {
			a.groups = buildGroups(s.Containers, s.ContInfo)
			a.cursor = 0
		}
		s := a.sessions[name]
		if s == nil {
			break
		}
		switch s.ConnState {
		case ConnNone:
			// Start connection, keep switcher open.
			return *a, func() tea.Msg { return connectServerMsg{name: name} }
		case ConnConnecting, ConnSSH:
			// Connection in progress — ignore.
		default:
			// Already connected — close switcher.
			a.switcher = false
		}
	case "esc", "S":
		a.switcher = false
	}
	return *a, nil
}

// handleZoom adjusts the time window and triggers a backfill.
func (a *App) handleZoom(key string) tea.Cmd {
	s := a.session()
	if s == nil || s.Client == nil {
		return nil
	}
	prev := a.windowIdx
	switch key {
	case "+", "=":
		for i := a.windowIdx + 1; i < len(timeWindows); i++ {
			w := timeWindows[i]
			if s.RetentionDays > 0 && w.seconds > int64(s.RetentionDays)*86400 {
				break
			}
			a.windowIdx = i
			break
		}
	case "-":
		if a.windowIdx > 0 {
			a.windowIdx--
		}
	}
	if a.windowIdx == prev {
		return nil
	}
	// Clear history so graphs show fresh data from the backfill.
	s.HostCPUHist = NewRingBuffer[float64](histBufSize)
	s.HostMemHist = NewRingBuffer[float64](histBufSize)
	s.BackfillPending = true

	cmds := []tea.Cmd{backfillMetrics(s.Client, a.windowSeconds())}

	// Re-backfill detail metrics when in detail view.
	if a.view == viewDetail {
		s.Detail.cpuHist = NewRingBuffer[float64](histBufSize)
		s.Detail.memHist = NewRingBuffer[float64](histBufSize)
		s.Detail.metricsBackfilled = false
		s.Detail.metricsBackfillPending = false
		if cmd := s.Detail.onSwitch(s.Client, a.windowSeconds(), s.RetentionDays); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	return tea.Batch(cmds...)
}
