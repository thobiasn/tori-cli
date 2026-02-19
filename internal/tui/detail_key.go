package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// handleDetailKey handles keys when the detail view is active.
func (a *App) handleDetailKey(msg tea.KeyMsg) (App, tea.Cmd) {
	s := a.session()
	if s == nil {
		return *a, nil
	}
	det := &s.Detail
	key := msg.String()

	// Expand modal captures all keys.
	if det.expandModal != nil {
		return *a, updateExpandModal(det, a, s, key)
	}

	// Filter modal captures all keys.
	if det.filterModal != nil {
		return *a, updateFilterModal(det, s, key, a.display)
	}

	// Info overlay: dismiss with Esc or i.
	if det.infoOverlay {
		if key == "esc" || key == "i" {
			det.infoOverlay = false
		}
		return *a, nil
	}

	// Zoom.
	if key == "+" || key == "=" || key == "-" {
		if cmd := a.handleZoom(key); cmd != nil {
			return *a, cmd
		}
		return *a, nil
	}

	switch key {
	case "esc":
		if det.isSearchActive() {
			det.setSearchText("")
			det.filterLevel = ""
			det.filterFrom = 0
			det.filterTo = 0
			return *a, refetchLogs(det, s.Client, s.RetentionDays)
		}
		return *a, a.leaveDetail()

	case "s":
		switch det.filterLevel {
		case "":
			det.filterLevel = "ERR"
		case "ERR":
			det.filterLevel = "WARN"
		case "WARN":
			det.filterLevel = "INFO"
		case "INFO":
			det.filterLevel = "DBUG"
		default:
			det.filterLevel = ""
		}
		// Level filtering is server-side, so refetch. Scope is unchanged
		// (same container/project/time range) so skip the redundant count.
		det.resetLogs()
		return *a, fireSearch(det, s.Client, s.RetentionDays, true)

	case "f":
		now := time.Now()
		m := &logFilterModal{
			text:     det.searchText,
			fromDate: newMaskedField(a.display.DateFormat, now),
			fromTime: newMaskedField(a.display.TimeFormat, now),
			toDate:   newMaskedField(a.display.DateFormat, now),
			toTime:   newMaskedField(a.display.TimeFormat, now),
		}
		if det.filterFrom != 0 {
			t := time.Unix(det.filterFrom, 0)
			m.fromDate.fill(t.Format(a.display.DateFormat))
			m.fromTime.fill(t.Format(a.display.TimeFormat))
		}
		if det.filterTo != 0 {
			t := time.Unix(det.filterTo, 0)
			m.toDate.fill(t.Format(a.display.DateFormat))
			m.toTime.fill(t.Format(a.display.TimeFormat))
		}
		det.filterModal = m
		return *a, nil

	case "i":
		det.infoOverlay = true
		return *a, nil

	case "G":
		det.logScroll = 0
		det.logPaused = false
		data := det.filteredData()
		if len(data) > 0 {
			det.logCursor = len(data) - 1
		}
		return *a, nil
	}

	// Log scrolling.
	data := det.filteredData()
	innerH := logAreaHeight(a, det, s)
	start, end := visibleLogWindow(det, data, innerH, a.display.DateFormat)
	visibleCount := end - start
	maxScroll := len(data) - innerH
	if maxScroll < 0 {
		maxScroll = 0
	}

	switch key {
	case "j", "down":
		if det.logCursor < visibleCount-1 {
			det.logCursor++
		}
		// Resume when cursor reaches the bottom of visible entries.
		if det.logPaused && det.logCursor >= visibleCount-1 {
			det.resetLogPosition()
		}
	case "k", "up":
		det.logPaused = true
		if det.logCursor > 0 {
			det.logCursor--
		} else if det.logScroll < maxScroll {
			det.logScroll++
		}
	case "enter":
		if det.logCursor >= 0 && len(data) > 0 {
			idx := start + det.logCursor
			if idx >= 0 && idx < len(data) {
				project := det.project
				if project == "" {
					project = det.svcProject
				}
				svcName := serviceNameByID(data[idx].ContainerID, s.ContInfo)
				if svcName == "" {
					svcName = data[idx].ContainerName
				}
				det.expandModal = &logExpandModal{
					entry:       data[idx],
					server:      s.Name,
					project:     project,
					serviceName: svcName,
				}
			}
		}
	}
	return *a, nil
}

// updateFilterModal handles keys inside the filter modal.
func updateFilterModal(det *DetailState, s *Session, key string, cfg DisplayConfig) tea.Cmd {
	m := det.filterModal
	switch key {
	case "tab":
		if m.focus == 0 {
			m.focus = 1
		} else {
			m.focus = 0
		}
	case "enter":
		det.setSearchText(m.text)
		det.filterFrom = parseFilterBound(m.fromDate.resolved(), m.fromTime.resolved(), cfg.DateFormat, cfg.TimeFormat, false)
		det.filterTo = parseFilterBound(m.toDate.resolved(), m.toTime.resolved(), cfg.DateFormat, cfg.TimeFormat, true)
		det.filterModal = nil

		if det.isSearchActive() {
			det.resetLogs()
			return fireSearch(det, s.Client, s.RetentionDays, false)
		}
		return refetchLogs(det, s.Client, s.RetentionDays)
	case "esc":
		det.filterModal = nil
	case "f":
		if m.focus != 0 {
			det.filterModal = nil
		} else if len(m.text) < 128 {
			m.text += key
		}
	case "backspace":
		switch m.focus {
		case 0:
			if len(m.text) > 0 {
				m.text = m.text[:len(m.text)-1]
			}
		case 1:
			m.fromDate.backspace()
		case 2:
			m.fromTime.backspace()
		case 3:
			m.toDate.backspace()
		case 4:
			m.toTime.backspace()
		}
	default:
		if m.focus > 0 {
			switch key {
			case "h", "left":
				if m.focus == 2 {
					m.focus = 1
				} else if m.focus == 4 {
					m.focus = 3
				}
				return nil
			case "l", "right":
				if m.focus == 1 {
					m.focus = 2
				} else if m.focus == 3 {
					m.focus = 4
				}
				return nil
			case "j", "down":
				if m.focus == 1 {
					m.focus = 3
				} else if m.focus == 2 {
					m.focus = 4
				}
				return nil
			case "k", "up":
				if m.focus == 3 {
					m.focus = 1
				} else if m.focus == 4 {
					m.focus = 2
				}
				return nil
			}
		}
		if len(key) == 1 {
			switch m.focus {
			case 0:
				if len(m.text) < 128 {
					m.text += key
				}
			case 1:
				if m.fromDate.typeRune(rune(key[0])) {
					m.focus = 2
				}
			case 2:
				if m.fromTime.typeRune(rune(key[0])) {
					m.focus = 3
				}
			case 3:
				if m.toDate.typeRune(rune(key[0])) {
					m.focus = 4
				}
			case 4:
				m.toTime.typeRune(rune(key[0]))
			}
		}
	}
	return nil
}

// updateExpandModal handles keys inside the log expand modal.
func updateExpandModal(det *DetailState, a *App, s *Session, key string) tea.Cmd {
	m := det.expandModal
	switch key {
	case "esc", "enter":
		det.expandModal = nil
	case "n":
		m.scroll++
	case "p":
		if m.scroll > 0 {
			m.scroll--
		}
	case "j", "k", "down", "up":
		data := det.filteredData()
		if len(data) == 0 {
			return nil
		}
		innerH := logAreaHeight(a, det, s)
		start, end := visibleLogWindow(det, data, innerH, a.display.DateFormat)
		visibleCount := end - start
		idx := start + det.logCursor
		if key == "j" || key == "down" {
			if idx+1 >= len(data) {
				return nil
			}
			idx++
			if det.logCursor < visibleCount-1 {
				det.logCursor++
			} else if det.logScroll > 0 {
				det.logScroll--
			}
		} else {
			if idx <= 0 {
				return nil
			}
			idx--
			maxScroll := len(data) - innerH
			if maxScroll < 0 {
				maxScroll = 0
			}
			if det.logCursor > 0 {
				det.logCursor--
			} else if det.logScroll < maxScroll {
				det.logScroll++
			}
		}
		m.entry = data[idx]
		m.scroll = 0
	}
	return nil
}
