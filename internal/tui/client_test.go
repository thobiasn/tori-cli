package tui

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/thobiasn/rook/internal/protocol"
)

// msgCollector implements tea.Model and collects messages sent via prog.Send.
type msgCollector struct {
	mu   sync.Mutex
	msgs []tea.Msg
	done chan struct{}
	want int
}

func newCollector(want int) *msgCollector {
	return &msgCollector{done: make(chan struct{}), want: want}
}

func (m *msgCollector) Init() tea.Cmd { return nil }
func (m *msgCollector) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs = append(m.msgs, msg)
	if m.want > 0 && len(m.msgs) >= m.want {
		select {
		case <-m.done:
		default:
			close(m.done)
		}
		return m, tea.Quit
	}
	return m, nil
}
func (m *msgCollector) View() string { return "" }

func (m *msgCollector) waitFor(t *testing.T, timeout time.Duration) []tea.Msg {
	t.Helper()
	select {
	case <-m.done:
	case <-time.After(timeout):
		m.mu.Lock()
		n := len(m.msgs)
		m.mu.Unlock()
		t.Fatalf("timed out waiting for %d messages, got %d", m.want, n)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.msgs
}

// mockServer reads envelopes from conn and sends back responses.
// respFn is called for each received envelope; it returns the response to send.
func mockServer(t *testing.T, conn net.Conn, respFn func(*protocol.Envelope) *protocol.Envelope) {
	t.Helper()
	for {
		env, err := protocol.ReadMsg(conn)
		if err != nil {
			return
		}
		resp := respFn(env)
		if resp != nil {
			if err := protocol.WriteMsg(conn, resp); err != nil {
				return
			}
		}
	}
}

func TestRequestResponse(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	c := NewClient(clientConn)
	defer c.Close()

	coll := newCollector(0)
	p := tea.NewProgram(coll, tea.WithoutRenderer(), tea.WithInput(nil))
	c.SetProgram(p)

	// Server echoes back a result with the same ID.
	go mockServer(t, serverConn, func(env *protocol.Envelope) *protocol.Envelope {
		resp, _ := protocol.NewEnvelope(protocol.TypeResult, env.ID, &protocol.QueryContainersResp{
			Containers: []protocol.ContainerInfo{{ID: "abc", Name: "test"}},
		})
		return resp
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	containers, err := c.QueryContainers(ctx)
	if err != nil {
		t.Fatalf("QueryContainers: %v", err)
	}
	if len(containers) != 1 || containers[0].ID != "abc" {
		t.Fatalf("unexpected containers: %v", containers)
	}
}

func TestConcurrentRequests(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	c := NewClient(clientConn)
	defer c.Close()

	coll := newCollector(0)
	p := tea.NewProgram(coll, tea.WithoutRenderer(), tea.WithInput(nil))
	c.SetProgram(p)

	// Server responds with a message containing the request ID in the container name,
	// so each caller can verify it got the right response.
	go mockServer(t, serverConn, func(env *protocol.Envelope) *protocol.Envelope {
		name := ""
		switch env.ID {
		case 1:
			name = "first"
		case 2:
			name = "second"
		case 3:
			name = "third"
		}
		resp, _ := protocol.NewEnvelope(protocol.TypeResult, env.ID, &protocol.QueryContainersResp{
			Containers: []protocol.ContainerInfo{{ID: "c", Name: name}},
		})
		return resp
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	results := make([]string, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			containers, err := c.QueryContainers(ctx)
			if err != nil {
				t.Errorf("request %d: %v", idx, err)
				return
			}
			if len(containers) > 0 {
				results[idx] = containers[0].Name
			}
		}(i)
	}
	wg.Wait()

	// Each goroutine should have gotten a response (names may vary by scheduling).
	for i, r := range results {
		if r == "" {
			t.Errorf("request %d got empty response", i)
		}
	}
}

func TestStreamingDispatch(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	c := NewClient(clientConn)
	defer c.Close()

	coll := newCollector(3)
	p := tea.NewProgram(coll, tea.WithoutRenderer(), tea.WithInput(nil))
	c.SetProgram(p)

	// Push three streaming messages from server.
	go func() {
		metricsEnv, _ := protocol.NewEnvelope(protocol.TypeMetricsUpdate, 0, &protocol.MetricsUpdate{
			Timestamp: 100,
			Host:      &protocol.HostMetrics{CPUPercent: 42.0},
		})
		protocol.WriteMsg(serverConn, metricsEnv)

		logEnv, _ := protocol.NewEnvelope(protocol.TypeLogEntry, 0, &protocol.LogEntryMsg{
			ContainerName: "web",
			Message:       "hello",
		})
		protocol.WriteMsg(serverConn, logEnv)

		alertEnv, _ := protocol.NewEnvelope(protocol.TypeAlertEvent, 0, &protocol.AlertEvent{
			RuleName: "high_cpu",
			State:    "firing",
		})
		protocol.WriteMsg(serverConn, alertEnv)
	}()

	// Run the program so it processes Send() messages.
	go p.Run()

	msgs := coll.waitFor(t, 2*time.Second)

	var gotMetrics, gotLog, gotAlert bool
	for _, msg := range msgs {
		switch m := msg.(type) {
		case MetricsMsg:
			gotMetrics = true
			if m.Host.CPUPercent != 42.0 {
				t.Errorf("unexpected CPU: %v", m.Host.CPUPercent)
			}
		case LogMsg:
			gotLog = true
			if m.Message != "hello" {
				t.Errorf("unexpected log: %v", m.Message)
			}
		case AlertEventMsg:
			gotAlert = true
			if m.RuleName != "high_cpu" {
				t.Errorf("unexpected alert: %v", m.RuleName)
			}
		}
	}
	if !gotMetrics || !gotLog || !gotAlert {
		t.Fatalf("missing messages: metrics=%v log=%v alert=%v", gotMetrics, gotLog, gotAlert)
	}
}

func TestConnectionCloseUnblocksPending(t *testing.T) {
	clientConn, serverConn := net.Pipe()

	c := NewClient(clientConn)

	coll := newCollector(0)
	p := tea.NewProgram(coll, tea.WithoutRenderer(), tea.WithInput(nil))
	c.SetProgram(p)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Server reads the request (proves it was sent), then closes.
	serverReady := make(chan struct{})
	go func() {
		protocol.ReadMsg(serverConn) // wait for the request to arrive
		close(serverReady)
		serverConn.Close()
	}()

	errCh := make(chan error, 1)
	go func() {
		_, err := c.QueryContainers(ctx)
		errCh <- err
	}()

	// Wait for server to receive the request before closing.
	<-serverReady

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error from closed connection")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request not unblocked after connection close")
	}

	clientConn.Close()
}

func TestRequestTimeout(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	c := NewClient(clientConn)
	defer c.Close()

	coll := newCollector(0)
	p := tea.NewProgram(coll, tea.WithoutRenderer(), tea.WithInput(nil))
	c.SetProgram(p)

	// Server reads but never responds.
	go func() {
		for {
			if _, err := protocol.ReadMsg(serverConn); err != nil {
				return
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := c.QueryContainers(ctx)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if ctx.Err() == nil {
		t.Fatal("expected context to be cancelled")
	}
}

func TestRequestOnDeadConnection(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	serverConn.Close()

	c := NewClient(clientConn)
	defer c.Close()

	coll := newCollector(0)
	p := tea.NewProgram(coll, tea.WithoutRenderer(), tea.WithInput(nil))
	c.SetProgram(p)

	// Wait for readLoop to detect the closed connection via done channel.
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
		t.Fatal("readLoop did not exit")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	_, err := c.QueryContainers(ctx)
	if err == nil {
		t.Fatal("expected error on dead connection")
	}
}

func TestErrorResponse(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	c := NewClient(clientConn)
	defer c.Close()

	coll := newCollector(0)
	p := tea.NewProgram(coll, tea.WithoutRenderer(), tea.WithInput(nil))
	c.SetProgram(p)

	go mockServer(t, serverConn, func(env *protocol.Envelope) *protocol.Envelope {
		resp, _ := protocol.NewEnvelope(protocol.TypeError, env.ID, &protocol.ErrorResult{
			Error: "not found",
		})
		return resp
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := c.QueryContainers(ctx)
	if err == nil {
		t.Fatal("expected error response")
	}
	if err.Error() != "not found" {
		t.Fatalf("unexpected error: %v", err)
	}
}
