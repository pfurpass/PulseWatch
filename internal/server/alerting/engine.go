package alerting

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"
)

const evalInterval = 15 * time.Second

// instanceKey identifiziert eine Alert-Instanz eindeutig
type instanceKey struct {
	ruleName string
	host     string
}

// instance verfolgt den Zustand einer einzelnen Alert-Instanz
type instance struct {
	state      AlertState
	pendingSince time.Time
	lastEventID  string
}

// MetricProvider liefert den letzten bekannten Wert einer Metrik
type MetricProvider interface {
	LastValue(host, metric string) (float64, bool)
}

// Engine wertet Alert-Regeln aus
type Engine struct {
	mu        sync.RWMutex
	cfg       *RulesConfig
	instances map[instanceKey]*instance
	history   *History
	notifier  *Notifier
	provider  MetricProvider
	logger    *slog.Logger
}

// New erstellt eine neue Alert-Engine
func New(cfg *RulesConfig, provider MetricProvider, history *History) *Engine {
	return &Engine{
		cfg:       cfg,
		instances: make(map[instanceKey]*instance),
		history:   history,
		notifier:  NewNotifier(cfg.Channels),
		provider:  provider,
		logger:    slog.Default(),
	}
}

// UpdateConfig aktualisiert Regeln zur Laufzeit
func (e *Engine) UpdateConfig(cfg *RulesConfig) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cfg = cfg
	e.notifier = NewNotifier(cfg.Channels)
}

// Config gibt die aktuelle Konfiguration zurück
func (e *Engine) Config() *RulesConfig {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cfg
}

// Run startet den Evaluierungs-Loop
func (e *Engine) Run(ctx context.Context, hosts func() []string) {
	ticker := time.NewTicker(evalInterval)
	defer ticker.Stop()

	e.logger.Info("alert engine started", "interval", evalInterval)

	for {
		select {
		case <-ctx.Done():
			e.logger.Info("alert engine stopped")
			return
		case <-ticker.C:
			e.evalAll(hosts())
		}
	}
}

// evalAll wertet alle Regeln für alle Hosts aus
func (e *Engine) evalAll(hosts []string) {
	e.mu.RLock()
	rules := make([]Rule, len(e.cfg.Alerts))
	copy(rules, e.cfg.Alerts)
	e.mu.RUnlock()

	now := time.Now()

	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}

		targetHosts := hosts
		if rule.Host != "*" && rule.Host != "" {
			targetHosts = []string{rule.Host}
		}

		for _, host := range targetHosts {
			e.evalRule(rule, host, now)
		}
	}
}

func (e *Engine) evalRule(rule Rule, host string, now time.Time) {
	value, ok := e.provider.LastValue(host, rule.Metric)
	if !ok {
		return // Keine Daten — nicht auswerten
	}

	key := instanceKey{ruleName: rule.Name, host: host}
	firing := evaluate(rule.Op, value, rule.Threshold)

	e.mu.Lock()
	inst, exists := e.instances[key]
	if !exists {
		inst = &instance{state: StateOK}
		e.instances[key] = inst
	}

	prevState := inst.state

	switch {
	case firing && prevState == StateOK:
		// Schwelle überschritten → Pending
		inst.state = StatePending
		inst.pendingSince = now

	case firing && prevState == StatePending:
		// Noch im Pending — hat die For-Dauer gewartet?
		if now.Sub(inst.pendingSince) >= rule.For.Duration {
			inst.state = StateFiring
			eventID := randID()
			inst.lastEventID = eventID
			e.mu.Unlock()

			event := AlertEvent{
				ID:        eventID,
				RuleName:  rule.Name,
				Host:      host,
				Metric:    rule.Metric,
				State:     StateFiring,
				Value:     value,
				Threshold: rule.Threshold,
				Severity:  rule.Severity,
				FiredAt:   now,
				Message:   fmt.Sprintf("%s on %s: %s %.2f %s %.2f", rule.Name, host, rule.Metric, value, rule.Op, rule.Threshold),
			}
			e.history.Add(event)
			e.notifier.Send(event, rule.Notify)

			e.logger.Warn("alert FIRING",
				"rule", rule.Name, "host", host,
				"metric", rule.Metric,
				"value", fmt.Sprintf("%.3f", value),
				"threshold", rule.Threshold,
			)
			return
		}

	case firing && prevState == StateFiring:
		// Bleibt firing — nichts tun

	case !firing && (prevState == StateFiring || prevState == StatePending):
		// Bedingung nicht mehr erfüllt → Resolved
		prevID := inst.lastEventID
		inst.state = StateOK
		inst.pendingSince = time.Time{}
		e.mu.Unlock()

		if prevState == StateFiring && prevID != "" {
			e.history.Resolve(prevID)
			e.logger.Info("alert RESOLVED", "rule", rule.Name, "host", host)
		}
		return
	}

	e.mu.Unlock()
}

// ActiveAlerts gibt aktuell feuernde Alerts zurück
func (e *Engine) ActiveAlerts() []AlertEvent {
	return e.history.Active()
}

// RecentHistory gibt die letzten N Alert-Events zurück
func (e *Engine) RecentHistory(n int) []AlertEvent {
	return e.history.Recent(n)
}

func randID() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return string(b)
}
