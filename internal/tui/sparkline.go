package tui

import (
	"math"

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

// ceilingSteps are the discrete scaling ceilings for braille sparklines.
// The sparkline picks the first step where peak < step * 0.85, giving ~15% headroom.
var ceilingSteps = [...]float64{10, 15, 25, 50, 75, 100}

// Sparkline renders a 2-row braille sparkline in a fixed color.
// Each braille character encodes two adjacent data points (left and right
// columns), and two vertically stacked characters give 8 levels of resolution.
// Values are auto-scaled against a discrete ceiling derived from the peak.
// Returns (topRow, bottomRow) as separately styled strings.
func Sparkline(data []float64, width int, color lipgloss.Color) (string, string) {
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

	// Select ceiling: first discrete step where peak < step * 0.85.
	// If peak exceeds all steps, auto-scale with ~15% headroom.
	ceiling := peak / 0.85
	if ceiling <= 0 {
		ceiling = 100
	}
	for _, step := range ceilingSteps {
		if peak < step*0.85 {
			ceiling = step
			break
		}
	}

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
