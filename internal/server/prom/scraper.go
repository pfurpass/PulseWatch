// Package prom implementiert einen Prometheus-Metrics-Scraper.
// Er liest /metrics-Endpunkte im Prometheus text/plain Format
// und schreibt die Werte in den PulseWatch-Storage.
package prom

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pulsewatch/internal/server/storage"
)

// Target ist ein Prometheus-Scrape-Target
type Target struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`     // Anzeigename (wird als Host verwendet)
	URL      string        `json:"url"`      // z.B. "http://myapp:8080/metrics"
	Interval time.Duration `json:"interval"` // Scrape-Intervall (default 15s)
	Labels   map[string]string `json:"labels,omitempty"` // Zusätzliche Labels
}

// Scraper scrapt Prometheus-Endpunkte
type Scraper struct {
	mu      sync.RWMutex
	targets map[string]*Target
	store   *storage.Engine
	client  *http.Client
	logger  *slog.Logger
	cancels map[string]context.CancelFunc
}

// New erstellt einen neuen Prometheus-Scraper
func New(store *storage.Engine) *Scraper {
	return &Scraper{
		targets: make(map[string]*Target),
		store:   store,
		client:  &http.Client{Timeout: 10 * time.Second},
		logger:  slog.Default(),
		cancels: make(map[string]context.CancelFunc),
	}
}

// AddTarget fügt ein neues Scrape-Target hinzu und startet den Scrape-Loop
func (s *Scraper) AddTarget(ctx context.Context, t Target) {
	if t.ID == "" {
		t.ID = randID()
	}
	if t.Interval <= 0 {
		t.Interval = 15 * time.Second
	}
	if t.Name == "" {
		t.Name = t.URL
	}

	s.mu.Lock()
	// Bestehenden Loop stoppen falls vorhanden
	if cancel, ok := s.cancels[t.ID]; ok {
		cancel()
	}
	s.targets[t.ID] = &t
	tCtx, cancel := context.WithCancel(ctx)
	s.cancels[t.ID] = cancel
	s.mu.Unlock()

	go s.scrapeLoop(tCtx, t)
	s.logger.Info("prometheus target added", "name", t.Name, "url", t.URL, "interval", t.Interval)
}

// RemoveTarget stoppt und entfernt ein Scrape-Target
func (s *Scraper) RemoveTarget(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cancel, ok := s.cancels[id]; ok {
		cancel()
		delete(s.cancels, id)
	}
	delete(s.targets, id)
}

// Targets gibt alle konfigurierten Targets zurück
func (s *Scraper) Targets() []Target {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Target, 0, len(s.targets))
	for _, t := range s.targets {
		result = append(result, *t)
	}
	return result
}

// scrapeLoop scrapt ein Target periodisch
func (s *Scraper) scrapeLoop(ctx context.Context, t Target) {
	// Sofort beim Start scrapen
	s.scrapeOne(t)

	ticker := time.NewTicker(t.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scrapeOne(t)
		}
	}
}

// scrapeOne scrapt einen einzelnen Endpunkt
func (s *Scraper) scrapeOne(t Target) {
	req, err := http.NewRequest(http.MethodGet, t.URL, nil)
	if err != nil {
		s.logger.Warn("prom scrape: invalid URL", "url", t.URL, "err", err)
		return
	}
	req.Header.Set("Accept", "text/plain;version=0.0.4")

	resp, err := s.client.Do(req)
	if err != nil {
		s.logger.Warn("prom scrape failed", "target", t.Name, "err", err)
		return
	}
	defer resp.Body.Close()

	now := time.Now()
	metrics, err := parsePrometheusText(resp.Body)
	if err != nil {
		s.logger.Warn("prom parse failed", "target", t.Name, "err", err)
		return
	}

	records := make([]storage.Record, 0, len(metrics))
	for name, value := range metrics {
		// Metric-Name bereinigen
		metricName := "prom." + sanitize(name)
		records = append(records, storage.Record{
			Timestamp: now,
			Host:      t.Name,
			Metric:    metricName,
			Value:     value,
		})
	}
	s.store.WriteBatch(records)

	s.logger.Debug("prom scraped", "target", t.Name, "metrics", len(records))
}

// ── Prometheus Text Format Parser ────────────────────────────────────────
// Implementiert die wichtigsten Teile von:
// https://prometheus.io/docs/instrumenting/exposition_formats/

func parsePrometheusText(r interface{ Read([]byte) (int, error) }) (map[string]float64, error) {
	result := make(map[string]float64)
	scanner := bufio.NewScanner(r)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Kommentare und leere Zeilen überspringen
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Format: metric_name{label1="v1",label2="v2"} value [timestamp]
		// Wir ignorieren Labels für den einfachen Speicher
		name, value, ok := parseMetricLine(line)
		if !ok {
			continue
		}

		// NaN und Inf überspringen
		if value != value || value > 1e308 || value < -1e308 {
			continue
		}

		result[name] = value
	}

	return result, scanner.Err()
}

func parseMetricLine(line string) (name string, value float64, ok bool) {
	// Label-Block entfernen: "metric{...} value" → "metric value"
	labelStart := strings.IndexByte(line, '{')
	labelEnd := strings.LastIndexByte(line, '}')

	var rest string
	if labelStart >= 0 && labelEnd > labelStart {
		name = strings.TrimSpace(line[:labelStart])
		rest = strings.TrimSpace(line[labelEnd+1:])
	} else {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			return "", 0, false
		}
		name = parts[0]
		rest = parts[1]
	}

	// Timestamp (optionales drittes Feld) ignorieren
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", 0, false
	}

	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return "", 0, false
	}

	return name, v, true
}

func sanitize(s string) string {
	var b strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '.' {
			b.WriteRune(c)
		} else if c == '-' || c == ':' {
			b.WriteRune('_')
		}
	}
	return b.String()
}

func randID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// ParseText ist die exportierte Version des Parsers (für Tests)
func ParseText(r interface{ Read([]byte) (int, error) }) map[string]float64 {
	result, _ := parsePrometheusText(r)
	return result
}
