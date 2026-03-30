package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/pulsewatch/internal/agent/buffer"
	"github.com/pulsewatch/internal/agent/collectors"
	"github.com/pulsewatch/internal/shared/models"
	"github.com/pulsewatch/internal/ws"
)

const (
	defaultInterval = time.Second
	bufferSize      = 86400 // 24 h @ 1s
)

// Config konfiguriert den Agent
type Config struct {
	Interval   time.Duration
	ListenAddr string
}

func DefaultConfig() Config {
	return Config{
		Interval:   defaultInterval,
		ListenAddr: ":19998",
	}
}

// Agent ist der Kern des PulseWatch-Agents
type Agent struct {
	hostname  string
	interval  time.Duration
	registry  *collectors.Registry
	ringbuf   *buffer.RingBuffer
	hub       *wsHub
	logger    *slog.Logger
	startedAt time.Time
}

// New erstellt einen neuen Agent
func New(cfg Config) (*Agent, error) {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}

	interval := cfg.Interval
	if interval <= 0 {
		interval = defaultInterval
	}

	return &Agent{
		hostname:  hostname,
		interval:  interval,
		registry:  collectors.NewRegistry(),
		ringbuf:   buffer.New(bufferSize),
		hub:       newWsHub(),
		logger:    slog.Default(),
		startedAt: time.Now(),
	}, nil
}

// Run startet den Agent (blockiert bis ctx abgebrochen wird)
func (a *Agent) Run(ctx context.Context, listenAddr string) error {
	a.logger.Info("PulseWatch Agent starting",
		"host", a.hostname,
		"interval", a.interval,
		"addr", listenAddr,
	)

	go a.hub.run(ctx)
	go a.collectLoop(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/metrics",  a.handlePrometheus)
	mux.HandleFunc("/snapshot", a.handleSnapshot)
	mux.HandleFunc("/history",  a.handleHistory)
	mux.HandleFunc("/info",     a.handleInfo)
	mux.HandleFunc("/ws",       a.handleWebSocket)

	srv := &http.Server{
		Addr:    listenAddr,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutCtx)
	}()

	a.logger.Info("Agent HTTP server listening", "addr", listenAddr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("agent server: %w", err)
	}
	return nil
}

// collectLoop sammelt Metriken in festem Intervall
func (a *Agent) collectLoop(ctx context.Context) {
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			snap := a.collect(t)
			a.ringbuf.Push(snap)
			a.hub.broadcast(snap)
		}
	}
}

// collect führt alle Collectors aus und baut einen Snapshot
func (a *Agent) collect(t time.Time) models.Snapshot {
	snap := models.Snapshot{
		Timestamp: t,
		Host:      a.hostname,
		Metrics:   make(map[string]float64),
		Tags:      make(map[string]models.Tags),
	}

	for _, c := range a.registry.All() {
		metrics, tags, err := c.Collect()
		if err != nil {
			a.logger.Warn("collector error", "collector", c.Name(), "err", err)
			continue
		}
		for k, v := range metrics {
			snap.Metrics[k] = v
		}
		for k, v := range tags {
			snap.Tags[k] = v
		}
	}
	return snap
}

// ── HTTP Handler ──────────────────────────────────────────────────────────────

func (a *Agent) handlePrometheus(w http.ResponseWriter, r *http.Request) {
	snaps := a.ringbuf.Last(1)
	if len(snaps) == 0 {
		http.Error(w, "no data yet", http.StatusServiceUnavailable)
		return
	}

	snap := snaps[0]
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")

	var sb strings.Builder
	for metric, value := range snap.Metrics {
		promName, labelStr := toPrometheusFormat(metric, snap.Tags[metric])
		fmt.Fprintf(&sb, "# HELP %s PulseWatch metric\n# TYPE %s gauge\n", promName, promName)
		if labelStr != "" {
			fmt.Fprintf(&sb, "%s{host=%q,%s} %g %d\n", promName, a.hostname, labelStr, value, snap.Timestamp.UnixMilli())
		} else {
			fmt.Fprintf(&sb, "%s{host=%q} %g %d\n", promName, a.hostname, value, snap.Timestamp.UnixMilli())
		}
	}
	w.Write([]byte(sb.String()))
}

func (a *Agent) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	snaps := a.ringbuf.Last(1)
	if len(snaps) == 0 {
		http.Error(w, "no data yet", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, snaps[0])
}

func (a *Agent) handleHistory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, a.ringbuf.Last(300))
}

func (a *Agent) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, models.AgentInfo{
		Host:      a.hostname,
		Version:   "0.1.0",
		StartedAt: a.startedAt,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	})
}

func (a *Agent) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := ws.Upgrade(w, r)
	if err != nil {
		a.logger.Warn("ws upgrade failed", "err", err)
		return
	}

	client := &wsClient{
		hub:  a.hub,
		conn: conn,
		send: make(chan models.Snapshot, 64),
	}
	a.hub.register <- client

	// Replay: letzte 60 Sekunden sofort senden
	go func() {
		for _, snap := range a.ringbuf.Last(60) {
			client.send <- snap
		}
	}()

	go client.writePump()
	client.readPump()
}

// ── WebSocket Hub (Pub/Sub) ───────────────────────────────────────────────────

type wsHub struct {
	mu         sync.RWMutex
	clients    map[*wsClient]bool
	broadcast_ chan models.Snapshot
	register   chan *wsClient
	unregister chan *wsClient
}

func newWsHub() *wsHub {
	return &wsHub{
		clients:    make(map[*wsClient]bool),
		broadcast_: make(chan models.Snapshot, 128),
		register:   make(chan *wsClient, 16),
		unregister: make(chan *wsClient, 16),
	}
}

func (h *wsHub) broadcast(s models.Snapshot) {
	select {
	case h.broadcast_ <- s:
	default:
	}
}

func (h *wsHub) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case c := <-h.register:
			h.mu.Lock()
			h.clients[c] = true
			h.mu.Unlock()
		case c := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[c]; ok {
				delete(h.clients, c)
				close(c.send)
			}
			h.mu.Unlock()
		case snap := <-h.broadcast_:
			h.mu.RLock()
			for c := range h.clients {
				select {
				case c.send <- snap:
				default:
					go func(cl *wsClient) { h.unregister <- cl }(c)
				}
			}
			h.mu.RUnlock()
		}
	}
}

type wsClient struct {
	hub  *wsHub
	conn *ws.Conn
	send chan models.Snapshot
}

func (c *wsClient) writePump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()
	for snap := range c.send {
		if err := c.conn.WriteJSON(snap); err != nil {
			return
		}
	}
}

func (c *wsClient) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()
	for {
		opcode, _, err := c.conn.ReadFrame()
		if err != nil {
			return
		}
		if opcode == 0x08 { // Close frame
			return
		}
	}
}

// ── Hilfsfunktionen ───────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func toPrometheusFormat(metric string, tags models.Tags) (name, labels string) {
	name = "pulsewatch_" + strings.NewReplacer(".", "_", "-", "_").Replace(metric)
	if len(tags) == 0 {
		return name, ""
	}
	parts := make([]string, 0, len(tags))
	for k, v := range tags {
		parts = append(parts, fmt.Sprintf("%s=%q", k, v))
	}
	return name, strings.Join(parts, ",")
}
