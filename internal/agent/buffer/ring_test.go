package buffer_test

import (
	"testing"
	"time"

	"github.com/pulsewatch/internal/agent/buffer"
	"github.com/pulsewatch/internal/shared/models"
)

func makeSnap(i int) models.Snapshot {
	return models.Snapshot{
		Timestamp: time.Unix(int64(i), 0),
		Host:      "test",
		Metrics:   map[string]float64{"cpu.usage_percent": float64(i)},
	}
}

func TestRingBuffer_BasicPushAndLast(t *testing.T) {
	rb := buffer.New(5)

	for i := 1; i <= 3; i++ {
		rb.Push(makeSnap(i))
	}

	got := rb.Last(3)
	if len(got) != 3 {
		t.Fatalf("expected 3 snapshots, got %d", len(got))
	}
	// Chronologische Reihenfolge: älteste zuerst
	for i, s := range got {
		want := float64(i + 1)
		if s.Metrics["cpu.usage_percent"] != want {
			t.Errorf("index %d: expected cpu=%v, got %v", i, want, s.Metrics["cpu.usage_percent"])
		}
	}
}

func TestRingBuffer_Overflow(t *testing.T) {
	rb := buffer.New(3)

	for i := 1; i <= 6; i++ {
		rb.Push(makeSnap(i))
	}

	if rb.Size() != 3 {
		t.Fatalf("expected size 3 after overflow, got %d", rb.Size())
	}

	got := rb.Last(3)
	// Nach 6 Pushes mit capacity 3: letzte 3 sind 4, 5, 6
	expected := []float64{4, 5, 6}
	for i, s := range got {
		if s.Metrics["cpu.usage_percent"] != expected[i] {
			t.Errorf("index %d: expected cpu=%v, got %v", i, expected[i], s.Metrics["cpu.usage_percent"])
		}
	}
}

func TestRingBuffer_LastMoreThanSize(t *testing.T) {
	rb := buffer.New(10)
	rb.Push(makeSnap(1))
	rb.Push(makeSnap(2))

	got := rb.Last(100) // Mehr angefordert als vorhanden
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d", len(got))
	}
}

func TestRingBuffer_Empty(t *testing.T) {
	rb := buffer.New(10)
	if got := rb.Last(5); got != nil {
		t.Errorf("expected nil on empty buffer, got %v", got)
	}
	if rb.Size() != 0 {
		t.Errorf("expected size 0")
	}
}

func TestRingBuffer_All(t *testing.T) {
	rb := buffer.New(5)
	for i := 1; i <= 4; i++ {
		rb.Push(makeSnap(i))
	}
	got := rb.All()
	if len(got) != 4 {
		t.Fatalf("expected 4, got %d", len(got))
	}
}
