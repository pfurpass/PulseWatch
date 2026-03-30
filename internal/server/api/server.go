package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/pulsewatch/internal/server/alerting"
	"github.com/pulsewatch/internal/server/anomaly"
	"github.com/pulsewatch/internal/server/auth"
	"github.com/pulsewatch/internal/server/ingest"
	"github.com/pulsewatch/internal/server/prom"
	"github.com/pulsewatch/internal/server/storage"
	"github.com/pulsewatch/internal/shared/models"
	"github.com/pulsewatch/internal/ws"
)

// Server ist der HTTP/WebSocket API-Server
type Server struct {
	store           *storage.Engine
	scraper         *ingest.Scraper
	hub             *liveHub
	logger          *slog.Logger
	version         string
	startedAt       time.Time
	ctx             context.Context

	// Phase 5 & 6
	alertEngine     *alerting.Engine
	anomalyDetector *anomaly.Detector
	authManager     *auth.Manager
	promScraper     *prom.Scraper
	liveCache       *ingest.LiveCache
}

// Options für den Server
type Options struct {
	AlertEngine     *alerting.Engine
	AnomalyDetector *anomaly.Detector
	AuthManager     *auth.Manager
	PromScraper     *prom.Scraper
	LiveCache       *ingest.LiveCache
}

// New erstellt einen neuen API-Server
func New(store *storage.Engine, scraper *ingest.Scraper, opts Options) *Server {
	srv := &Server{
		store:           store,
		scraper:         scraper,
		hub:             newLiveHub(),
		logger:          slog.Default(),
		version:         "0.2.0",
		startedAt:       time.Now(),
		alertEngine:     opts.AlertEngine,
		anomalyDetector: opts.AnomalyDetector,
		authManager:     opts.AuthManager,
		promScraper:     opts.PromScraper,
		liveCache:       opts.LiveCache,
	}

	// Live-Snapshots vom Scraper weiterleiten
	scraper.Subscribe(func(snap models.Snapshot) {
		srv.hub.broadcast(snap)
		// Anomalie-Detektion für jede Metrik
		if srv.anomalyDetector != nil {
			for metric, value := range snap.Metrics {
				srv.anomalyDetector.Observe(snap.Host, metric, value, snap.Timestamp)
			}
		}
	})

	return srv
}

// Handler gibt den HTTP-Handler zurück
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Frontend
	mux.HandleFunc("/", staticHandler)

	// Auth (immer öffentlich zugänglich)
	if s.authManager != nil {
		mux.HandleFunc("/api/v1/auth/login",  s.authManager.HandleLogin)
		mux.HandleFunc("/api/v1/auth/logout", s.authManager.HandleLogout)
		mux.HandleFunc("/api/v1/auth/me",     s.wrap(auth.RoleViewer, http.HandlerFunc(s.authManager.HandleMe)))
		mux.HandleFunc("/api/v1/auth/users",  s.wrap(auth.RoleAdmin,  http.HandlerFunc(s.authManager.HandleUsers)))
	}

	// Core API (Viewer)
	mux.HandleFunc("/api/v1/hosts",           s.wrap(auth.RoleViewer, http.HandlerFunc(s.handleHosts)))
	mux.HandleFunc("/api/v1/hosts/",          s.wrap(auth.RoleViewer, http.HandlerFunc(s.handleHostMetrics)))
	mux.HandleFunc("/api/v1/query",           s.wrap(auth.RoleViewer, http.HandlerFunc(s.handleQuery)))
	mux.HandleFunc("/api/v1/status",          http.HandlerFunc(s.handleStatus)) // public — health check
	mux.HandleFunc("/api/v1/stream",          s.wrap(auth.RoleViewer, http.HandlerFunc(s.handleStream)))

	// Targets (Editor)
	mux.HandleFunc("/api/v1/targets",         s.wrap(auth.RoleEditor, http.HandlerFunc(s.handleTargets)))

	// Alerts (Viewer lesen, Editor schreiben → handler intern unterscheidet)
	mux.HandleFunc("/api/v1/alerts/active",   s.wrap(auth.RoleViewer, http.HandlerFunc(s.handleAlertsActive)))
	mux.HandleFunc("/api/v1/alerts/history",  s.wrap(auth.RoleViewer, http.HandlerFunc(s.handleAlertsHistory)))
	mux.HandleFunc("/api/v1/alerts/rules",    s.wrap(auth.RoleViewer, http.HandlerFunc(s.handleAlertsRules)))

	// Anomalies (Viewer)
	mux.HandleFunc("/api/v1/anomalies",       s.wrap(auth.RoleViewer, http.HandlerFunc(s.handleAnomalies)))

	// Prometheus Targets (Editor)
	mux.HandleFunc("/api/v1/prom/targets",    s.wrap(auth.RoleEditor, http.HandlerFunc(s.handlePromTargets)))

	return corsMiddleware(logMiddleware(mux, s.logger))
}

// wrap wickelt einen Handler in Auth-Middleware ein
func (s *Server) wrap(role auth.Role, next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.authManager != nil && s.authManager.Enabled() {
			s.authManager.Middleware(role, next).ServeHTTP(w, r)
		} else {
			next.ServeHTTP(w, r)
		}
	}
}

// Start startet den Live-Hub
func (s *Server) Start(ctx context.Context) {
	s.ctx = ctx
	go s.hub.run(ctx)
}

// ── Handler ───────────────────────────────────────────────────────────────

func (s *Server) handleHosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { methodNotAllowed(w); return }
	writeJSON(w, map[string]any{"hosts": s.store.Hosts()})
}

func (s *Server) handleHostMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { methodNotAllowed(w); return }
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/hosts/"), "/")
	if len(parts) < 1 || parts[0] == "" {
		http.Error(w, "missing host", http.StatusBadRequest); return
	}
	writeJSON(w, map[string]any{"host": parts[0], "metrics": s.store.Metrics(parts[0])})
}

func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { methodNotAllowed(w); return }
	q := r.URL.Query()
	host, metric := q.Get("host"), q.Get("metric")
	if host == "" || metric == "" {
		http.Error(w, "host and metric are required", http.StatusBadRequest); return
	}
	now := time.Now()
	from, to := now.Add(-time.Hour), now
	if v := q.Get("from"); v != "" { if t, err := parseTime(v); err == nil { from = t } }
	if v := q.Get("to");   v != "" { if t, err := parseTime(v); err == nil { to = t   } }
	maxPoints := 500
	if v := q.Get("points"); v != "" { fmt.Sscanf(v, "%d", &maxPoints) }
	if maxPoints < 2   { maxPoints = 2 }
	if maxPoints > 10000 { maxPoints = 10000 }

	result, err := s.store.Query(storage.Query{Host:host, Metric:metric, From:from, To:to, MaxPoints:maxPoints})
	if err != nil { http.Error(w, err.Error(), http.StatusInternalServerError); return }

	type pt struct { T int64 `json:"t"`; V float64 `json:"v"` }
	pts := make([]pt, len(result.Points))
	for i, p := range result.Points { pts[i] = pt{p.Timestamp.UnixMilli(), p.Value} }

	// Anomalie-Score für diese Metrik anhängen
	var anomalyScore *anomaly.Score
	if s.anomalyDetector != nil {
		if sc, ok := s.anomalyDetector.GetScore(host, metric); ok {
			anomalyScore = sc
		}
	}

	writeJSON(w, map[string]any{
		"host": result.Host, "metric": result.Metric,
		"from": from.UnixMilli(), "to": to.UnixMilli(),
		"points": pts, "count": len(pts),
		"anomaly": anomalyScore,
	})
}

func (s *Server) handleTargets(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"targets": s.scraper.Targets()})
	case http.MethodPost:
		var body struct{ Address, Name string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil { http.Error(w, "invalid JSON", http.StatusBadRequest); return }
		if body.Address == "" { http.Error(w, "address required", http.StatusBadRequest); return }
		t := ingest.AgentTarget{Name:body.Name, Address:body.Address}
		s.scraper.AddTarget(t)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			s.scraper.FetchHistory(ctx, t)
		}()
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]string{"status":"added","address":body.Address})
	case http.MethodDelete:
		s.scraper.RemoveTarget(r.URL.Query().Get("address"))
		writeJSON(w, map[string]string{"status":"removed"})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	activeAlerts := 0
	if s.alertEngine != nil { activeAlerts = len(s.alertEngine.ActiveAlerts()) }
	anomalyCount := 0
	if s.anomalyDetector != nil { anomalyCount = len(s.anomalyDetector.Anomalies()) }
	authEnabled := s.authManager != nil && s.authManager.Enabled()

	writeJSON(w, map[string]any{
		"version":       s.version,
		"started_at":    s.startedAt,
		"uptime_sec":    int(time.Since(s.startedAt).Seconds()),
		"hosts":         len(s.store.Hosts()),
		"targets":       len(s.scraper.Targets()),
		"ws_clients":    s.hub.clientCount(),
		"active_alerts": activeAlerts,
		"anomalies":     anomalyCount,
		"auth_enabled":  authEnabled,
	})
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	hostFilter := r.URL.Query().Get("host")
	conn, err := ws.Upgrade(w, r)
	if err != nil { s.logger.Warn("ws upgrade failed", "err", err); return }

	client := &liveClient{hub:s.hub, conn:conn, send:make(chan models.Snapshot,64), hostFilter:hostFilter}
	s.hub.register <- client
	go client.writePump()
	client.readPump()
}

// ── Live Hub ──────────────────────────────────────────────────────────────

type liveHub struct {
	mu         sync.RWMutex
	clients    map[*liveClient]bool
	broadcast_ chan models.Snapshot
	register   chan *liveClient
	unregister chan *liveClient
}

func newLiveHub() *liveHub {
	return &liveHub{
		clients:    make(map[*liveClient]bool),
		broadcast_: make(chan models.Snapshot,256),
		register:   make(chan *liveClient,16),
		unregister: make(chan *liveClient,16),
	}
}

func (h *liveHub) broadcast(s models.Snapshot) {
	select { case h.broadcast_ <- s: default: }
}
func (h *liveHub) clientCount() int {
	h.mu.RLock(); defer h.mu.RUnlock(); return len(h.clients)
}
func (h *liveHub) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done(): return
		case c := <-h.register:
			h.mu.Lock(); h.clients[c]=true; h.mu.Unlock()
		case c := <-h.unregister:
			h.mu.Lock()
			if _,ok:=h.clients[c]; ok { delete(h.clients,c); close(c.send) }
			h.mu.Unlock()
		case snap := <-h.broadcast_:
			h.mu.RLock()
			for c := range h.clients {
				if c.hostFilter!="" && snap.Host!=c.hostFilter { continue }
				select { case c.send <- snap: default: go func(cl *liveClient){h.unregister<-cl}(c) }
			}
			h.mu.RUnlock()
		}
	}
}

type liveClient struct {
	hub        *liveHub
	conn       *ws.Conn
	send       chan models.Snapshot
	hostFilter string
}

func (c *liveClient) writePump() {
	defer func() { c.hub.unregister <- c; c.conn.Close() }()
	for snap := range c.send {
		if err := c.conn.WriteJSON(snap); err != nil { return }
	}
}
func (c *liveClient) readPump() {
	defer func() { c.hub.unregister <- c; c.conn.Close() }()
	for {
		op, _, err := c.conn.ReadFrame()
		if err != nil || op==0x08 { return }
	}
}

// ── Middleware ────────────────────────────────────────────────────────────

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin","*")
		w.Header().Set("Access-Control-Allow-Methods","GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers","Content-Type, Authorization")
		if r.Method==http.MethodOptions { w.WriteHeader(http.StatusNoContent); return }
		next.ServeHTTP(w,r)
	})
}

func logMiddleware(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		quiet := r.URL.Path=="/api/v1/stream" || strings.HasSuffix(r.URL.Path,"/metrics")
		if !quiet { logger.Debug("request","method",r.Method,"path",r.URL.Path) }
		next.ServeHTTP(w,r)
	})
}

// ── Helpers ───────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type","application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("","  ")
	enc.Encode(v)
}
func methodNotAllowed(w http.ResponseWriter) { http.Error(w,"method not allowed",http.StatusMethodNotAllowed) }
func parseTime(s string) (time.Time, error) {
	if t,err:=time.Parse(time.RFC3339,s); err==nil { return t,nil }
	if strings.HasPrefix(s,"-") { if d,err:=time.ParseDuration(s[1:]); err==nil { return time.Now().Add(-d),nil } }
	var ms int64
	if _,err:=fmt.Sscanf(s,"%d",&ms); err==nil && ms>1e10 { return time.UnixMilli(ms),nil }
	var sec int64
	if _,err:=fmt.Sscanf(s,"%d",&sec); err==nil { return time.Unix(sec,0),nil }
	return time.Time{}, fmt.Errorf("cannot parse time: %q",s)
}
