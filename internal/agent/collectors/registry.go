package collectors

import "github.com/pulsewatch/internal/shared/models"

// Collector ist das Interface für alle Metrik-Collector
type Collector interface {
	// Name gibt den eindeutigen Namen des Collectors zurück
	Name() string
	// Collect liest aktuelle Metriken und gibt sie zurück.
	// tags ist optional und enthält zusätzliche Labels pro Metrik-Key.
	Collect() (metrics map[string]float64, tags map[string]models.Tags, err error)
}

// Registry verwaltet alle registrierten Collectors
type Registry struct {
	collectors []Collector
}

// NewRegistry erstellt eine Registry mit allen Standard-Collectors
func NewRegistry() *Registry {
	return &Registry{
		collectors: []Collector{
			NewCPUCollector(),
			NewMemCollector(),
			NewDiskCollector(),
			NewNetCollector(),
			NewLoadCollector(),
		},
	}
}

// All gibt alle registrierten Collectors zurück
func (r *Registry) All() []Collector {
	return r.collectors
}

// Add fügt einen eigenen Collector hinzu (für Plugins)
func (r *Registry) Add(c Collector) {
	r.collectors = append(r.collectors, c)
}
