package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/pulsewatch/internal/server/prom"
)

// GET  /api/v1/prom/targets    → Prometheus-Targets anzeigen
// POST /api/v1/prom/targets    → Target hinzufügen
// DEL  /api/v1/prom/targets?id= → Target entfernen

func (s *Server) handlePromTargets(w http.ResponseWriter, r *http.Request) {
	if s.promScraper == nil {
		writeJSON(w, map[string]any{"targets": []any{}})
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"targets": s.promScraper.Targets()})

	case http.MethodPost:
		var body struct {
			Name     string `json:"name"`
			URL      string `json:"url"`
			Interval string `json:"interval"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if body.URL == "" {
			http.Error(w, "url is required", http.StatusBadRequest)
			return
		}
		interval := 15 * time.Second
		if body.Interval != "" {
			if d, err := time.ParseDuration(body.Interval); err == nil {
				interval = d
			}
		}
		t := prom.Target{Name: body.Name, URL: body.URL, Interval: interval}
		s.promScraper.AddTarget(s.ctx, t)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, t)

	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		s.promScraper.RemoveTarget(id)
		writeJSON(w, map[string]string{"status": "removed"})

	default:
		methodNotAllowed(w)
	}
}
