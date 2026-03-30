package collectors

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/pulsewatch/internal/shared/models"
)

// PluginConfig beschreibt ein externes Collector-Plugin
type PluginConfig struct {
	Name    string   `json:"name"`     // Eindeutiger Name
	Command string   `json:"command"`  // Pfad zur ausführbaren Datei
	Args    []string `json:"args"`     // Argumente
	Timeout time.Duration `json:"timeout"` // Max. Laufzeit (default 5s)
}

// PluginCollector führt externe Programme aus und liest ihre JSON-Ausgabe
//
// Das Plugin muss auf stdout ein JSON-Objekt ausgeben:
//
//	{"my.metric.name": 42.0, "another.metric": 1.23}
//
// Fehler können auf stderr geloggt werden (werden ignoriert).
type PluginCollector struct {
	cfg     PluginConfig
	mu      sync.Mutex
	last    map[string]float64
	lastErr error
}

func NewPluginCollector(cfg PluginConfig) *PluginCollector {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	return &PluginCollector{cfg: cfg}
}

func (p *PluginCollector) Name() string {
	return "plugin:" + p.cfg.Name
}

func (p *PluginCollector) Collect() (map[string]float64, map[string]models.Tags, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), p.cfg.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, p.cfg.Command, p.cfg.Args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, nil, fmt.Errorf("plugin %s: %w", p.cfg.Name, err)
	}

	var raw map[string]float64
	if err := json.Unmarshal(out, &raw); err != nil {
		// Versuche auch string-Werte
		var rawStr map[string]any
		if err2 := json.Unmarshal(out, &rawStr); err2 != nil {
			return nil, nil, fmt.Errorf("plugin %s: invalid JSON output: %w", p.cfg.Name, err)
		}
		raw = make(map[string]float64, len(rawStr))
		for k, v := range rawStr {
			switch n := v.(type) {
			case float64:
				raw[k] = n
			case int:
				raw[k] = float64(n)
			}
		}
	}

	// Metric-Namen mit Plugin-Präfix versehen
	result := make(map[string]float64, len(raw))
	for k, v := range raw {
		key := k
		if !strings.HasPrefix(k, "plugin.") {
			key = "plugin." + p.cfg.Name + "." + k
		}
		result[key] = v
	}

	p.last = result
	return result, nil, nil
}

// PluginRegistry verwaltet alle registrierten Plugin-Collectors
type PluginRegistry struct {
	mu       sync.RWMutex
	plugins  map[string]*PluginCollector
}

func NewPluginRegistry() *PluginRegistry {
	return &PluginRegistry{plugins: make(map[string]*PluginCollector)}
}

func (r *PluginRegistry) Register(cfg PluginConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.plugins[cfg.Name] = NewPluginCollector(cfg)
}

func (r *PluginRegistry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.plugins, name)
}

func (r *PluginRegistry) All() []Collector {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Collector, 0, len(r.plugins))
	for _, p := range r.plugins {
		result = append(result, p)
	}
	return result
}
