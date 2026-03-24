package tui

import "testing"

// TestSparklineStability verifies that live-mode sparklines (1:1 mapping,
// no resampling) produce stable dot heights when new data arrives.
func TestSparklineStability(t *testing.T) {
	const (
		bufSize  = 600 // histBufSize
		width    = 80
		nSamples = width * 2 // 160
		ceiling  = 100.0
	)

	// Fill a ring buffer with steady 20% CPU, then a spike.
	rb := NewRingBuffer[float64](bufSize)
	for i := 0; i < bufSize; i++ {
		if i == 301 {
			rb.Push(95) // spike
		} else {
			rb.Push(20)
		}
	}

	// Simulate live mode: truncate to the last nSamples points.
	data1 := tailSlice(rb.Data(), nSamples, true)
	heights1 := make([]int, len(data1))
	for i, v := range data1 {
		heights1[i] = dotHeight([]float64{v}, 0, ceiling)
	}

	// Push a single new tick.
	rb.Push(20)

	data2 := tailSlice(rb.Data(), nSamples, true)
	heights2 := make([]int, len(data2))
	for i, v := range data2 {
		heights2[i] = dotHeight([]float64{v}, 0, ceiling)
	}

	// With 1:1 mapping, interior heights should be identical after a shift.
	// heights2[i] should equal heights1[i+1] (data scrolled left by one).
	for i := 0; i < nSamples-1; i++ {
		if heights2[i] != heights1[i+1] {
			t.Errorf("height[%d] after tick = %d, want %d (= height[%d] before tick)",
				i, heights2[i], heights1[i+1], i+1)
		}
	}
}
