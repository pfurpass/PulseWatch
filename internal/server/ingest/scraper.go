// Package ingest scrapt periodisch alle registrierten Agents
// und schreibt die Metriken in den Storage.
package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/pulsewatch/internal/server/storage"
	"github.com/pulsewatch/internal/shared/models"
)

// AgentTarget ist ein zu scrappender Agent
type AgentTarget struct {
	Name    string // Anzeigename (optional, sonst Hostname aus Info)
	Address string // z.B. "http://192.168.1.10:19998"
}

// Scraper scrapt alle Agents und persistiert die Metriken
type Scraper struct {
	mu       sync.RWMutex
	targets  []AgentTarget
	store    *storage.Engine
	interval time.Duration
	client   *http.Client
	logger   *slog.Logger

	// Live-Broadcast: Callbacks für frisch eingetroffene Snapshots
	subMu       sync.RWMutex
	subscribers []func(models.Snapshot)

	// Live-Cache für Alerting
	liveCache *LiveCache
}

// New erstellt einen neuen Scraper
func New(store *storage.Engine, interval time.Duration) *Scraper {
	return &Scraper{
		store:    store,
		interval: interval,
		client: &http.Client{
			Timeout: 3 * time.Second,
		},
		logger: slog.Default(),
	}
}

// AddTarget registriert einen neuen Agent-Endpunkt
func (s *Scraper) AddTarget(t AgentTarget) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.targets = append(s.targets, t)
	s.logger.Info("agent target added", "address", t.Address)
}

// RemoveTarget entfernt einen Target nach Adresse
func (s *Scraper) RemoveTarget(address string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	filtered := s.targets[:0]
	for _, t := range s.targets {
		if t.Address != address {
			filtered = append(filtered, t)
		}
	}
	s.targets = filtered
}

// Targets gibt alle aktuellen Targets zurück
func (s *Scraper) Targets() []AgentTarget {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]AgentTarget, len(s.targets))
	copy(result, s.targets)
	return result
}

// Subscribe registriert einen Callback für Live-Snapshots
func (s *Scraper) Subscribe(fn func(models.Snapshot)) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	s.subscribers = append(s.subscribers, fn)
}

// Run startet den Scrape-Loop (blockiert bis ctx Done)
func (s *Scraper) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	s.logger.Info("scraper starting", "interval", s.interval)

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("scraper stopped")
			return
		case <-ticker.C:
			s.scrapeAll(ctx)
		}
	}
}

// scrapeAll scrapt alle Targets parallel
func (s *Scraper) scrapeAll(ctx context.Context) {
	s.mu.RLock()
	targets := make([]AgentTarget, len(s.targets))
	copy(targets, s.targets)
	s.mu.RUnlock()

	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		go func(target AgentTarget) {
			defer wg.Done()
			if err := s.scrapeOne(ctx, target); err != nil {
				s.logger.Warn("scrape failed", "target", target.Address, "err", err)
			}
		}(t)
	}
	wg.Wait()
}

// scrapeOne scrapt einen einzelnen Agent
func (s *Scraper) scrapeOne(ctx context.Context, target AgentTarget) error {
	url := target.Address + "/snapshot"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusServiceUnavailable {
		return fmt.Errorf("agent not ready yet")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var snap models.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return fmt.Errorf("decode snapshot: %w", err)
	}

	// Alle Metriken in Storage schreiben
	records := make([]storage.Record, 0, len(snap.Metrics))
	for metric, value := range snap.Metrics {
		records = append(records, storage.Record{
			Timestamp: snap.Timestamp,
			Host:      snap.Host,
			Metric:    metric,
			Value:     value,
		})
	}
	s.store.WriteBatch(records)

	// Live-Cache aktualisieren (für Alerting)
	if s.liveCache != nil {
		s.liveCache.Update(snap)
	}

	// Live-Subscribers benachrichtigen
	s.subMu.RLock()
	subs := make([]func(models.Snapshot), len(s.subscribers))
	copy(subs, s.subscribers)
	s.subMu.RUnlock()

	for _, fn := range subs {
		go fn(snap)
	}

	return nil
}

// FetchHistory lädt die History eines Agents nach und schreibt sie in den Storage.
// Wird einmalig beim Hinzufügen eines neuen Agents aufgerufen.
func (s *Scraper) FetchHistory(ctx context.Context, target AgentTarget) error {
	url := target.Address + "/history"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var snaps []models.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snaps); err != nil {
		return err
	}

	s.logger.Info("fetched history from agent",
		"target", target.Address,
		"snapshots", len(snaps),
	)

	for _, snap := range snaps {
		for metric, value := range snap.Metrics {
			s.store.Write(storage.Record{
				Timestamp: snap.Timestamp,
				Host:      snap.Host,
				Metric:    metric,
				Value:     value,
			})
		}
	}
	return nil
}

// SetLiveCache registriert einen Cache für Live-Daten (für Alerting)
func (s *Scraper) SetLiveCache(c *LiveCache) {
	s.mu.Lock()
	s.liveCache = c
	s.mu.Unlock()
}
