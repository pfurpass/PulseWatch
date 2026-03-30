package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/pulsewatch/internal/server/alerting"
)

// GET  /api/v1/alerts/active    → aktive Alerts
// GET  /api/v1/alerts/history   → Alert-History (letzte N)
// GET  /api/v1/alerts/rules     → Regeln anzeigen
// POST /api/v1/alerts/rules     → Regel hinzufügen
// GET  /api/v1/anomalies        → Anomalie-Scores

func (s *Server) handleAlertsActive(w http.ResponseWriter, r *http.Request) {
	if s.alertEngine == nil { writeJSON(w, map[string]any{"alerts": []any{}}); return }
	writeJSON(w, map[string]any{"alerts": s.alertEngine.ActiveAlerts()})
}

func (s *Server) handleAlertsHistory(w http.ResponseWriter, r *http.Request) {
	if s.alertEngine == nil { writeJSON(w, map[string]any{"events": []any{}}); return }
	n := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n2, err := strconv.Atoi(v); err == nil && n2 > 0 {
			n = n2
		}
	}
	writeJSON(w, map[string]any{"events": s.alertEngine.RecentHistory(n)})
}

func (s *Server) handleAlertsRules(w http.ResponseWriter, r *http.Request) {
	if s.alertEngine == nil { writeJSON(w, map[string]any{"rules": []any{}}); return }

	switch r.Method {
	case http.MethodGet:
		cfg := s.alertEngine.Config()
		writeJSON(w, map[string]any{"rules": cfg.Alerts, "channels": cfg.Channels})

	case http.MethodPost:
		var rule alerting.Rule
		if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		rule.Enabled = true
		cfg := s.alertEngine.Config()
		cfg.Alerts = append(cfg.Alerts, rule)
		s.alertEngine.UpdateConfig(cfg)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, rule)

	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleAnomalies(w http.ResponseWriter, r *http.Request) {
	if s.anomalyDetector == nil {
		writeJSON(w, map[string]any{"anomalies": []any{}, "scores": []any{}})
		return
	}
	host := r.URL.Query().Get("host")
	writeJSON(w, map[string]any{
		"anomalies": s.anomalyDetector.Anomalies(),
		"scores":    s.anomalyDetector.AllScores(host),
	})
}
