package tui

import "github.com/thobiasn/rook/internal/protocol"

// Session holds all per-server state: connection, accumulated data, and view state.
type Session struct {
	Name   string
	Client *Client
	Tunnel *Tunnel // nil for local connections

	// Accumulated live data.
	Host       *protocol.HostMetrics
	Disks      []protocol.DiskMetrics
	Containers []protocol.ContainerMetrics
	ContInfo   []protocol.ContainerInfo
	Alerts     map[int64]*protocol.AlertEvent

	// History buffers.
	Rates          *RateCalc
	CPUHistory     map[string]*RingBuffer[float64]
	MemHistory     map[string]*RingBuffer[float64]
	HostCPUHistory      *RingBuffer[float64]
	HostMemHistory      *RingBuffer[float64]
	HostMemUsedHistory *RingBuffer[float64]

	// Per-session view state.
	Dash   DashboardState
	Alertv AlertViewState
	Detail DetailState

	Err error
}

// NewSession creates a session with initialized buffers.
func NewSession(name string, client *Client, tunnel *Tunnel) *Session {
	return &Session{
		Name:                 name,
		Client:               client,
		Tunnel:               tunnel,
		Alerts:               make(map[int64]*protocol.AlertEvent),
		Rates:                NewRateCalc(),
		CPUHistory:           make(map[string]*RingBuffer[float64]),
		MemHistory:           make(map[string]*RingBuffer[float64]),
		HostCPUHistory:       NewRingBuffer[float64](180),
		HostMemHistory:       NewRingBuffer[float64](180),
		HostMemUsedHistory: NewRingBuffer[float64](180),
		Dash:                 newDashboardState(),
		Alertv:               newAlertViewState(),
	}
}
