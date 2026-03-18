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

	// Server switcher captures all keys when active.
	if a.switcher {
		return a.handleSwitcherKey(key)
	}

	// Global view/server navigation (blocked by sub-modals).
	if !a.hasActiveSubModal() {
		switch key {
		case "S":
			if len(a.sessions) > 1 {
				a.switcher = true
				for i, name := range a.sessionOrder {
					if name == a.activeSession {
						a.switcherCursor = i
						break
					}
				}
			}
			return a, nil
		case "1":
			a.pendingKey = ""
			switch a.view {
			case viewDetail:
				a.leaveDetail()
			case viewAlerts:
				a.leaveAlerts()
			}
			return a, nil
		case "2":
			a.pendingKey = ""
			if a.view == viewDetail {
				a.leaveDetail()
			}
			if a.view != viewAlerts {
				return a, a.enterAlerts()
			}
			return a, nil
		}
	}

	// Detail view captures its own keys.
	if a.view == viewDetail {
		return a.handleDetailKey(msg)
	}

	// Alerts view captures its own keys.
	if a.view == viewAlerts {
		return a.handleAlertsKey(msg)
	}

	// Chord resolution: gg = jump to top.
	if a.pendingKey == "g" {
		a.pendingKey = ""
		if key == "g" {
			a.cursor = 0
			return a, nil
		}
		// Fall through to process key normally.
	}

	// Zoom time window.
	if key == "+" || key == "=" || key == "-" {
		if cmd := a.handleZoom(key); cmd != nil {
			return a, cmd
		}
		return a, nil
	}

	items := buildSelectableItems(a.groups, a.collapsed)

	switch key {
	case "j", "down":
		clampNav(&a.cursor, 1, len(items))
		return a, nil

	case "k", "up":
		clampNav(&a.cursor, -1, len(items))
		return a, nil

	case " ":
		if a.cursor >= 0 && a.cursor < len(items) && items[a.cursor].isProject {
			name := a.groups[items[a.cursor].groupIdx].name
			a.collapsed[name] = !a.collapsed[name]
			newItems := buildSelectableItems(a.groups, a.collapsed)
			clampNav(&a.cursor, 0, len(newItems))
		}
		return a, nil

	case "g":
		a.pendingKey = "g"
		return a, nil

	case "G":
		a.cursor = max(len(items)-1, 0)
		return a, nil

	case "}":
		found := false
		for i := a.cursor + 1; i < len(items); i++ {
			if items[i].isProject {
				a.cursor = i
				found = true
				break
			}
		}
		if !found {
			a.cursor = max(len(items)-1, 0)
		}
		return a, nil

	case "{":
		found := false
		for i := a.cursor - 1; i >= 0; i-- {
			if items[i].isProject {
				a.cursor = i
				found = true
				break
			}
		}
		if !found {
			a.cursor = 0
		}
		return a, nil

	case "ctrl+d":
		clampNav(&a.cursor, halfPage(a.height), len(items))
		return a, nil

	case "ctrl+u":
		clampNav(&a.cursor, -halfPage(a.height), len(items))
		return a, nil

	case "y":
		if a.cursor >= 0 && a.cursor < len(items) {
			item := items[a.cursor]
			g := a.groups[item.groupIdx]
			if item.isProject {
				yankToClipboard(g.name)
			} else {
				yankToClipboard(g.containers[item.contIdx].Name)
			}
		}
		return a, nil

	case "t":
		return a, a.toggleTracking()

	case "enter":
		a.pendingKey = ""
		return a.enterDetail()
	}

	return a, nil
}

func (a *App) handleSwitcherKey(key string) (App, tea.Cmd) {
	switch key {
	case "j", "down":
		clampNav(&a.switcherCursor, 1, len(a.sessionOrder))
	case "k", "up":
		clampNav(&a.switcherCursor, -1, len(a.sessionOrder))
	case "g":
		if a.pendingKey == "g" {
			a.pendingKey = ""
			a.switcherCursor = 0
		} else {
			a.pendingKey = "g"
		}
	case "G":
		a.pendingKey = ""
		a.switcherCursor = len(a.sessionOrder) - 1
	case "ctrl+d":
		clampNav(&a.switcherCursor, halfPage(a.height), len(a.sessionOrder))
	case "ctrl+u":
		clampNav(&a.switcherCursor, -halfPage(a.height), len(a.sessionOrder))
	case "enter":
		name := a.sessionOrder[a.switcherCursor]
		// Return to dashboard when switching servers from detail/alerts.
		if a.view == viewDetail {
			a.leaveDetail()
		} else if a.view == viewAlerts {
			a.leaveAlerts()
		}
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
	s.BackfillGen++
	s.BackfillPending = true

	cmds := []tea.Cmd{backfillMetrics(s.Client, a.windowSeconds(), s.BackfillGen)}

	// Re-backfill detail metrics when in detail view.
	if a.view == viewDetail {
		s.Detail.cpuHist = NewRingBuffer[float64](histBufSize)
		s.Detail.memHist = NewRingBuffer[float64](histBufSize)
		s.Detail.metricsGen++
		s.Detail.metricsBackfilled = false
		if cmd := s.Detail.onSwitch(s.Client, a.windowSeconds(), s.RetentionDays); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	return tea.Batch(cmds...)
}
