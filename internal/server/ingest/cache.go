package ingest

import (
	"sync"

	"github.com/pulsewatch/internal/shared/models"
)

// LiveCache hält den letzten bekannten Snapshot pro Host
// und implementiert das alerting.MetricProvider Interface.
type LiveCache struct {
	mu   sync.RWMutex
	data map[string]models.Snapshot // host → letzter Snapshot
}

func NewLiveCache() *LiveCache {
	return &LiveCache{data: make(map[string]models.Snapshot)}
}

// Update aktualisiert den Cache mit einem neuen Snapshot
func (c *LiveCache) Update(snap models.Snapshot) {
	c.mu.Lock()
	c.data[snap.Host] = snap
	c.mu.Unlock()
}

// LastValue gibt den letzten bekannten Wert einer Metrik zurück
func (c *LiveCache) LastValue(host, metric string) (float64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	snap, ok := c.data[host]
	if !ok {
		return 0, false
	}
	v, ok := snap.Metrics[metric]
	return v, ok
}

// Hosts gibt alle bekannten Hosts zurück
func (c *LiveCache) Hosts() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	hosts := make([]string, 0, len(c.data))
	for h := range c.data {
		hosts = append(hosts, h)
	}
	return hosts
}

// LatestSnapshot gibt den letzten Snapshot eines Hosts zurück
func (c *LiveCache) LatestSnapshot(host string) (models.Snapshot, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s, ok := c.data[host]
	return s, ok
}
