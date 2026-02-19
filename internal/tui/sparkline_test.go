package tui

import (
	"math"
	"testing"
)

func TestSelectCeiling(t *testing.T) {
	tests := []struct {
		name     string
		peak     float64
		knownMax float64
		want     float64
	}{
		// Known max: ceiling = knownMax regardless of peak.
		{"host cpu at max", 100, 100, 100},
		{"host cpu idle", 5, 100, 100},
		{"host cpu zero", 0, 100, 100},
		{"container cpu at 2-core limit", 200, 200, 200},
		{"container mem at 1GB limit", 1e9, 1e9, 1e9},
		{"spike above limit still uses limit", 220, 200, 200},

		// Auto-scale: discrete steps (peak < step * 0.85 picks that step).
		// Steps: 10, 15, 25, 50, 75, 100.
		{"zero peak auto-scales to 10", 0, 0, 10},
		{"tiny peak picks step 10", 5, 0, 10},
		{"peak 9 picks step 15", 9, 0, 15},
		{"peak 20 picks step 25", 20, 0, 25},
		{"peak 40 picks step 50", 40, 0, 50},
		{"peak 45 picks step 75", 45, 0, 75},
		{"peak 60 picks step 75", 60, 0, 75},
		{"peak 64 picks step 100", 64, 0, 100},
		{"peak 84 picks step 100", 84, 0, 100},

		// Auto-scale: no step fits (peak >= 85), falls back to maxStep=100.
		{"peak 86 falls back to 100", 86, 0, 100},
		{"peak 100 falls back to 100", 100, 0, 100},

		// Auto-scale: peak exceeds maxStep, scales to peak/0.85.
		{"peak 101 auto-scales", 101, 0, 101 / 0.85},
		{"peak 350 auto-scales", 350, 0, 350 / 0.85},
		{"memory bytes auto-scale", 6e8, 0, 6e8 / 0.85},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := selectCeiling(tt.peak, tt.knownMax)
			if math.Abs(got-tt.want) > 0.01 {
				t.Errorf("selectCeiling(%g, %g) = %g, want %g", tt.peak, tt.knownMax, got, tt.want)
			}
		})
	}
}

func TestDotHeight(t *testing.T) {
	tests := []struct {
		name    string
		samples []float64
		idx     int
		ceiling float64
		want    int
	}{
		{"zero value", []float64{0}, 0, 100, 0},
		{"negative value", []float64{-5}, 0, 100, 0},
		{"at ceiling", []float64{100}, 0, 100, 8},
		{"half ceiling", []float64{50}, 0, 100, 4},
		{"quarter ceiling", []float64{25}, 0, 100, 2},
		{"small positive gets minimum 1", []float64{0.1}, 0, 100, 1},
		{"above ceiling clamps to 8", []float64{120}, 0, 100, 8},
		{"out of bounds index", []float64{100}, 1, 100, 0},

		// Real-world: memory bytes with byte ceiling.
		{"mem at limit", []float64{1e9}, 0, 1e9, 8},
		{"mem at half", []float64{5e8}, 0, 1e9, 4},

		// Real-world: multi-core CPU with auto-scaled ceiling.
		{"350% cpu, ceiling 411", []float64{350}, 0, 350 / 0.85, 7},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dotHeight(tt.samples, tt.idx, tt.ceiling)
			if got != tt.want {
				t.Errorf("dotHeight(%v, %d, %g) = %d, want %d", tt.samples, tt.idx, tt.ceiling, got, tt.want)
			}
		})
	}
}

func TestResample(t *testing.T) {
	t.Run("empty input", func(t *testing.T) {
		got := resample(nil, 4)
		if len(got) != 4 {
			t.Fatalf("len = %d, want 4", len(got))
		}
		for i, v := range got {
			if v != 0 {
				t.Errorf("got[%d] = %g, want 0", i, v)
			}
		}
	})

	t.Run("same length copies", func(t *testing.T) {
		data := []float64{10, 20, 30}
		got := resample(data, 3)
		for i, v := range got {
			if v != data[i] {
				t.Errorf("got[%d] = %g, want %g", i, v, data[i])
			}
		}
		// Verify it's a copy, not the same slice.
		got[0] = 999
		if data[0] == 999 {
			t.Error("resample returned original slice, not a copy")
		}
	})

	t.Run("downsample averages buckets", func(t *testing.T) {
		got := resample([]float64{10, 20, 30, 40}, 2)
		want := []float64{15, 35}
		for i, v := range got {
			if math.Abs(v-want[i]) > 0.01 {
				t.Errorf("got[%d] = %g, want %g", i, v, want[i])
			}
		}
	})

	t.Run("upsample interpolates", func(t *testing.T) {
		got := resample([]float64{0, 10}, 3)
		want := []float64{0, 5, 10}
		for i, v := range got {
			if math.Abs(v-want[i]) > 0.01 {
				t.Errorf("got[%d] = %g, want %g", i, v, want[i])
			}
		}
	})

	t.Run("single value fills output", func(t *testing.T) {
		got := resample([]float64{42}, 3)
		for i, v := range got {
			if v != 42 {
				t.Errorf("got[%d] = %g, want 42", i, v)
			}
		}
	})
}
