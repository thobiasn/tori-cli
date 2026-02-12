package protocol

import (
	"bytes"
	"testing"

	"github.com/vmihailenco/msgpack/v5"
)

func TestMetricsUpdateRoundtrip(t *testing.T) {
	orig := MetricsUpdate{
		Timestamp: 1700000000,
		Host: &HostMetrics{
			CPUPercent: 45.5, MemTotal: 16e9, MemUsed: 8e9, MemPercent: 50,
			SwapTotal: 4e9, SwapUsed: 1e9, Load1: 1.5, Load5: 1.2, Load15: 0.9, Uptime: 86400,
		},
		Disks: []DiskMetrics{
			{Mountpoint: "/", Device: "/dev/sda1", Total: 100e9, Used: 50e9, Free: 50e9, Percent: 50},
		},
		Networks: []NetMetrics{
			{Iface: "eth0", RxBytes: 1000, TxBytes: 500, RxPackets: 10, TxPackets: 5},
		},
		Containers: []ContainerMetrics{
			{ID: "abc123", Name: "web", Image: "nginx", State: "running", CPUPercent: 5, MemUsage: 100e6, MemLimit: 512e6, MemPercent: 19.5},
		},
	}

	env, err := NewEnvelope(TypeMetricsUpdate, 0, &orig)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := WriteMsg(&buf, env); err != nil {
		t.Fatal(err)
	}

	got, err := ReadMsg(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != TypeMetricsUpdate {
		t.Fatalf("type = %q, want %q", got.Type, TypeMetricsUpdate)
	}

	var decoded MetricsUpdate
	if err := DecodeBody(got.Body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Timestamp != orig.Timestamp {
		t.Errorf("timestamp = %d, want %d", decoded.Timestamp, orig.Timestamp)
	}
	if decoded.Host.CPUPercent != orig.Host.CPUPercent {
		t.Errorf("host cpu = %f, want %f", decoded.Host.CPUPercent, orig.Host.CPUPercent)
	}
	if len(decoded.Disks) != 1 || decoded.Disks[0].Mountpoint != "/" {
		t.Errorf("disks mismatch: %+v", decoded.Disks)
	}
	if len(decoded.Networks) != 1 || decoded.Networks[0].Iface != "eth0" {
		t.Errorf("networks mismatch: %+v", decoded.Networks)
	}
	if len(decoded.Containers) != 1 || decoded.Containers[0].ID != "abc123" {
		t.Errorf("containers mismatch: %+v", decoded.Containers)
	}
}

func TestLogEntryMsgRoundtrip(t *testing.T) {
	orig := LogEntryMsg{
		Timestamp:     1700000000,
		ContainerID:   "abc123",
		ContainerName: "web",
		Stream:        "stdout",
		Message:       "hello world",
	}

	env, err := NewEnvelope(TypeLogEntry, 0, &orig)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := WriteMsg(&buf, env); err != nil {
		t.Fatal(err)
	}

	got, err := ReadMsg(&buf)
	if err != nil {
		t.Fatal(err)
	}

	var decoded LogEntryMsg
	if err := DecodeBody(got.Body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != orig {
		t.Errorf("got %+v, want %+v", decoded, orig)
	}
}

func TestAlertEventRoundtrip(t *testing.T) {
	orig := AlertEvent{
		ID: 42, RuleName: "high_cpu", Severity: "critical",
		Condition: "host.cpu_percent > 90", InstanceKey: "high_cpu",
		FiredAt: 1700000000, Message: "CPU high", State: "firing",
	}

	env, err := NewEnvelope(TypeAlertEvent, 0, &orig)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := WriteMsg(&buf, env); err != nil {
		t.Fatal(err)
	}

	got, err := ReadMsg(&buf)
	if err != nil {
		t.Fatal(err)
	}

	var decoded AlertEvent
	if err := DecodeBody(got.Body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != orig {
		t.Errorf("got %+v, want %+v", decoded, orig)
	}
}

func TestQueryMessagesRoundtrip(t *testing.T) {
	tests := []struct {
		name string
		typ  MsgType
		body any
	}{
		{"QueryMetricsReq", TypeQueryMetrics, &QueryMetricsReq{Start: 1000, End: 2000}},
		{"QueryLogsReq", TypeQueryLogs, &QueryLogsReq{Start: 1000, End: 2000, ContainerID: "abc", ContainerIDs: []string{"abc", "def"}, Stream: "stdout", Search: "error", Limit: 500}},
		{"QueryAlertsReq", TypeQueryAlerts, &QueryAlertsReq{Start: 1000, End: 2000}},
		{"AckAlertReq", TypeActionAckAlert, &AckAlertReq{AlertID: 42}},
		{"SilenceAlertReq", TypeActionSilence, &SilenceAlertReq{RuleName: "high_cpu", Duration: 3600}},
		{"SubscribeLogs", TypeSubscribeLogs, &SubscribeLogs{ContainerID: "abc", Project: "myapp", Stream: "stderr", Search: "panic"}},
		{"Unsubscribe", TypeUnsubscribe, &Unsubscribe{Topic: "metrics"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := NewEnvelope(tt.typ, 1, tt.body)
			if err != nil {
				t.Fatal(err)
			}

			var buf bytes.Buffer
			if err := WriteMsg(&buf, env); err != nil {
				t.Fatal(err)
			}

			got, err := ReadMsg(&buf)
			if err != nil {
				t.Fatal(err)
			}
			if got.Type != tt.typ {
				t.Errorf("type = %q, want %q", got.Type, tt.typ)
			}
			if got.ID != 1 {
				t.Errorf("id = %d, want 1", got.ID)
			}
		})
	}
}

func TestResponseRoundtrip(t *testing.T) {
	t.Run("Result", func(t *testing.T) {
		orig := Result{OK: true, Message: "done"}
		env, err := NewEnvelope(TypeResult, 5, &orig)
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := WriteMsg(&buf, env); err != nil {
			t.Fatal(err)
		}
		got, err := ReadMsg(&buf)
		if err != nil {
			t.Fatal(err)
		}
		var decoded Result
		if err := DecodeBody(got.Body, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded != orig {
			t.Errorf("got %+v, want %+v", decoded, orig)
		}
	})

	t.Run("ErrorResult", func(t *testing.T) {
		orig := ErrorResult{Error: "not found"}
		env, err := NewEnvelope(TypeError, 5, &orig)
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := WriteMsg(&buf, env); err != nil {
			t.Fatal(err)
		}
		got, err := ReadMsg(&buf)
		if err != nil {
			t.Fatal(err)
		}
		var decoded ErrorResult
		if err := DecodeBody(got.Body, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded != orig {
			t.Errorf("got %+v, want %+v", decoded, orig)
		}
	})

	t.Run("QueryContainersResp", func(t *testing.T) {
		orig := QueryContainersResp{
			Containers: []ContainerInfo{
				{ID: "abc", Name: "web", Image: "nginx", State: "running", Project: "myapp"},
			},
		}
		env, err := NewEnvelope(TypeResult, 3, &orig)
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := WriteMsg(&buf, env); err != nil {
			t.Fatal(err)
		}
		got, err := ReadMsg(&buf)
		if err != nil {
			t.Fatal(err)
		}
		var decoded QueryContainersResp
		if err := DecodeBody(got.Body, &decoded); err != nil {
			t.Fatal(err)
		}
		if len(decoded.Containers) != 1 || decoded.Containers[0].Project != "myapp" {
			t.Errorf("containers mismatch: %+v", decoded.Containers)
		}
	})
}

func TestNewEnvelopeNoBody(t *testing.T) {
	env := NewEnvelopeNoBody(TypeSubscribeMetrics, 1)
	if env.Type != TypeSubscribeMetrics {
		t.Errorf("type = %q, want %q", env.Type, TypeSubscribeMetrics)
	}
	if env.ID != 1 {
		t.Errorf("id = %d, want 1", env.ID)
	}
	if env.Body != nil {
		t.Errorf("body should be nil, got %v", env.Body)
	}

	// Should still round-trip.
	var buf bytes.Buffer
	if err := WriteMsg(&buf, env); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMsg(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != TypeSubscribeMetrics {
		t.Errorf("type = %q, want %q", got.Type, TypeSubscribeMetrics)
	}
}

func TestContainerEventRoundtrip(t *testing.T) {
	orig := ContainerEvent{
		Timestamp:   1700000000,
		ContainerID: "abc123def456",
		Name:        "web",
		Image:       "nginx:latest",
		State:       "running",
		Action:      "start",
		Project:     "myapp",
	}

	env, err := NewEnvelope(TypeContainerEvent, 0, &orig)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := WriteMsg(&buf, env); err != nil {
		t.Fatal(err)
	}

	got, err := ReadMsg(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != TypeContainerEvent {
		t.Fatalf("type = %q, want %q", got.Type, TypeContainerEvent)
	}

	var decoded ContainerEvent
	if err := DecodeBody(got.Body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != orig {
		t.Errorf("got %+v, want %+v", decoded, orig)
	}
}

func TestContainerEventOmitEmptyProject(t *testing.T) {
	orig := ContainerEvent{
		Timestamp:   1700000000,
		ContainerID: "abc123",
		Name:        "web",
		Image:       "nginx",
		State:       "exited",
		Action:      "die",
	}

	raw, err := msgpack.Marshal(&orig)
	if err != nil {
		t.Fatal(err)
	}

	var decoded ContainerEvent
	if err := msgpack.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Project != "" {
		t.Errorf("project = %q, want empty (omitempty)", decoded.Project)
	}
	if decoded != orig {
		t.Errorf("got %+v, want %+v", decoded, orig)
	}
}

func TestNewFieldsRoundtrip(t *testing.T) {
	orig := MetricsUpdate{
		Timestamp: 1700000000,
		Host: &HostMetrics{
			CPUPercent: 45.5, MemTotal: 16e9, MemUsed: 8e9, MemPercent: 50,
			MemCached: 2e9, MemFree: 6e9,
			SwapTotal: 4e9, SwapUsed: 1e9,
		},
		Containers: []ContainerMetrics{
			{
				ID: "abc123", Name: "web", Image: "nginx", State: "running",
				Health: "healthy", StartedAt: 1700000000, RestartCount: 3, ExitCode: 0,
				CPUPercent: 5, MemUsage: 100e6, MemLimit: 512e6, MemPercent: 19.5,
			},
		},
	}

	raw, err := msgpack.Marshal(&orig)
	if err != nil {
		t.Fatal(err)
	}

	var decoded MetricsUpdate
	if err := msgpack.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Host.MemCached != 2e9 {
		t.Errorf("host mem_cached = %d, want %d", decoded.Host.MemCached, uint64(2e9))
	}
	if decoded.Host.MemFree != 6e9 {
		t.Errorf("host mem_free = %d, want %d", decoded.Host.MemFree, uint64(6e9))
	}
	c := decoded.Containers[0]
	if c.Health != "healthy" {
		t.Errorf("health = %q, want healthy", c.Health)
	}
	if c.StartedAt != 1700000000 {
		t.Errorf("started_at = %d, want 1700000000", c.StartedAt)
	}
	if c.RestartCount != 3 {
		t.Errorf("restart_count = %d, want 3", c.RestartCount)
	}
}

func TestSetTrackingReqRoundtrip(t *testing.T) {
	orig := SetTrackingReq{Container: "web", Tracked: false}
	env, err := NewEnvelope(TypeActionSetTracking, 1, &orig)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := WriteMsg(&buf, env); err != nil {
		t.Fatal(err)
	}

	got, err := ReadMsg(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != TypeActionSetTracking {
		t.Fatalf("type = %q, want %q", got.Type, TypeActionSetTracking)
	}

	var decoded SetTrackingReq
	if err := DecodeBody(got.Body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Container != "web" || decoded.Tracked != false {
		t.Errorf("got %+v, want container=web tracked=false", decoded)
	}
}

func TestQueryTrackingRespRoundtrip(t *testing.T) {
	orig := QueryTrackingResp{
		TrackedContainers: []string{"web", "api"},
		TrackedProjects:   []string{"myapp"},
	}
	env, err := NewEnvelope(TypeResult, 1, &orig)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := WriteMsg(&buf, env); err != nil {
		t.Fatal(err)
	}

	got, err := ReadMsg(&buf)
	if err != nil {
		t.Fatal(err)
	}

	var decoded QueryTrackingResp
	if err := DecodeBody(got.Body, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.TrackedContainers) != 2 || decoded.TrackedContainers[0] != "web" {
		t.Errorf("containers = %v, want [web api]", decoded.TrackedContainers)
	}
	if len(decoded.TrackedProjects) != 1 || decoded.TrackedProjects[0] != "myapp" {
		t.Errorf("projects = %v, want [myapp]", decoded.TrackedProjects)
	}
}

func TestContainerInfoTrackedField(t *testing.T) {
	orig := QueryContainersResp{
		Containers: []ContainerInfo{
			{ID: "abc", Name: "web", Tracked: true},
			{ID: "def", Name: "api", Tracked: false},
		},
	}
	raw, err := msgpack.Marshal(&orig)
	if err != nil {
		t.Fatal(err)
	}
	var decoded QueryContainersResp
	if err := msgpack.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.Containers[0].Tracked {
		t.Error("first container should be tracked")
	}
	if decoded.Containers[1].Tracked {
		t.Error("second container should not be tracked")
	}
}

func TestContainerInfoNewFields(t *testing.T) {
	orig := QueryContainersResp{
		Containers: []ContainerInfo{
			{
				ID: "abc", Name: "web", Image: "nginx", State: "running", Project: "myapp",
				Health: "unhealthy", StartedAt: 1700000000, RestartCount: 2, ExitCode: 1,
			},
		},
	}

	raw, err := msgpack.Marshal(&orig)
	if err != nil {
		t.Fatal(err)
	}

	var decoded QueryContainersResp
	if err := msgpack.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	c := decoded.Containers[0]
	if c.Health != "unhealthy" {
		t.Errorf("health = %q, want unhealthy", c.Health)
	}
	if c.RestartCount != 2 {
		t.Errorf("restart_count = %d, want 2", c.RestartCount)
	}
}

func TestTimedMetricsRoundtrip(t *testing.T) {
	orig := QueryMetricsResp{
		Host: []TimedHostMetrics{
			{Timestamp: 1700000000, HostMetrics: HostMetrics{CPUPercent: 45.5, MemTotal: 16e9}},
		},
		Disks: []TimedDiskMetrics{
			{Timestamp: 1700000000, DiskMetrics: DiskMetrics{Mountpoint: "/", Total: 100e9}},
		},
		Networks: []TimedNetMetrics{
			{Timestamp: 1700000000, NetMetrics: NetMetrics{Iface: "eth0", RxBytes: 1000}},
		},
		Containers: []TimedContainerMetrics{
			{Timestamp: 1700000000, ContainerMetrics: ContainerMetrics{ID: "abc", Name: "web"}},
		},
	}

	raw, err := msgpack.Marshal(&orig)
	if err != nil {
		t.Fatal(err)
	}

	var decoded QueryMetricsResp
	if err := msgpack.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Host) != 1 || decoded.Host[0].CPUPercent != 45.5 {
		t.Errorf("host mismatch: %+v", decoded.Host)
	}
	if len(decoded.Containers) != 1 || decoded.Containers[0].ID != "abc" {
		t.Errorf("containers mismatch: %+v", decoded.Containers)
	}
}
