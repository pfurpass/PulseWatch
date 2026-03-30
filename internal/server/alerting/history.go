package alerting

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AlertEvent ist ein Eintrag in der Alert-History
type AlertEvent struct {
	ID        string     `json:"id"`
	RuleName  string     `json:"rule_name"`
	Host      string     `json:"host"`
	Metric    string     `json:"metric"`
	State     AlertState `json:"state"`
	Value     float64    `json:"value"`
	Threshold float64    `json:"threshold"`
	Severity  Severity   `json:"severity"`
	FiredAt   time.Time  `json:"fired_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	Message   string     `json:"message"`
}

// History speichert Alert-Events
type History struct {
	mu      sync.RWMutex
	events  []AlertEvent
	maxSize int
	path    string
}

func NewHistory(dataDir string, maxSize int) *History {
	h := &History{
		events:  make([]AlertEvent, 0, maxSize),
		maxSize: maxSize,
		path:    filepath.Join(dataDir, "alert_history.json"),
	}
	h.load()
	return h
}

// Add fügt ein Event hinzu (neueste zuerst)
func (h *History) Add(e AlertEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Neueste zuerst
	h.events = append([]AlertEvent{e}, h.events...)
	if len(h.events) > h.maxSize {
		h.events = h.events[:h.maxSize]
	}
	h.persist()
}

// Resolve markiert ein Event als aufgelöst
func (h *History) Resolve(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	for i := range h.events {
		if h.events[i].ID == id && h.events[i].ResolvedAt == nil {
			h.events[i].ResolvedAt = &now
			h.events[i].State = StateResolved
		}
	}
	h.persist()
}

// Recent gibt die letzten N Events zurück
func (h *History) Recent(n int) []AlertEvent {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if n > len(h.events) {
		n = len(h.events)
	}
	result := make([]AlertEvent, n)
	copy(result, h.events[:n])
	return result
}

// Active gibt aktuell feuernde Alerts zurück
func (h *History) Active() []AlertEvent {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var result []AlertEvent
	seen := make(map[string]bool)
	for _, e := range h.events {
		key := e.RuleName + "\x00" + e.Host
		if !seen[key] && e.State == StateFiring {
			result = append(result, e)
			seen[key] = true
		}
	}
	return result
}

func (h *History) load() {
	f, err := os.Open(h.path)
	if err != nil {
		return
	}
	defer f.Close()
	json.NewDecoder(f).Decode(&h.events)
}

func (h *History) persist() {
	os.MkdirAll(filepath.Dir(h.path), 0o755)
	f, err := os.Create(h.path)
	if err != nil {
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	enc.Encode(h.events)
}
