package tui

import (
	"math"
	"testing"

	"github.com/thobiasn/tori-cli/internal/protocol"
)

func TestRateCalcFirstUpdateZero(t *testing.T) {
	rc := NewRateCalc()
	nets := []protocol.NetMetrics{{Iface: "eth0", RxBytes: 1000, TxBytes: 500}}
	conts := []protocol.ContainerMetrics{{ID: "c1", NetRx: 100, NetTx: 50, BlockRead: 200, BlockWrite: 100}}
	rc.Update(100, nets, conts)

	if rc.NetRxRate != 0 || rc.NetTxRate != 0 {
		t.Errorf("first update should yield zero host rates, got rx=%f tx=%f", rc.NetRxRate, rc.NetTxRate)
	}
	if _, ok := rc.ContainerRates["c1"]; ok {
		t.Error("first update should not produce container rates")
	}
}

func TestRateCalcSecondUpdate(t *testing.T) {
	rc := NewRateCalc()
	nets1 := []protocol.NetMetrics{{Iface: "eth0", RxBytes: 1000, TxBytes: 500}}
	conts1 := []protocol.ContainerMetrics{{ID: "c1", NetRx: 100, NetTx: 50, BlockRead: 200, BlockWrite: 100}}
	rc.Update(100, nets1, conts1)

	nets2 := []protocol.NetMetrics{{Iface: "eth0", RxBytes: 2000, TxBytes: 1000}}
	conts2 := []protocol.ContainerMetrics{{ID: "c1", NetRx: 600, NetTx: 250, BlockRead: 700, BlockWrite: 400}}
	rc.Update(110, nets2, conts2)

	// dt = 10s, delta rx = 1000 → 100/s
	if math.Abs(rc.NetRxRate-100) > 0.01 {
		t.Errorf("NetRxRate = %f, want 100", rc.NetRxRate)
	}
	if math.Abs(rc.NetTxRate-50) > 0.01 {
		t.Errorf("NetTxRate = %f, want 50", rc.NetTxRate)
	}

	cr := rc.ContainerRates["c1"]
	if math.Abs(cr.NetRxRate-50) > 0.01 {
		t.Errorf("container NetRxRate = %f, want 50", cr.NetRxRate)
	}
	if math.Abs(cr.BlockReadRate-50) > 0.01 {
		t.Errorf("container BlockReadRate = %f, want 50", cr.BlockReadRate)
	}
}

func TestRateCalcStaleCleanup(t *testing.T) {
	rc := NewRateCalc()
	nets := []protocol.NetMetrics{{Iface: "eth0", RxBytes: 100, TxBytes: 100}}
	conts := []protocol.ContainerMetrics{{ID: "c1", NetRx: 100, NetTx: 100}}
	rc.Update(100, nets, conts)

	// Second update: eth0 gone, c1 gone, new c2.
	nets2 := []protocol.NetMetrics{{Iface: "eth1", RxBytes: 200, TxBytes: 200}}
	conts2 := []protocol.ContainerMetrics{{ID: "c2", NetRx: 200, NetTx: 200}}
	rc.Update(110, nets2, conts2)

	if _, ok := rc.ContainerRates["c1"]; ok {
		t.Error("stale container c1 should be cleaned up")
	}
}

func TestRateCalcZeroTimeDelta(t *testing.T) {
	rc := NewRateCalc()
	nets := []protocol.NetMetrics{{Iface: "eth0", RxBytes: 1000, TxBytes: 500}}
	rc.Update(100, nets, nil)

	// Same timestamp — should not produce rates (division by zero guard).
	nets2 := []protocol.NetMetrics{{Iface: "eth0", RxBytes: 2000, TxBytes: 1000}}
	rc.Update(100, nets2, nil)

	if rc.NetRxRate != 0 || rc.NetTxRate != 0 {
		t.Errorf("zero dt should yield zero rates, got rx=%f tx=%f", rc.NetRxRate, rc.NetTxRate)
	}
}

func TestRateCalcCounterWraparound(t *testing.T) {
	rc := NewRateCalc()
	nets := []protocol.NetMetrics{{Iface: "eth0", RxBytes: 1000, TxBytes: 500}}
	conts := []protocol.ContainerMetrics{{ID: "c1", NetRx: 1000, NetTx: 500, BlockRead: 200, BlockWrite: 100}}
	rc.Update(100, nets, conts)

	// Counter reset: new values smaller than previous (wraparound).
	nets2 := []protocol.NetMetrics{{Iface: "eth0", RxBytes: 100, TxBytes: 50}}
	conts2 := []protocol.ContainerMetrics{{ID: "c1", NetRx: 50, NetTx: 25, BlockRead: 10, BlockWrite: 5}}
	rc.Update(110, nets2, conts2)

	// Should produce zero rates (not huge unsigned wraparound values).
	if rc.NetRxRate != 0 {
		t.Errorf("counter reset should yield 0 host rx rate, got %f", rc.NetRxRate)
	}
	if rc.NetTxRate != 0 {
		t.Errorf("counter reset should yield 0 host tx rate, got %f", rc.NetTxRate)
	}
	cr := rc.ContainerRates["c1"]
	if cr.NetRxRate != 0 || cr.NetTxRate != 0 || cr.BlockReadRate != 0 || cr.BlockWriteRate != 0 {
		t.Errorf("counter reset should yield 0 container rates, got %+v", cr)
	}
}

func TestRateCalcMultipleInterfaces(t *testing.T) {
	rc := NewRateCalc()
	nets1 := []protocol.NetMetrics{
		{Iface: "eth0", RxBytes: 1000, TxBytes: 500},
		{Iface: "eth1", RxBytes: 2000, TxBytes: 1000},
	}
	rc.Update(100, nets1, nil)

	nets2 := []protocol.NetMetrics{
		{Iface: "eth0", RxBytes: 2000, TxBytes: 1000},
		{Iface: "eth1", RxBytes: 4000, TxBytes: 2000},
	}
	rc.Update(110, nets2, nil)

	// eth0: 1000/10=100, eth1: 2000/10=200, total=300
	if math.Abs(rc.NetRxRate-300) > 0.01 {
		t.Errorf("NetRxRate = %f, want 300", rc.NetRxRate)
	}
	if math.Abs(rc.NetTxRate-150) > 0.01 {
		t.Errorf("NetTxRate = %f, want 150", rc.NetTxRate)
	}
}
