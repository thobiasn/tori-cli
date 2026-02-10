package tui

import (
	"reflect"
	"testing"
)

func TestRingBufferBasic(t *testing.T) {
	r := NewRingBuffer[int](5)

	if r.Len() != 0 {
		t.Fatalf("expected len 0, got %d", r.Len())
	}
	if data := r.Data(); data != nil {
		t.Fatalf("expected nil data, got %v", data)
	}

	r.Push(1)
	r.Push(2)
	r.Push(3)

	if r.Len() != 3 {
		t.Fatalf("expected len 3, got %d", r.Len())
	}

	want := []int{1, 2, 3}
	got := r.Data()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Data() = %v, want %v", got, want)
	}
}

func TestRingBufferOverflow(t *testing.T) {
	r := NewRingBuffer[int](3)

	for i := 1; i <= 5; i++ {
		r.Push(i)
	}

	if r.Len() != 3 {
		t.Fatalf("expected len 3, got %d", r.Len())
	}

	// Should contain the last 3 values.
	want := []int{3, 4, 5}
	got := r.Data()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Data() = %v, want %v", got, want)
	}
}

func TestRingBufferOrdering(t *testing.T) {
	r := NewRingBuffer[string](4)

	r.Push("a")
	r.Push("b")
	r.Push("c")
	r.Push("d")
	r.Push("e") // overwrites "a"
	r.Push("f") // overwrites "b"

	want := []string{"c", "d", "e", "f"}
	got := r.Data()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Data() = %v, want %v", got, want)
	}
}

func TestRingBufferExactFill(t *testing.T) {
	r := NewRingBuffer[int](3)
	r.Push(10)
	r.Push(20)
	r.Push(30)

	if r.Len() != 3 {
		t.Fatalf("expected len 3, got %d", r.Len())
	}

	want := []int{10, 20, 30}
	got := r.Data()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Data() = %v, want %v", got, want)
	}
}

func TestRingBufferSingleElement(t *testing.T) {
	r := NewRingBuffer[int](1)
	r.Push(1)
	r.Push(2)
	r.Push(3)

	if r.Len() != 1 {
		t.Fatalf("expected len 1, got %d", r.Len())
	}

	want := []int{3}
	got := r.Data()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Data() = %v, want %v", got, want)
	}
}
