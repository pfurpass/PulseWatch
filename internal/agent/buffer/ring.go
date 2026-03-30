package buffer

import (
	"sync"

	"github.com/pulsewatch/internal/shared/models"
)

// RingBuffer speichert Snapshots im Speicher (FIFO, feste Größe)
// Wenn voll, werden älteste Einträge überschrieben.
type RingBuffer struct {
	mu       sync.RWMutex
	data     []models.Snapshot
	head     int  // nächste Schreib-Position
	size     int  // aktuelle Anzahl Einträge
	capacity int  // maximale Kapazität
}

// New erstellt einen RingBuffer mit der angegebenen Kapazität.
// Bei 1s-Granularität und capacity=86400 → 24h Puffer.
func New(capacity int) *RingBuffer {
	return &RingBuffer{
		data:     make([]models.Snapshot, capacity),
		capacity: capacity,
	}
}

// Push fügt einen Snapshot hinzu. Überschreibt älteste Daten wenn voll.
func (r *RingBuffer) Push(s models.Snapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.data[r.head] = s
	r.head = (r.head + 1) % r.capacity

	if r.size < r.capacity {
		r.size++
	}
}

// Last gibt die letzten n Snapshots zurück (chronologisch, älteste zuerst).
func (r *RingBuffer) Last(n int) []models.Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if n > r.size {
		n = r.size
	}
	if n == 0 {
		return nil
	}

	result := make([]models.Snapshot, n)

	// Start-Position: head - n (modulo)
	start := (r.head - n + r.capacity) % r.capacity

	for i := 0; i < n; i++ {
		result[i] = r.data[(start+i)%r.capacity]
	}

	return result
}

// All gibt alle gepufferten Snapshots zurück (chronologisch).
func (r *RingBuffer) All() []models.Snapshot {
	return r.Last(r.size)
}

// Size gibt die aktuelle Anzahl gepufferter Einträge zurück.
func (r *RingBuffer) Size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.size
}

// Len gibt die maximale Kapazität zurück.
func (r *RingBuffer) Len() int {
	return r.capacity
}
