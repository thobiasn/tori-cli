package agent

import "github.com/thobiasn/rook/internal/protocol"

func convertTimedHost(src []TimedHostMetrics) []protocol.TimedHostMetrics {
	out := make([]protocol.TimedHostMetrics, len(src))
	for i, s := range src {
		out[i] = protocol.TimedHostMetrics{
			Timestamp: s.Timestamp.Unix(),
			HostMetrics: protocol.HostMetrics{
				CPUPercent: s.CPUPercent, MemTotal: s.MemTotal, MemUsed: s.MemUsed, MemPercent: s.MemPercent,
				MemCached: s.MemCached, MemFree: s.MemFree,
				SwapTotal: s.SwapTotal, SwapUsed: s.SwapUsed,
				Load1: s.Load1, Load5: s.Load5, Load15: s.Load15, Uptime: s.Uptime,
			},
		}
	}
	return out
}

func convertTimedDisk(src []TimedDiskMetrics) []protocol.TimedDiskMetrics {
	out := make([]protocol.TimedDiskMetrics, len(src))
	for i, s := range src {
		out[i] = protocol.TimedDiskMetrics{
			Timestamp: s.Timestamp.Unix(),
			DiskMetrics: protocol.DiskMetrics{
				Mountpoint: s.Mountpoint, Device: s.Device,
				Total: s.Total, Used: s.Used, Free: s.Free, Percent: s.Percent,
			},
		}
	}
	return out
}

func convertTimedNet(src []TimedNetMetrics) []protocol.TimedNetMetrics {
	out := make([]protocol.TimedNetMetrics, len(src))
	for i, s := range src {
		out[i] = protocol.TimedNetMetrics{
			Timestamp: s.Timestamp.Unix(),
			NetMetrics: protocol.NetMetrics{
				Iface: s.Iface, RxBytes: s.RxBytes, TxBytes: s.TxBytes,
				RxPackets: s.RxPackets, TxPackets: s.TxPackets,
				RxErrors: s.RxErrors, TxErrors: s.TxErrors,
			},
		}
	}
	return out
}

func convertTimedContainer(src []TimedContainerMetrics) []protocol.TimedContainerMetrics {
	out := make([]protocol.TimedContainerMetrics, len(src))
	for i, s := range src {
		out[i] = protocol.TimedContainerMetrics{
			Timestamp: s.Timestamp.Unix(),
			ContainerMetrics: protocol.ContainerMetrics{
				Project: s.Project, Service: s.Service,
				CPUPercent: s.CPUPercent, MemUsage: s.MemUsage, MemLimit: s.MemLimit, MemPercent: s.MemPercent,
				NetRx: s.NetRx, NetTx: s.NetTx, BlockRead: s.BlockRead, BlockWrite: s.BlockWrite, PIDs: s.PIDs,
			},
		}
	}
	return out
}

func convertLogEntries(src []LogEntry) []protocol.LogEntryMsg {
	out := make([]protocol.LogEntryMsg, len(src))
	for i, s := range src {
		out[i] = protocol.LogEntryMsg{
			Timestamp:     s.Timestamp.Unix(),
			ContainerID:   s.ContainerID,
			ContainerName: s.ContainerName,
			Stream:        s.Stream,
			Message:       s.Message,
		}
	}
	return out
}

func convertAlerts(src []Alert) []protocol.AlertMsg {
	out := make([]protocol.AlertMsg, len(src))
	for i, s := range src {
		out[i] = protocol.AlertMsg{
			ID:           s.ID,
			RuleName:     s.RuleName,
			Severity:     s.Severity,
			Condition:    s.Condition,
			InstanceKey:  s.InstanceKey,
			FiredAt:      s.FiredAt.Unix(),
			Message:      s.Message,
			Acknowledged: s.Acknowledged,
		}
		if s.ResolvedAt != nil {
			out[i].ResolvedAt = s.ResolvedAt.Unix()
		}
	}
	return out
}
