package prom_test

import (
	"strings"
	"testing"

	"github.com/pulsewatch/internal/server/prom"
)

func TestParsePrometheusText(t *testing.T) {
	input := `
# HELP go_goroutines Number of goroutines
# TYPE go_goroutines gauge
go_goroutines 42

# HELP process_cpu_seconds_total Total CPU seconds
# TYPE process_cpu_seconds_total counter
process_cpu_seconds_total 1.23

# HELP http_requests_total HTTP requests
# TYPE http_requests_total counter
http_requests_total{method="GET",status="200"} 100
http_requests_total{method="POST",status="500"} 5

# HELP memory_bytes Memory usage
memory_bytes 1.073741824e+09

# Comment only line
go_memstats_alloc_bytes 2.5e+06
`
	metrics := prom.ParseText(strings.NewReader(input))

	cases := []struct {
		name  string
		want  float64
	}{
		{"go_goroutines", 42},
		{"process_cpu_seconds_total", 1.23},
		{"memory_bytes", 1.073741824e9},
		{"go_memstats_alloc_bytes", 2.5e6},
	}

	for _, tc := range cases {
		got, ok := metrics[tc.name]
		if !ok {
			t.Errorf("missing metric %q", tc.name)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}

	// Labeled metrics: mindestens einer der http_requests sollte da sein
	found := false
	for k := range metrics {
		if strings.HasPrefix(k, "http_requests_total") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected http_requests_total metric")
	}
}

func TestParsePrometheusText_Empty(t *testing.T) {
	metrics := prom.ParseText(strings.NewReader(""))
	if len(metrics) != 0 {
		t.Errorf("expected empty result, got %d metrics", len(metrics))
	}
}

func TestParsePrometheusText_CommentsOnly(t *testing.T) {
	input := "# HELP foo bar\n# TYPE foo gauge\n"
	metrics := prom.ParseText(strings.NewReader(input))
	if len(metrics) != 0 {
		t.Errorf("expected 0 metrics from comment-only input, got %d", len(metrics))
	}
}

func TestParsePrometheusText_InvalidLines(t *testing.T) {
	input := `
good_metric 1.0
bad line without value
another_good 2.5
`
	metrics := prom.ParseText(strings.NewReader(input))
	if metrics["good_metric"] != 1.0 {
		t.Errorf("good_metric: got %v, want 1.0", metrics["good_metric"])
	}
	if metrics["another_good"] != 2.5 {
		t.Errorf("another_good: got %v, want 2.5", metrics["another_good"])
	}
}

func TestParsePrometheusText_Timestamp(t *testing.T) {
	// Timestamps werden ignoriert, Wert trotzdem gelesen
	input := "metric_with_ts 99.9 1609459200000\n"
	metrics := prom.ParseText(strings.NewReader(input))
	if metrics["metric_with_ts"] != 99.9 {
		t.Errorf("got %v, want 99.9", metrics["metric_with_ts"])
	}
}
