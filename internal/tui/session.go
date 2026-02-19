package tui

import (
	"context"

	"github.com/thobiasn/tori-cli/internal/protocol"
)

// ConnState tracks the server connection status.
type ConnState int

const (
	ConnNone       ConnState = iota // not connected yet
	ConnConnecting                  // connection in progress
	ConnSSH                         // SSH tunnel up, agent unreachable
	ConnReady                       // fully connected
	ConnError                       // connection failure
)

// Session holds all per-server state: connection and accumulated data.
type Session struct {
	Name          string
	Client        *Client
	Tunnel        *Tunnel
	ConnState     ConnState
	ConnMsg       string
	Config        ServerConfig
	connectCancel context.CancelFunc

	// Accumulated live data.
	Host       *protocol.HostMetrics
	Disks      []protocol.DiskMetrics
	Containers []protocol.ContainerMetrics
	ContInfo   []protocol.ContainerInfo
	Alerts     map[int64]*protocol.AlertEvent

	// Version info from hello handshake.
	AgentVersion   string
	VersionWarning string

	// Alert rules.
	RuleCount int // number of configured alert rules

	// History for dashboard graphs.
	HostCPUHist     *RingBuffer[float64]
	HostMemHist     *RingBuffer[float64]
	Rates           *RateCalc
	BackfillPending bool   // true while a backfill query is in-flight
	BackfillGen     uint64 // incremented on each window change; stale responses are discarded
	RetentionDays   int  // reported by agent, limits zoom range

	// Detail view state.
	Detail DetailState

	// Alerts view state.
	AlertsView AlertsState

	Err error
}

// histBufSize is the number of data points in each ring buffer.
// 600 points covers ~100 minutes at 10s intervals, and serves as the
// downsampling target for historical queries.
const histBufSize = 600

// NewSession creates a session with initialized buffers.
func NewSession(name string, client *Client, tunnel *Tunnel) *Session {
	return &Session{
		Name:        name,
		Client:      client,
		Tunnel:      tunnel,
		Alerts:      make(map[int64]*protocol.AlertEvent),
		HostCPUHist: NewRingBuffer[float64](histBufSize),
		HostMemHist: NewRingBuffer[float64](histBufSize),
		Rates:       NewRateCalc(),
	}
}
