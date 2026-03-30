// Package storage implements a three-tier time-series storage engine:
//
//   Hot  — in-memory ring buffer  (last ~10 min, full 1s resolution)
//   Warm — append-only flat files (last 7 days, full resolution)
//   Cold — downsampled flat files (older data, 1-min averages)
//
// No external dependencies — pure Go stdlib only.
package storage

import (
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Record ist ein einzelner Messpunkt im Storage
type Record struct {
	Timestamp time.Time
	Host      string
	Metric    string
	Value     float64
}

// QueryResult ist das Ergebnis einer Zeitreihen-Abfrage
type QueryResult struct {
	Host   string
	Metric string
	Points []Point
}

// Point ist ein einzelner Datenpunkt
type Point struct {
	Timestamp time.Time
	Value     float64
}

// Query beschreibt eine Abfrage
type Query struct {
	Host     string
	Metric   string
	From     time.Time
	To       time.Time
	MaxPoints int // 0 = unbegrenzt; >0 = Downsampling auf N Punkte
}

// ── Engine ────────────────────────────────────────────────────────────────────

const (
	hotCapacity   = 600       // 10 Minuten @ 1s pro Host+Metric
	flushInterval = 10 * time.Second
	fileExt       = ".pwts"   // PulseWatch Time Series
)

// Engine ist die zentrale Storage-Instanz
type Engine struct {
	mu      sync.RWMutex
	hot     map[string]*hotSeries // key = "host\x00metric"
	dataDir string
	logger  *slog.Logger

	writeCh chan Record
	stopCh  chan struct{}
	wg      sync.WaitGroup
}

// New erstellt eine neue Storage-Engine
func New(dataDir string) (*Engine, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("storage: mkdir %s: %w", dataDir, err)
	}

	e := &Engine{
		hot:     make(map[string]*hotSeries),
		dataDir: dataDir,
		logger:  slog.Default(),
		writeCh: make(chan Record, 8192),
		stopCh:  make(chan struct{}),
	}
	return e, nil
}

// Start startet den Hintergrund-Writer
func (e *Engine) Start() {
	e.wg.Add(1)
	go e.writeLoop()
}

// Stop fährt den Writer sauber herunter
func (e *Engine) Stop() {
	close(e.stopCh)
	e.wg.Wait()
}

// Write schreibt einen Record asynchron (non-blocking)
func (e *Engine) Write(r Record) {
	select {
	case e.writeCh <- r:
	default:
		e.logger.Warn("storage write channel full, dropping record",
			"host", r.Host, "metric", r.Metric)
	}
}

// WriteBatch schreibt mehrere Records aus einem Snapshot
func (e *Engine) WriteBatch(records []Record) {
	for _, r := range records {
		e.Write(r)
	}
}

// Query fragt Zeitreihendaten ab
func (e *Engine) Query(q Query) (QueryResult, error) {
	result := QueryResult{Host: q.Host, Metric: q.Metric}

	now := time.Now()
	if q.To.IsZero() {
		q.To = now
	}
	if q.From.IsZero() {
		q.From = q.To.Add(-1 * time.Hour)
	}

	var points []Point

	// Hot-Daten: immer versuchen (in-memory, schnell)
	hotPoints := e.queryHot(q.Host, q.Metric, q.From, q.To)
	points = append(points, hotPoints...)

	// Warm-Daten: immer versuchen.
	// Nötig wenn Hot leer ist (z.B. nach Server-Neustart) oder
	// der Zeitraum über die Hot-Kapazität hinausreicht.
	hotSet := make(map[int64]bool, len(hotPoints))
	for _, p := range hotPoints {
		hotSet[p.Timestamp.UnixNano()] = true
	}
	warmPoints, err := e.queryWarm(q.Host, q.Metric, q.From, q.To)
	if err != nil {
		e.logger.Warn("warm query error", "err", err)
	} else {
		for _, p := range warmPoints {
			if !hotSet[p.Timestamp.UnixNano()] {
				points = append(points, p)
			}
		}
	}

	// Chronologisch sortieren
	sort.Slice(points, func(i, j int) bool {
		return points[i].Timestamp.Before(points[j].Timestamp)
	})

	// Downsampling wenn MaxPoints gesetzt
	if q.MaxPoints > 0 && len(points) > q.MaxPoints {
		points = downsample(points, q.MaxPoints)
	}

	result.Points = points
	return result, nil
}

// Hosts gibt alle bekannten Hosts zurück
func (e *Engine) Hosts() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()

	seen := make(map[string]bool)
	for key := range e.hot {
		parts := strings.SplitN(key, "\x00", 2)
		if len(parts) == 2 {
			seen[parts[0]] = true
		}
	}

	// Auch Hosts aus Dateien einlesen
	entries, _ := os.ReadDir(e.dataDir)
	for _, entry := range entries {
		if entry.IsDir() {
			seen[entry.Name()] = true
		}
	}

	hosts := make([]string, 0, len(seen))
	for h := range seen {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	return hosts
}

// Metrics gibt alle bekannten Metriken für einen Host zurück
func (e *Engine) Metrics(host string) []string {
	e.mu.RLock()
	seen := make(map[string]bool)
	for key := range e.hot {
		parts := strings.SplitN(key, "\x00", 2)
		if len(parts) == 2 && parts[0] == host {
			seen[parts[1]] = true
		}
	}
	e.mu.RUnlock()

	// Aus Dateien
	hostDir := filepath.Join(e.dataDir, host)
	entries, _ := os.ReadDir(hostDir)
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), fileExt) {
			metric := strings.TrimSuffix(entry.Name(), fileExt)
			metric = strings.ReplaceAll(metric, "__", ".")
			seen[metric] = true
		}
	}

	metrics := make([]string, 0, len(seen))
	for m := range seen {
		metrics = append(metrics, m)
	}
	sort.Strings(metrics)
	return metrics
}

// ── Hot Storage (In-Memory) ───────────────────────────────────────────────────

type hotSeries struct {
	mu   sync.RWMutex
	data [hotCapacity]Point
	head int
	size int
}

func (s *hotSeries) push(p Point) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[s.head] = p
	s.head = (s.head + 1) % hotCapacity
	if s.size < hotCapacity {
		s.size++
	}
}

func (s *hotSeries) query(from, to time.Time) []Point {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.size == 0 {
		return nil
	}

	result := make([]Point, 0, s.size)
	start := (s.head - s.size + hotCapacity) % hotCapacity

	for i := 0; i < s.size; i++ {
		p := s.data[(start+i)%hotCapacity]
		if !p.Timestamp.Before(from) && !p.Timestamp.After(to) {
			result = append(result, p)
		}
	}
	return result
}

func (e *Engine) getOrCreateHot(host, metric string) *hotSeries {
	key := host + "\x00" + metric
	e.mu.RLock()
	s, ok := e.hot[key]
	e.mu.RUnlock()
	if ok {
		return s
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if s, ok = e.hot[key]; ok {
		return s
	}
	s = &hotSeries{}
	e.hot[key] = s
	return s
}

func (e *Engine) queryHot(host, metric string, from, to time.Time) []Point {
	key := host + "\x00" + metric
	e.mu.RLock()
	s, ok := e.hot[key]
	e.mu.RUnlock()
	if !ok {
		return nil
	}
	return s.query(from, to)
}

// ── Warm Storage (Flat Files) ─────────────────────────────────────────────────
//
// Dateiformat (Binary, Big-Endian):
//   [8 bytes: Unix-Nanos int64] [8 bytes: float64 IEEE 754]
//   → 16 Bytes pro Datenpunkt
//
// Dateiname: <dataDir>/<host>/<metric__dot__replaced>_<YYYY-MM-DD>.pwts

const recordSize = 16

func (e *Engine) appendWarm(r Record) error {
	path := e.warmPath(r.Host, r.Metric, r.Timestamp)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	buf := make([]byte, recordSize)
	binary.BigEndian.PutUint64(buf[0:8], uint64(r.Timestamp.UnixNano()))
	binary.BigEndian.PutUint64(buf[8:16], math.Float64bits(r.Value))
	_, err = f.Write(buf)
	return err
}

func (e *Engine) queryWarm(host, metric string, from, to time.Time) ([]Point, error) {
	var points []Point

	// Alle Tage zwischen from und to durchsuchen
	day := from.Truncate(24 * time.Hour)
	end := to.Truncate(24 * time.Hour)

	for !day.After(end) {
		path := e.warmPath(host, metric, day)
		pts, err := e.readWarmFile(path, from, to)
		if err != nil && !os.IsNotExist(err) {
			e.logger.Warn("reading warm file", "path", path, "err", err)
		}
		points = append(points, pts...)
		day = day.Add(24 * time.Hour)
	}

	return points, nil
}

func (e *Engine) readWarmFile(path string, from, to time.Time) ([]Point, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var points []Point
	buf := make([]byte, recordSize)

	for {
		if _, err := io.ReadFull(f, buf); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return points, err
		}

		nanos := int64(binary.BigEndian.Uint64(buf[0:8]))
		value := math.Float64frombits(binary.BigEndian.Uint64(buf[8:16]))
		ts := time.Unix(0, nanos)

		if !ts.Before(from) && !ts.After(to) {
			points = append(points, Point{Timestamp: ts, Value: value})
		}
	}

	return points, nil
}

func (e *Engine) warmPath(host, metric string, t time.Time) string {
	// Punkte in Dateinamen durch __ ersetzen (Filesystem-sicher)
	safeMetric := strings.ReplaceAll(metric, ".", "__")
	safeMetric = strings.ReplaceAll(safeMetric, "/", "_")
	filename := safeMetric + "_" + t.UTC().Format("2006-01-02") + fileExt
	return filepath.Join(e.dataDir, host, filename)
}

// ── Write Loop ────────────────────────────────────────────────────────────────

func (e *Engine) writeLoop() {
	defer e.wg.Done()

	flushTicker := time.NewTicker(flushInterval)
	defer flushTicker.Stop()

	// Batch-Puffer für Warm-Storage (gesammelt, dann geflusht)
	pending := make([]Record, 0, 1024)

	flush := func() {
		for _, r := range pending {
			if err := e.appendWarm(r); err != nil {
				e.logger.Warn("warm write error", "host", r.Host, "metric", r.Metric, "err", err)
			}
		}
		pending = pending[:0]
	}

	for {
		select {
		case <-e.stopCh:
			// Restliche Einträge aus Channel leeren
			for len(e.writeCh) > 0 {
				r := <-e.writeCh
				e.getOrCreateHot(r.Host, r.Metric).push(Point{r.Timestamp, r.Value})
				pending = append(pending, r)
			}
			flush()
			return

		case r := <-e.writeCh:
			// Sofort in Hot-Storage
			e.getOrCreateHot(r.Host, r.Metric).push(Point{r.Timestamp, r.Value})
			pending = append(pending, r)

		case <-flushTicker.C:
			flush()
		}
	}
}

// ── Downsampling (LTTB — Largest Triangle Three Buckets) ─────────────────────
//
// LTTB erhält die visuelle Form der Kurve viel besser als einfaches Averaging.

func downsample(points []Point, target int) []Point {
	n := len(points)
	if n <= target {
		return points
	}

	result := make([]Point, 0, target)
	result = append(result, points[0]) // Erster Punkt immer behalten

	bucketSize := float64(n-2) / float64(target-2)

	for i := 0; i < target-2; i++ {
		// Nächster Bucket-Bereich
		nextStart := int(float64(i+1)*bucketSize) + 1
		nextEnd := int(float64(i+2)*bucketSize) + 1
		if nextEnd > n {
			nextEnd = n
		}

		// Durchschnitt des nächsten Buckets als "Zielpunkt"
		var avgX, avgY float64
		count := nextEnd - nextStart
		for j := nextStart; j < nextEnd; j++ {
			avgX += float64(points[j].Timestamp.UnixNano())
			avgY += points[j].Value
		}
		avgX /= float64(count)
		avgY /= float64(count)

		// Aktueller Bucket
		rangeStart := int(float64(i)*bucketSize) + 1
		rangeEnd := int(float64(i+1)*bucketSize) + 1

		// Letzter ausgewählter Punkt
		prev := result[len(result)-1]
		prevX := float64(prev.Timestamp.UnixNano())
		prevY := prev.Value

		// Punkt mit größtem Dreieck-Flächeninhalt auswählen
		maxArea := -1.0
		var selected Point
		for j := rangeStart; j < rangeEnd; j++ {
			cx := float64(points[j].Timestamp.UnixNano())
			cy := points[j].Value
			area := math.Abs((prevX-avgX)*(cy-prevY) - (prevX-cx)*(avgY-prevY))
			if area > maxArea {
				maxArea = area
				selected = points[j]
			}
		}
		result = append(result, selected)
	}

	result = append(result, points[n-1]) // Letzter Punkt immer behalten
	return result
}
