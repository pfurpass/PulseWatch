// Package anomaly implementiert Z-Score-basierte Anomalie-Erkennung
// für jede (host, metric)-Kombination.
//
// Z-Score = (x - µ) / σ
// Ein Z-Score > threshold (default 3.0) gilt als Anomalie.
package anomaly

import (
	"math"
	"sync"
	"time"
)

const (
	defaultWindowSize = 120  // 2 Minuten bei 1s-Granularität
	defaultThreshold  = 3.0
	minSamples        = 30   // Mindest-Stichproben vor Erkennung
)

// Score beschreibt das Ergebnis der Anomalie-Analyse
type Score struct {
	Host      string    `json:"host"`
	Metric    string    `json:"metric"`
	ZScore    float64   `json:"z_score"`
	IsAnomaly bool      `json:"is_anomaly"`
	Value     float64   `json:"value"`
	Mean      float64   `json:"mean"`
	StdDev    float64   `json:"std_dev"`
	Timestamp time.Time `json:"timestamp"`
}

type window struct {
	values []float64
	head   int
	count  int
	size   int
}

func newWindow(size int) *window {
	return &window{values: make([]float64, size), size: size}
}

func (w *window) push(v float64) {
	w.values[w.head] = v
	w.head = (w.head + 1) % w.size
	if w.count < w.size {
		w.count++
	}
}

func (w *window) stats() (mean, stddev float64) {
	if w.count == 0 {
		return 0, 0
	}
	sum := 0.0
	for i := 0; i < w.count; i++ {
		sum += w.values[i]
	}
	mean = sum / float64(w.count)

	variance := 0.0
	for i := 0; i < w.count; i++ {
		d := w.values[i] - mean
		variance += d * d
	}
	variance /= float64(w.count)
	stddev = math.Sqrt(variance)
	return
}

// Detector erkennt Anomalien in Echtzeit-Metriken
type Detector struct {
	mu         sync.RWMutex
	windows    map[string]*window // "host\x00metric"
	scores     map[string]*Score
	windowSize int
	threshold  float64
}

// New erstellt einen neuen Detector
func New(windowSize int, threshold float64) *Detector {
	if windowSize <= 0 {
		windowSize = defaultWindowSize
	}
	if threshold <= 0 {
		threshold = defaultThreshold
	}
	return &Detector{
		windows:    make(map[string]*window),
		scores:     make(map[string]*Score),
		windowSize: windowSize,
		threshold:  threshold,
	}
}

// Observe fügt einen neuen Messwert ein und berechnet den Z-Score
func (d *Detector) Observe(host, metric string, value float64, t time.Time) *Score {
	key := host + "\x00" + metric

	d.mu.Lock()
	w, ok := d.windows[key]
	if !ok {
		w = newWindow(d.windowSize)
		d.windows[key] = w
	}

	// Aktuellen Wert für Z-Score berechnen BEVOR wir ihn ins Fenster pushen
	mean, stddev := w.stats()
	w.push(value)

	var zScore float64
	isAnomaly := false

	if w.count >= minSamples {
		// Bei stddev ≈ 0 (alle Werte identisch) minimum epsilon verwenden,
		// damit Ausreißer trotzdem erkannt werden.
		effectiveStd := stddev
		if effectiveStd < 1e-9 {
			effectiveStd = 1e-9
		}
		zScore = math.Abs((value - mean) / effectiveStd)
		isAnomaly = zScore > d.threshold
	}

	score := &Score{
		Host:      host,
		Metric:    metric,
		ZScore:    zScore,
		IsAnomaly: isAnomaly,
		Value:     value,
		Mean:      mean,
		StdDev:    stddev,
		Timestamp: t,
	}
	d.scores[key] = score
	d.mu.Unlock()

	return score
}

// GetScore gibt den letzten Score für host+metric zurück
func (d *Detector) GetScore(host, metric string) (*Score, bool) {
	key := host + "\x00" + metric
	d.mu.RLock()
	defer d.mu.RUnlock()
	s, ok := d.scores[key]
	return s, ok
}

// Anomalies gibt alle aktuell als anomal markierten Metriken zurück
func (d *Detector) Anomalies() []Score {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var result []Score
	for _, s := range d.scores {
		if s.IsAnomaly {
			result = append(result, *s)
		}
	}
	return result
}

// AllScores gibt alle bekannten Scores zurück (für Dashboard)
func (d *Detector) AllScores(host string) []Score {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var result []Score
	for key, s := range d.scores {
		if host == "" || s.Host == host {
			_ = key
			result = append(result, *s)
		}
	}
	return result
}

// SetThreshold ändert den Anomalie-Schwellenwert zur Laufzeit
func (d *Detector) SetThreshold(t float64) {
	d.mu.Lock()
	d.threshold = t
	d.mu.Unlock()
}
