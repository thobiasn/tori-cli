package protocol

import "github.com/vmihailenco/msgpack/v5"

// MsgType identifies the type of a protocol message.
type MsgType string

const (
	// Streaming: client subscribes, agent pushes.
	TypeSubscribeMetrics MsgType = "subscribe:metrics"
	TypeSubscribeLogs    MsgType = "subscribe:logs"
	TypeSubscribeAlerts  MsgType = "subscribe:alerts"
	TypeUnsubscribe      MsgType = "unsubscribe"
	TypeMetricsUpdate    MsgType = "metrics:update"
	TypeLogEntry         MsgType = "log:entry"
	TypeAlertEvent       MsgType = "alert:event"

	// Request-response.
	TypeQueryMetrics       MsgType = "query:metrics"
	TypeQueryLogs          MsgType = "query:logs"
	TypeQueryAlerts        MsgType = "query:alerts"
	TypeQueryContainers    MsgType = "query:containers"
	TypeActionAckAlert     MsgType = "action:ack_alert"
	TypeActionSilence      MsgType = "action:silence_alert"
	TypeActionRestart      MsgType = "action:restart_container"
	TypeActionSetTracking  MsgType = "action:set_tracking"
	TypeResult             MsgType = "result"
	TypeError              MsgType = "error"
)

// Envelope is the top-level wire message. Body is decoded in a second pass
// based on the Type field.
type Envelope struct {
	Type MsgType            `msgpack:"type"`
	ID   uint32             `msgpack:"id"`
	Body msgpack.RawMessage `msgpack:"body"`
}

// --- Streaming messages ---

// SubscribeLogs is the body for TypeSubscribeLogs.
type SubscribeLogs struct {
	ContainerID string `msgpack:"container_id,omitempty"`
	Project     string `msgpack:"project,omitempty"`
	Stream      string `msgpack:"stream,omitempty"`
	Search      string `msgpack:"search,omitempty"`
}

// Unsubscribe is the body for TypeUnsubscribe.
type Unsubscribe struct {
	Topic string `msgpack:"topic"`
}

// MetricsUpdate is pushed every collect cycle.
type MetricsUpdate struct {
	Timestamp  int64              `msgpack:"timestamp"`
	Host       *HostMetrics       `msgpack:"host,omitempty"`
	Disks      []DiskMetrics      `msgpack:"disks,omitempty"`
	Networks   []NetMetrics       `msgpack:"networks,omitempty"`
	Containers []ContainerMetrics `msgpack:"containers,omitempty"`
}

// LogEntryMsg is pushed per matching log line.
type LogEntryMsg struct {
	Timestamp     int64  `msgpack:"timestamp"`
	ContainerID   string `msgpack:"container_id"`
	ContainerName string `msgpack:"container_name"`
	Stream        string `msgpack:"stream"`
	Message       string `msgpack:"message"`
}

// AlertEvent is pushed on alert state transitions.
type AlertEvent struct {
	ID          int64  `msgpack:"id"`
	RuleName    string `msgpack:"rule_name"`
	Severity    string `msgpack:"severity"`
	Condition   string `msgpack:"condition"`
	InstanceKey string `msgpack:"instance_key"`
	FiredAt     int64  `msgpack:"fired_at"`
	ResolvedAt  int64  `msgpack:"resolved_at,omitempty"`
	Message     string `msgpack:"message"`
	State       string `msgpack:"state"` // "firing" or "resolved"
}

// --- Request-response messages ---

// QueryMetricsReq is the body for TypeQueryMetrics.
type QueryMetricsReq struct {
	Start int64 `msgpack:"start"`
	End   int64 `msgpack:"end"`
}

// QueryMetricsResp is the response for TypeQueryMetrics.
type QueryMetricsResp struct {
	Host       []TimedHostMetrics      `msgpack:"host"`
	Disks      []TimedDiskMetrics      `msgpack:"disks"`
	Networks   []TimedNetMetrics       `msgpack:"networks"`
	Containers []TimedContainerMetrics `msgpack:"containers"`
}

// QueryLogsReq is the body for TypeQueryLogs.
type QueryLogsReq struct {
	Start       int64  `msgpack:"start"`
	End         int64  `msgpack:"end"`
	ContainerID string `msgpack:"container_id,omitempty"`
	Stream      string `msgpack:"stream,omitempty"`
	Search      string `msgpack:"search,omitempty"`
	Limit       int    `msgpack:"limit,omitempty"`
}

// QueryLogsResp is the response for TypeQueryLogs.
type QueryLogsResp struct {
	Entries []LogEntryMsg `msgpack:"entries"`
}

// QueryAlertsReq is the body for TypeQueryAlerts.
type QueryAlertsReq struct {
	Start int64 `msgpack:"start"`
	End   int64 `msgpack:"end"`
}

// QueryAlertsResp is the response for TypeQueryAlerts.
type QueryAlertsResp struct {
	Alerts []AlertMsg `msgpack:"alerts"`
}

// AlertMsg represents an alert in query responses.
type AlertMsg struct {
	ID           int64  `msgpack:"id"`
	RuleName     string `msgpack:"rule_name"`
	Severity     string `msgpack:"severity"`
	Condition    string `msgpack:"condition"`
	InstanceKey  string `msgpack:"instance_key"`
	FiredAt      int64  `msgpack:"fired_at"`
	ResolvedAt   int64  `msgpack:"resolved_at,omitempty"`
	Message      string `msgpack:"message"`
	Acknowledged bool   `msgpack:"acknowledged"`
}

// QueryContainersResp is the response for TypeQueryContainers.
type QueryContainersResp struct {
	Containers []ContainerInfo `msgpack:"containers"`
}

// ContainerInfo describes a running container.
type ContainerInfo struct {
	ID      string `msgpack:"id"`
	Name    string `msgpack:"name"`
	Image   string `msgpack:"image"`
	State   string `msgpack:"state"`
	Project string `msgpack:"project,omitempty"`
}

// AckAlertReq is the body for TypeActionAckAlert.
type AckAlertReq struct {
	AlertID int64 `msgpack:"alert_id"`
}

// SilenceAlertReq is the body for TypeActionSilence.
type SilenceAlertReq struct {
	RuleName string `msgpack:"rule_name"`
	Duration int64  `msgpack:"duration"` // seconds
}

// RestartContainerReq is the body for TypeActionRestart.
type RestartContainerReq struct {
	ContainerID string `msgpack:"container_id"`
}

// Result is the generic success response.
type Result struct {
	OK      bool   `msgpack:"ok"`
	Message string `msgpack:"message,omitempty"`
}

// ErrorResult is the generic error response.
type ErrorResult struct {
	Error string `msgpack:"error"`
}

// --- Protocol-local metric types (mirrors agent types, no import dependency) ---

type HostMetrics struct {
	CPUPercent float64 `msgpack:"cpu_percent"`
	MemTotal   uint64  `msgpack:"mem_total"`
	MemUsed    uint64  `msgpack:"mem_used"`
	MemPercent float64 `msgpack:"mem_percent"`
	SwapTotal  uint64  `msgpack:"swap_total"`
	SwapUsed   uint64  `msgpack:"swap_used"`
	Load1      float64 `msgpack:"load1"`
	Load5      float64 `msgpack:"load5"`
	Load15     float64 `msgpack:"load15"`
	Uptime     float64 `msgpack:"uptime"`
}

type DiskMetrics struct {
	Mountpoint string  `msgpack:"mountpoint"`
	Device     string  `msgpack:"device"`
	Total      uint64  `msgpack:"total"`
	Used       uint64  `msgpack:"used"`
	Free       uint64  `msgpack:"free"`
	Percent    float64 `msgpack:"percent"`
}

type NetMetrics struct {
	Iface     string `msgpack:"iface"`
	RxBytes   uint64 `msgpack:"rx_bytes"`
	TxBytes   uint64 `msgpack:"tx_bytes"`
	RxPackets uint64 `msgpack:"rx_packets"`
	TxPackets uint64 `msgpack:"tx_packets"`
	RxErrors  uint64 `msgpack:"rx_errors"`
	TxErrors  uint64 `msgpack:"tx_errors"`
}

type ContainerMetrics struct {
	ID         string  `msgpack:"id"`
	Name       string  `msgpack:"name"`
	Image      string  `msgpack:"image"`
	State      string  `msgpack:"state"`
	CPUPercent float64 `msgpack:"cpu_percent"`
	MemUsage   uint64  `msgpack:"mem_usage"`
	MemLimit   uint64  `msgpack:"mem_limit"`
	MemPercent float64 `msgpack:"mem_percent"`
	NetRx      uint64  `msgpack:"net_rx"`
	NetTx      uint64  `msgpack:"net_tx"`
	BlockRead  uint64  `msgpack:"block_read"`
	BlockWrite uint64  `msgpack:"block_write"`
	PIDs       uint64  `msgpack:"pids"`
}

type TimedHostMetrics struct {
	Timestamp int64 `msgpack:"timestamp"`
	HostMetrics
}

type TimedDiskMetrics struct {
	Timestamp int64 `msgpack:"timestamp"`
	DiskMetrics
}

type TimedNetMetrics struct {
	Timestamp int64 `msgpack:"timestamp"`
	NetMetrics
}

type TimedContainerMetrics struct {
	Timestamp int64 `msgpack:"timestamp"`
	ContainerMetrics
}
