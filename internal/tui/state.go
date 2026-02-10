package tui

// RingBuffer is a fixed-size circular buffer. When full, new pushes
// overwrite the oldest entry.
type RingBuffer[T any] struct {
	buf   []T
	size  int
	head  int // next write position
	count int
}

// NewRingBuffer creates a ring buffer with the given capacity.
func NewRingBuffer[T any](size int) *RingBuffer[T] {
	return &RingBuffer[T]{
		buf:  make([]T, size),
		size: size,
	}
}

// Push adds a value to the buffer, overwriting the oldest if full.
func (r *RingBuffer[T]) Push(v T) {
	r.buf[r.head] = v
	r.head = (r.head + 1) % r.size
	if r.count < r.size {
		r.count++
	}
}

// Data returns all stored values in insertion order (oldest first).
func (r *RingBuffer[T]) Data() []T {
	if r.count == 0 {
		return nil
	}
	out := make([]T, r.count)
	start := (r.head - r.count + r.size) % r.size
	for i := 0; i < r.count; i++ {
		out[i] = r.buf[(start+i)%r.size]
	}
	return out
}

// Last returns the newest element, or false if the buffer is empty.
func (r *RingBuffer[T]) Last() (T, bool) {
	if r.count == 0 {
		var zero T
		return zero, false
	}
	idx := (r.head - 1 + r.size) % r.size
	return r.buf[idx], true
}

// Len returns the number of stored values.
func (r *RingBuffer[T]) Len() int {
	return r.count
}
