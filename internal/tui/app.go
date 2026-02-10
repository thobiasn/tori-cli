package tui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/thobiasn/rook/internal/protocol"
)

type view int

const (
	viewDashboard view = iota
	viewLogs
	viewAlerts
	viewDetail
)

// App is the root Bubbletea model.
type App struct {
	client *Client
	width  int
	height int
	active view
	theme  Theme
	err    error

	// Live data placeholders — populated by streaming, consumed by views in M4c.
	metricsCount int
	logCount     int
	alertCount   int
	containers   []protocol.ContainerInfo
}

// NewApp creates the root model.
func NewApp(client *Client) App {
	return App{
		client: client,
		theme:  DefaultTheme(),
	}
}

// subscribeAll subscribes to all streaming topics and queries containers.
func subscribeAll(c *Client) tea.Cmd {
	return func() tea.Msg {
		if err := c.Subscribe(protocol.TypeSubscribeMetrics, nil); err != nil {
			return ConnErrMsg{Err: fmt.Errorf("subscribe metrics: %w", err)}
		}
		if err := c.Subscribe(protocol.TypeSubscribeLogs, nil); err != nil {
			return ConnErrMsg{Err: fmt.Errorf("subscribe logs: %w", err)}
		}
		if err := c.Subscribe(protocol.TypeSubscribeAlerts, nil); err != nil {
			return ConnErrMsg{Err: fmt.Errorf("subscribe alerts: %w", err)}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		containers, err := c.QueryContainers(ctx)
		if err != nil {
			return ConnErrMsg{Err: fmt.Errorf("query containers: %w", err)}
		}
		return containersMsg(containers)
	}
}

type containersMsg []protocol.ContainerInfo

func (a App) Init() tea.Cmd {
	return subscribeAll(a.client)
}

func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return a, tea.Quit
		}

	case ConnErrMsg:
		a.err = msg.Err
		return a, tea.Quit

	case containersMsg:
		a.containers = []protocol.ContainerInfo(msg)

	case MetricsMsg:
		a.metricsCount++

	case LogMsg:
		a.logCount++

	case AlertEventMsg:
		a.alertCount++
	}
	return a, nil
}

func (a App) View() string {
	if a.err != nil {
		return fmt.Sprintf("Error: %v\n", a.err)
	}
	return fmt.Sprintf(
		"Connected — %d containers, %d metrics updates, %d log entries, %d alert events\n",
		len(a.containers), a.metricsCount, a.logCount, a.alertCount,
	)
}
