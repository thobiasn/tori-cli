package tui

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/thobiasn/tori-cli/internal/protocol"
)

// backfillMetrics fetches historical host metrics to populate graphs.
// seconds=0 uses the default live backfill (last ~100 minutes).
// seconds>0 requests server-side downsampling to histBufSize points.
func backfillMetrics(c *Client, seconds int64, gen uint64) tea.Cmd {
	return func() tea.Msg {
		timeout := 5 * time.Second
		hist := seconds > 0
		if hist {
			timeout = 15 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		now := time.Now().Unix()
		rangeSec := int64(histBufSize * 10) // 600 points * 10s default interval
		points := 0
		if hist {
			rangeSec = seconds
			points = histBufSize
		}
		start := now - rangeSec

		resp, err := c.QueryMetrics(ctx, &protocol.QueryMetricsReq{
			Start:  start,
			End:    now,
			Points: points,
		})
		if err != nil {
			return nil // Non-critical: streaming will fill graphs.
		}
		return metricsBackfillMsg{
			server:    c.server,
			resp:      resp,
			rangeHist: hist,
			gen:       gen,
		}
	}
}

// handleMetricsBackfill populates host ring buffers from historical data.
func handleMetricsBackfill(s *Session, resp *protocol.QueryMetricsResp, rangeHist bool) {
	if rangeHist {
		cpuBuf := NewRingBuffer[float64](histBufSize)
		memBuf := NewRingBuffer[float64](histBufSize)
		for _, h := range resp.Host {
			cpuBuf.Push(h.CPUPercent)
			memBuf.Push(h.MemPercent)
		}
		s.HostCPUHist = cpuBuf
		s.HostMemHist = memBuf
	} else {
		for _, h := range resp.Host {
			s.HostCPUHist.Push(h.CPUPercent)
			s.HostMemHist.Push(h.MemPercent)
		}
	}
}

func connectServerCmd(name string, cfg ServerConfig, appctx *appCtx, ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		remoteSock := cfg.Socket
		if remoteSock == "" {
			remoteSock = "/run/tori/tori.sock"
		}

		var tunnel *Tunnel
		var sockPath string

		if cfg.Host != "" {
			promptFn := func(prompt string) (string, error) {
				ch := make(chan string, 1)
				if appctx.prog == nil {
					return "", errors.New("no program")
				}
				appctx.prog.Send(sshPromptMsg{
					server:  name,
					prompt:  prompt,
					respond: ch,
				})
				resp, ok := <-ch
				if !ok {
					return "", errors.New("cancelled")
				}
				return resp, nil
			}

			var err error
			tunnel, err = NewTunnelAskpass(cfg.Host, remoteSock, promptFn, SSHOptions{
				Port:         cfg.Port,
				IdentityFile: cfg.IdentityFile,
			})
			if err != nil {
				return connectDoneMsg{server: name, err: fmt.Errorf("tunnel: %w", err)}
			}

			if err := tunnel.WaitReady(ctx); err != nil {
				tunnel.Close()
				return connectDoneMsg{server: name, err: fmt.Errorf("tunnel: %w", err)}
			}
			sockPath = tunnel.LocalSocket()
		} else {
			sockPath = remoteSock
		}

		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
		if err != nil {
			if tunnel != nil {
				tunnel.Close()
			}
			return connectDoneMsg{server: name, err: fmt.Errorf("dial: %w", err)}
		}

		client := NewClient(conn, name)
		return connectDoneMsg{server: name, client: client, tunnel: tunnel}
	}
}

func (a *App) handleConnectDone(msg connectDoneMsg) (App, tea.Cmd) {
	s := a.sessions[msg.server]
	if s == nil {
		return *a, nil
	}

	if a.connecting == msg.server {
		a.connecting = ""
	}

	if s.connectCancel != nil {
		s.connectCancel()
		s.connectCancel = nil
	}

	if msg.err != nil {
		s.ConnState = ConnNone
		s.ConnMsg = ""
		a.connError = msg.err.Error()
		a.switcher = true
		return *a, a.processAutoConnectQueue()
	}

	s.Client = msg.client
	s.Tunnel = msg.tunnel
	s.ConnState = ConnReady
	s.ConnMsg = "connected"
	s.Err = nil
	if a.switcher && a.activeSession == msg.server {
		a.switcher = false
	}

	if a.ctx.prog != nil {
		s.Client.SetProgram(a.ctx.prog)
	}

	s.BackfillPending = true

	var cmds []tea.Cmd
	cmds = append(cmds, subscribeAll(s.Client, a.windowSeconds(), s.BackfillGen))

	if cmd := a.processAutoConnectQueue(); cmd != nil {
		cmds = append(cmds, cmd)
	}

	return *a, tea.Batch(cmds...)
}

func (a App) handleSSHPromptInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	p := a.sshPrompt

	switch {
	case key == "enter":
		if p.hostKey {
			select {
			case p.respond <- "yes":
			default:
			}
		} else {
			select {
			case p.respond <- string(p.input):
			default:
			}
		}
		a.sshPrompt = nil
		return a, nil

	case key == "esc", key == "ctrl+c":
		close(p.respond)
		a.sshPrompt = nil
		return a, nil

	case key == "backspace":
		if len(p.input) > 0 && !p.hostKey {
			p.input = p.input[:len(p.input)-1]
		}
		return a, nil

	default:
		if p.hostKey {
			if key == "y" || key == "Y" {
				select {
				case p.respond <- "yes":
				default:
				}
				a.sshPrompt = nil
				return a, nil
			}
			if key == "n" || key == "N" {
				close(p.respond)
				a.sshPrompt = nil
				return a, nil
			}
			return a, nil
		}

		if len(key) == 1 && len(p.input) < 256 {
			p.input = append(p.input, rune(key[0]))
		}
		return a, nil
	}
}

func isPasswordPrompt(prompt string) bool {
	lower := strings.ToLower(prompt)
	return strings.Contains(lower, "passphrase") ||
		strings.Contains(lower, "password")
}

func isHostKeyPrompt(prompt string) bool {
	lower := strings.ToLower(prompt)
	return strings.Contains(lower, "authenticity") ||
		strings.Contains(lower, "fingerprint") ||
		strings.Contains(lower, "yes/no")
}

func renderConnErrorModal(a *App, width, height int) string {
	theme := &a.theme
	muted := mutedStyle(theme)
	titleStyle := lipgloss.NewStyle().Foreground(theme.Critical).Bold(true)

	modalW := 60
	innerW := modalW - 6

	// Sanitize: strip carriage returns and tabs from SSH stderr output.
	errText := strings.NewReplacer("\r", "", "\t", "  ").Replace(a.connError)

	var lines []string
	for _, chunk := range wrapText(errText, innerW) {
		lines = append(lines, muted.Render(Truncate(chunk, innerW)))
	}

	return (dialogLayout{
		title:      "Connection Error",
		titleStyle: &titleStyle,
		width:      modalW,
		lines:      lines,
		tips:       dialogTips(theme, "any key", "dismiss"),
	}).render(width, height, theme)
}

func (a *App) renderSSHPromptModal(width, height int) string {
	p := a.sshPrompt
	theme := &a.theme
	muted := mutedStyle(theme)

	modalW := 72
	innerW := modalW - 2

	var lines []string
	lines = append(lines, muted.Render(Truncate(p.server, innerW-4)))
	lines = append(lines, "")

	for _, chunk := range wrapText(p.prompt, innerW-4) {
		lines = append(lines, chunk)
	}

	var tips string
	if p.hostKey {
		tips = dialogTips(theme, "y", "accept", "n", "reject")
	} else {
		lines = append(lines, "")

		display := string(p.input)
		if p.masked {
			display = strings.Repeat("*", len(p.input))
		}
		cursor := lipgloss.NewStyle().Foreground(theme.Accent).Render("█")
		prompt := muted.Render("> ")
		promptW := lipgloss.Width(prompt)
		fieldW := innerW - promptW - 4
		if len(display) > fieldW {
			display = display[len(display)-fieldW:]
		}
		lines = append(lines, prompt+display+cursor)

		tips = dialogTips(theme, "enter", "submit", "esc", "cancel")
	}

	return (dialogLayout{
		title: "SSH",
		width: modalW,
		lines: lines,
		tips:  tips,
	}).render(width, height, theme)
}

func (a *App) toggleTracking() tea.Cmd {
	s := a.session()
	if s == nil || s.Client == nil {
		return nil
	}

	items := buildSelectableItems(a.groups, a.collapsed)
	if a.cursor < 0 || a.cursor >= len(items) {
		return nil
	}

	item := items[a.cursor]
	group := a.groups[item.groupIdx].name

	if item.isProject {
		// Toggle entire project.
		tracked := isProjectTracked(group, s.ContInfo)
		client := s.Client
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client.SetTracking(ctx, "", group, !tracked)
			return trackingDoneMsg{server: client.server}
		}
	}

	// Single container.
	c := containerAtCursor(a.groups, items, a.cursor)
	if c == nil {
		return nil
	}
	name := containerNameByID(c.ID, s.ContInfo)
	tracked := isContainerTracked(c.ID, s.ContInfo)
	client := s.Client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		client.SetTracking(ctx, name, "", !tracked)
		return trackingDoneMsg{server: client.server}
	}
}

func isProjectTracked(project string, contInfo []protocol.ContainerInfo) bool {
	for _, ci := range contInfo {
		if ci.Project == project && ci.Tracked {
			return true
		}
	}
	return false
}

func isContainerTracked(id string, contInfo []protocol.ContainerInfo) bool {
	for _, ci := range contInfo {
		if ci.ID == id {
			return ci.Tracked
		}
	}
	return false
}

func containerNameByID(id string, contInfo []protocol.ContainerInfo) string {
	for _, ci := range contInfo {
		if ci.ID == id {
			return ci.Name
		}
	}
	return ""
}
