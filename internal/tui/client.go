package tui

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/thobiasn/rook/internal/protocol"
)

// Tea message types dispatched by the reader goroutine.
type MetricsMsg struct{ *protocol.MetricsUpdate }
type LogMsg struct{ protocol.LogEntryMsg }
type AlertEventMsg struct{ protocol.AlertEvent }
type ContainerEventMsg struct{ protocol.ContainerEvent }
type ConnErrMsg struct{ Err error }

// Client wraps a protocol connection to the agent and dispatches
// streaming messages as tea.Msg values.
type Client struct {
	conn     net.Conn
	mu       sync.Mutex // serializes writes
	nextID   atomic.Uint32
	pendMu   sync.Mutex
	pending  map[uint32]chan *protocol.Envelope
	prog     *tea.Program
	done     chan struct{} // closed when readLoop exits
	started  sync.Once    // ensures readLoop starts exactly once
	closed   atomic.Bool  // set by Close to suppress spurious ConnErrMsg
}

// NewClient wraps an existing connection. Call SetProgram to start reading.
func NewClient(conn net.Conn) *Client {
	return &Client{
		conn:    conn,
		pending: make(map[uint32]chan *protocol.Envelope),
		done:    make(chan struct{}),
	}
}

// SetProgram sets the tea.Program for streaming dispatch and starts readLoop.
// Safe to call multiple times; only the first call starts the reader goroutine.
func (c *Client) SetProgram(p *tea.Program) {
	c.prog = p
	c.started.Do(func() { go c.readLoop() })
}

// Close closes the underlying connection. The readLoop will exit without
// sending a ConnErrMsg.
func (c *Client) Close() error {
	c.closed.Store(true)
	return c.conn.Close()
}

func (c *Client) readLoop() {
	defer func() {
		close(c.done)
		c.pendMu.Lock()
		for id, ch := range c.pending {
			close(ch)
			delete(c.pending, id)
		}
		c.pendMu.Unlock()
		// Only notify the TUI on unexpected disconnects, not deliberate Close().
		if c.prog != nil && !c.closed.Load() {
			c.prog.Send(ConnErrMsg{Err: errors.New("connection lost")})
		}
	}()

	for {
		env, err := protocol.ReadMsg(c.conn)
		if err != nil {
			return
		}
		if env.ID > 0 {
			c.pendMu.Lock()
			ch, ok := c.pending[env.ID]
			c.pendMu.Unlock()
			if ok {
				ch <- env
			}
			continue
		}
		// Streaming message (ID == 0): dispatch as tea.Msg.
		c.dispatchStreaming(env)
	}
}

func (c *Client) dispatchStreaming(env *protocol.Envelope) {
	switch env.Type {
	case protocol.TypeMetricsUpdate:
		var m protocol.MetricsUpdate
		if err := protocol.DecodeBody(env.Body, &m); err == nil {
			c.prog.Send(MetricsMsg{&m})
		}
	case protocol.TypeLogEntry:
		var m protocol.LogEntryMsg
		if err := protocol.DecodeBody(env.Body, &m); err == nil {
			c.prog.Send(LogMsg{m})
		}
	case protocol.TypeAlertEvent:
		var m protocol.AlertEvent
		if err := protocol.DecodeBody(env.Body, &m); err == nil {
			c.prog.Send(AlertEventMsg{m})
		}
	case protocol.TypeContainerEvent:
		var m protocol.ContainerEvent
		if err := protocol.DecodeBody(env.Body, &m); err == nil {
			c.prog.Send(ContainerEventMsg{m})
		}
	}
}

// Request sends a request and blocks until the response arrives, ctx cancels,
// or the connection dies.
func (c *Client) Request(ctx context.Context, typ protocol.MsgType, body any) (*protocol.Envelope, error) {
	id := c.nextID.Add(1)

	var env *protocol.Envelope
	var err error
	if body != nil {
		env, err = protocol.NewEnvelope(typ, id, body)
	} else {
		env = protocol.NewEnvelopeNoBody(typ, id)
	}
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	ch := make(chan *protocol.Envelope, 1)
	c.pendMu.Lock()
	c.pending[id] = ch
	c.pendMu.Unlock()

	defer func() {
		c.pendMu.Lock()
		delete(c.pending, id)
		c.pendMu.Unlock()
	}()

	c.mu.Lock()
	err = protocol.WriteMsg(c.conn, env)
	c.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, errors.New("connection closed")
		}
		if resp.Type == protocol.TypeError {
			var e protocol.ErrorResult
			if err := protocol.DecodeBody(resp.Body, &e); err == nil {
				msg := e.Error
				if len(msg) > 256 {
					msg = msg[:256]
				}
				return nil, errors.New(msg)
			}
			return nil, errors.New("unknown error from agent")
		}
		return resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, errors.New("connection closed")
	}
}

// Subscribe sends a streaming subscription (ID=0).
func (c *Client) Subscribe(typ protocol.MsgType, body any) error {
	var env *protocol.Envelope
	var err error
	if body != nil {
		env, err = protocol.NewEnvelope(typ, 0, body)
	} else {
		env = protocol.NewEnvelopeNoBody(typ, 0)
	}
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return protocol.WriteMsg(c.conn, env)
}

// Unsubscribe removes a streaming subscription.
func (c *Client) Unsubscribe(topic string) error {
	return c.Subscribe(protocol.TypeUnsubscribe, &protocol.Unsubscribe{Topic: topic})
}

// QueryContainers returns the list of containers from the agent.
func (c *Client) QueryContainers(ctx context.Context) ([]protocol.ContainerInfo, error) {
	resp, err := c.Request(ctx, protocol.TypeQueryContainers, nil)
	if err != nil {
		return nil, err
	}
	var r protocol.QueryContainersResp
	if err := protocol.DecodeBody(resp.Body, &r); err != nil {
		return nil, err
	}
	return r.Containers, nil
}

// QueryMetrics returns historical host/container metrics.
func (c *Client) QueryMetrics(ctx context.Context, start, end int64) (*protocol.QueryMetricsResp, error) {
	resp, err := c.Request(ctx, protocol.TypeQueryMetrics, &protocol.QueryMetricsReq{Start: start, End: end})
	if err != nil {
		return nil, err
	}
	var r protocol.QueryMetricsResp
	if err := protocol.DecodeBody(resp.Body, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// QueryLogs returns historical log entries.
func (c *Client) QueryLogs(ctx context.Context, req *protocol.QueryLogsReq) ([]protocol.LogEntryMsg, error) {
	resp, err := c.Request(ctx, protocol.TypeQueryLogs, req)
	if err != nil {
		return nil, err
	}
	var r protocol.QueryLogsResp
	if err := protocol.DecodeBody(resp.Body, &r); err != nil {
		return nil, err
	}
	return r.Entries, nil
}

// QueryAlerts returns historical alerts.
func (c *Client) QueryAlerts(ctx context.Context, start, end int64) ([]protocol.AlertMsg, error) {
	resp, err := c.Request(ctx, protocol.TypeQueryAlerts, &protocol.QueryAlertsReq{Start: start, End: end})
	if err != nil {
		return nil, err
	}
	var r protocol.QueryAlertsResp
	if err := protocol.DecodeBody(resp.Body, &r); err != nil {
		return nil, err
	}
	return r.Alerts, nil
}

// AckAlert acknowledges an alert by ID.
func (c *Client) AckAlert(ctx context.Context, alertID int64) error {
	_, err := c.Request(ctx, protocol.TypeActionAckAlert, &protocol.AckAlertReq{AlertID: alertID})
	return err
}

// SilenceAlert silences a rule for the given duration (seconds).
func (c *Client) SilenceAlert(ctx context.Context, rule string, dur int64) error {
	_, err := c.Request(ctx, protocol.TypeActionSilence, &protocol.SilenceAlertReq{RuleName: rule, Duration: dur})
	return err
}

// RestartContainer asks the agent to restart a container.
func (c *Client) RestartContainer(ctx context.Context, containerID string) error {
	_, err := c.Request(ctx, protocol.TypeActionRestart, &protocol.RestartContainerReq{ContainerID: containerID})
	return err
}
