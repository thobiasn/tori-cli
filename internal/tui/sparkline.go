package tui

import (
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// LoadingSparkline generates an animated 2-row braille sine wave for loading states.
// Driven by the spinner frame counter; produces a gentle scrolling wave.
func LoadingSparkline(frame, width int, color lipgloss.Color) (string, string) {
	if width < 1 {
		return "", ""
	}

	totalSamples := width * 2
	topChars := make([]rune, width)
	botChars := make([]rune, width)

	for i := 0; i < width; i++ {
		li := i * 2
		ri := li + 1

		lh := waveHeight(li, totalSamples, frame)
		rh := waveHeight(ri, totalSamples, frame)

		botChars[i] = rune(0x2800 | leftColBits(min(lh, 4)) | rightColBits(min(rh, 4)))
		topChars[i] = rune(0x2800 | leftColBits(max(lh-4, 0)) | rightColBits(max(rh-4, 0)))
	}

	style := lipgloss.NewStyle().Foreground(color)
	return style.Render(string(topChars)), style.Render(string(botChars))
}

// waveHeight returns a braille dot height (1–5) for a sine wave at the given sample index.
// The wave scrolls left as frame advances, with ~2 cycles across the width.
func waveHeight(idx, totalSamples, frame int) int {
	phase := 2 * math.Pi * float64(idx) / float64(totalSamples) * 2
	scroll := 2 * math.Pi * float64(frame) / 20
	v := math.Sin(phase - scroll)
	// Map [-1, 1] to [1, 5].
	return int(math.Round((v+1)/2*4)) + 1
}

// shimmerDensity maps a 0–3 level to braille characters of increasing density.
// Used for the log loading skeleton shimmer effect.
var shimmerDensity = [4]rune{
	0x2824, // ⠤ dots 3,6 — thin horizontal
	0x2836, // ⠶ dots 2,3,5,6 — medium
	0x28F6, // ⣶ dots 2,3,5,6,7,8 — thick
	0x28FF, // ⣿ all dots — full
}

// LoadingLogs generates animated skeleton log lines using braille characters.
// Each line mimics a log entry shape (timestamp block + message block of varying width).
// A diagonal shimmer wave sweeps through, cycling braille density.
func LoadingLogs(frame, width, height int, color lipgloss.Color) string {
	if width < 1 || height < 1 {
		return ""
	}

	tsW := 8 // timestamp placeholder width
	gap := 2 // gap between timestamp and message
	maxMsgW := width - tsW - gap
	if maxMsgW < 4 {
		maxMsgW = 4
	}

	style := lipgloss.NewStyle().Foreground(color)
	lines := make([]string, height)

	for y := 0; y < height; y++ {
		// Deterministic pseudo-random message width per line (30%–90% of available).
		frac := float64((y*17+7)%31) / 31.0
		msgW := int(frac*float64(maxMsgW)*0.6) + maxMsgW*3/10
		if msgW > maxMsgW {
			msgW = maxMsgW
		}

		buf := make([]rune, width)
		for x := 0; x < width; x++ {
			inTs := x < tsW
			inMsg := x >= tsW+gap && x < tsW+gap+msgW
			if !inTs && !inMsg {
				buf[x] = ' '
				continue
			}

			// Diagonal shimmer: sine wave with ~1.5 cycles, scrolling.
			phase := 2 * math.Pi * float64(x+y*4) / float64(width) * 1.5
			scroll := 2 * math.Pi * float64(frame) / 16
			v := (math.Sin(phase-scroll) + 1) / 2 // 0..1

			// Bias toward sparse: only the peak reaches full density.
			level := int(v * 3.99)
			buf[x] = shimmerDensity[level]
		}
		lines[y] = style.Render(string(buf))
	}
	return strings.Join(lines, "\n")
}

// ceilingSteps are the discrete scaling ceilings for braille sparklines.
// The sparkline picks the first step where peak < step * 0.85, giving ~15% headroom.
var ceilingSteps = [...]float64{10, 15, 25, 50, 75, 100}

// selectCeiling determines the y-axis ceiling for a sparkline.
// When knownMax > 0, the data has a known upper bound (e.g. 100% for host
// metrics, MemLimit bytes for capped containers) and hitting it fills the graph.
// When knownMax is 0, the ceiling is auto-scaled from the peak: a discrete step
// is chosen if the peak fits, otherwise peak/0.85 gives ~15% headroom.
func selectCeiling(peak, knownMax float64) float64 {
	if knownMax > 0 {
		return knownMax
	}
	maxStep := ceilingSteps[len(ceilingSteps)-1]
	ceiling := maxStep
	if peak > maxStep {
		ceiling = peak / 0.85
	}
	for _, step := range ceilingSteps {
		if peak < step*0.85 {
			return step
		}
	}
	return ceiling
}

// Sparkline renders a 2-row braille sparkline in a fixed color.
// Each braille character encodes two adjacent data points (left and right
// columns), and two vertically stacked characters give 8 levels of resolution.
// When max > 0, it is used as the ceiling directly — the data has a known
// upper bound and hitting it fills the graph. When max is 0, values are
// auto-scaled against a discrete ceiling derived from the peak.
// Returns (topRow, bottomRow) as separately styled strings.
func Sparkline(data []float64, width int, color lipgloss.Color, knownMax float64) (string, string) {
	if width < 1 {
		return "", ""
	}

	samples := resample(data, width*2)

	// Find peak.
	var peak float64
	for _, v := range samples {
		if v > peak {
			peak = v
		}
	}

	ceiling := selectCeiling(peak, knownMax)

	topChars := make([]rune, width)
	botChars := make([]rune, width)

	for i := 0; i < width; i++ {
		li := i * 2
		ri := li + 1

		lh := dotHeight(samples, li, ceiling)
		rh := dotHeight(samples, ri, ceiling)

		// Split each height into bottom (0–4) and top (0–4).
		botChars[i] = rune(0x2800 | leftColBits(min(lh, 4)) | rightColBits(min(rh, 4)))
		topChars[i] = rune(0x2800 | leftColBits(max(lh-4, 0)) | rightColBits(max(rh-4, 0)))
	}

	// Left-pad with empty braille when data doesn't fill the width.
	dataChars := (len(samples) + 1) / 2
	if pad := width - dataChars; pad > 0 {
		for _, chars := range []*[]rune{&topChars, &botChars} {
			padded := make([]rune, width)
			for p := 0; p < pad; p++ {
				padded[p] = 0x2800
			}
			copy(padded[pad:], (*chars)[width-dataChars:])
			*chars = padded
		}
	}

	style := lipgloss.NewStyle().Foreground(color)
	return style.Render(string(topChars)), style.Render(string(botChars))
}

// dotHeight converts a sample value to a dot height (0–8).
// Any nonzero value gets at least height 1.
func dotHeight(samples []float64, idx int, ceiling float64) int {
	if idx >= len(samples) {
		return 0
	}
	v := samples[idx]
	if v <= 0 {
		return 0
	}
	h := int(math.Round(v / ceiling * 8))
	if h < 1 {
		h = 1
	}
	if h > 8 {
		h = 8
	}
	return h
}

// leftColBits maps a fill height (0–4 dots from bottom up) to left-column braille bits.
func leftColBits(h int) int {
	switch h {
	case 1:
		return 0x40 // dot 7
	case 2:
		return 0x44 // dots 7,3
	case 3:
		return 0x46 // dots 7,3,2
	case 4:
		return 0x47 // dots 7,3,2,1
	default:
		return 0
	}
}

// rightColBits maps a fill height (0–4 dots from bottom up) to right-column braille bits.
func rightColBits(h int) int {
	switch h {
	case 1:
		return 0x80 // dot 8
	case 2:
		return 0xA0 // dots 8,6
	case 3:
		return 0xB0 // dots 8,6,5
	case 4:
		return 0xB8 // dots 8,6,5,4
	default:
		return 0
	}
}

// resample produces exactly n samples from data.
// Downsamples by averaging buckets, upsamples by linear interpolation.
func resample(data []float64, n int) []float64 {
	if len(data) == 0 || n <= 0 {
		return make([]float64, n)
	}
	if len(data) == n {
		out := make([]float64, n)
		copy(out, data)
		return out
	}

	out := make([]float64, n)

	if len(data) > n {
		// Downsample: average each bucket.
		ratio := float64(len(data)) / float64(n)
		for i := range out {
			lo := int(float64(i) * ratio)
			hi := int(float64(i+1) * ratio)
			if hi > len(data) {
				hi = len(data)
			}
			var sum float64
			for j := lo; j < hi; j++ {
				sum += data[j]
			}
			out[i] = sum / float64(hi-lo)
		}
	} else {
		// Upsample: linear interpolation.
		for i := range out {
			t := float64(i) * float64(len(data)-1) / float64(n-1)
			lo := int(t)
			if lo >= len(data)-1 {
				out[i] = data[len(data)-1]
				continue
			}
			frac := t - float64(lo)
			out[i] = data[lo]*(1-frac) + data[lo+1]*frac
		}
	}
	return out
}
