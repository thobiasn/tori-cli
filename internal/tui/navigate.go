package tui

// clampNav applies delta to *cursor and clamps to [0, length-1].
// With delta=0 it acts as a pure bounds clamp.
func clampNav(cursor *int, delta, length int) {
	if length <= 0 {
		*cursor = 0
		return
	}
	*cursor += delta
	if *cursor < 0 {
		*cursor = 0
	}
	if max := length - 1; *cursor > max {
		*cursor = max
	}
}

// halfPage returns max(height/2, 1).
func halfPage(height int) int {
	if h := height / 2; h > 1 {
		return h
	}
	return 1
}
