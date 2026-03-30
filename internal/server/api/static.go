package api

import (
	_ "embed"
	"net/http"
	"strings"
)

//go:embed web/index.html
var indexHTML []byte

// staticHandler serviert das eingebettete Frontend
func staticHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && !strings.HasSuffix(r.URL.Path, ".html") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(indexHTML)
}
