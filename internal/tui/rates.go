package tui

import "github.com/thobiasn/rook/internal/protocol"

// RateCalc computes per-second rates from cumulative network and container counters.
type RateCalc struct {
	// Host network: previous values per interface.
	prevNet map[string]netSample
	// Container: previous values per container ID.
	prevCont map[string]contSample

	// Computed results.
	NetRxRate      float64
	NetTxRate      float64
	ContainerRates map[string]ContainerRate
}

type netSample struct {
	rxBytes uint64
	txBytes uint64
	ts      int64
}

type contSample struct {
	netRx      uint64
	netTx      uint64
	blockRead  uint64
	blockWrite uint64
	ts         int64
}

// ContainerRate holds computed rates for a single container.
type ContainerRate struct {
	NetRxRate      float64
	NetTxRate      float64
	BlockReadRate  float64
	BlockWriteRate float64
}

// NewRateCalc creates a new rate calculator.
func NewRateCalc() *RateCalc {
	return &RateCalc{
		prevNet:        make(map[string]netSample),
		prevCont:       make(map[string]contSample),
		ContainerRates: make(map[string]ContainerRate),
	}
}

// Update computes rates from a new metrics snapshot.
func (r *RateCalc) Update(ts int64, nets []protocol.NetMetrics, containers []protocol.ContainerMetrics) {
	// Host network rates.
	var totalRx, totalTx float64
	currentIfaces := make(map[string]bool, len(nets))
	for _, n := range nets {
		currentIfaces[n.Iface] = true
		if prev, ok := r.prevNet[n.Iface]; ok {
			dt := float64(ts - prev.ts)
			if dt > 0 {
				if n.RxBytes >= prev.rxBytes {
					totalRx += float64(n.RxBytes-prev.rxBytes) / dt
				}
				if n.TxBytes >= prev.txBytes {
					totalTx += float64(n.TxBytes-prev.txBytes) / dt
				}
			}
		}
		r.prevNet[n.Iface] = netSample{rxBytes: n.RxBytes, txBytes: n.TxBytes, ts: ts}
	}
	r.NetRxRate = totalRx
	r.NetTxRate = totalTx

	// Clean up stale interfaces.
	for iface := range r.prevNet {
		if !currentIfaces[iface] {
			delete(r.prevNet, iface)
		}
	}

	// Container rates.
	currentConts := make(map[string]bool, len(containers))
	for _, c := range containers {
		currentConts[c.ID] = true
		if prev, ok := r.prevCont[c.ID]; ok {
			dt := float64(ts - prev.ts)
			if dt > 0 {
				var cr ContainerRate
				if c.NetRx >= prev.netRx {
					cr.NetRxRate = float64(c.NetRx-prev.netRx) / dt
				}
				if c.NetTx >= prev.netTx {
					cr.NetTxRate = float64(c.NetTx-prev.netTx) / dt
				}
				if c.BlockRead >= prev.blockRead {
					cr.BlockReadRate = float64(c.BlockRead-prev.blockRead) / dt
				}
				if c.BlockWrite >= prev.blockWrite {
					cr.BlockWriteRate = float64(c.BlockWrite-prev.blockWrite) / dt
				}
				r.ContainerRates[c.ID] = cr
			}
		}
		r.prevCont[c.ID] = contSample{
			netRx: c.NetRx, netTx: c.NetTx,
			blockRead: c.BlockRead, blockWrite: c.BlockWrite,
			ts: ts,
		}
	}

	// Clean up stale containers.
	for id := range r.prevCont {
		if !currentConts[id] {
			delete(r.prevCont, id)
			delete(r.ContainerRates, id)
		}
	}
}
