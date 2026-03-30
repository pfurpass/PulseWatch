package collectors_test

import (
	"os"
	"testing"

	"github.com/pulsewatch/internal/agent/collectors"
	"github.com/pulsewatch/internal/shared/models"
)

func skipIfNoProcFS(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skipf("/proc not available (%s missing), skipping", path)
	}
}

func TestCPUCollector(t *testing.T) {
	skipIfNoProcFS(t, "/proc/stat")

	c := collectors.NewCPUCollector()
	metrics, _, err := c.Collect()
	if err != nil {
		t.Fatalf("CPUCollector.Collect() error: %v", err)
	}

	required := []string{
		models.MetricCPUUsage,
		models.MetricCPUUser,
		models.MetricCPUSystem,
		models.MetricCPUIdle,
	}
	for _, key := range required {
		v, ok := metrics[key]
		if !ok {
			t.Errorf("missing metric: %s", key)
		}
		if v < 0 || v > 100 {
			t.Errorf("%s = %v, expected 0–100", key, v)
		}
	}

	total := metrics[models.MetricCPUUser] + metrics[models.MetricCPUSystem] + metrics[models.MetricCPUIdle]
	if total < 99 || total > 101 {
		t.Errorf("CPU percentages don't add up to ~100: user+system+idle = %v", total)
	}
}

func TestMemCollector(t *testing.T) {
	skipIfNoProcFS(t, "/proc/meminfo")

	c := collectors.NewMemCollector()
	metrics, _, err := c.Collect()
	if err != nil {
		t.Fatalf("MemCollector.Collect() error: %v", err)
	}

	if metrics[models.MetricMemTotal] <= 0 {
		t.Errorf("mem.total_bytes should be > 0, got %v", metrics[models.MetricMemTotal])
	}
	if metrics[models.MetricMemUsedPercent] < 0 || metrics[models.MetricMemUsedPercent] > 100 {
		t.Errorf("mem.used_percent out of range: %v", metrics[models.MetricMemUsedPercent])
	}
	// Used + Free ≤ Total (Available kann kleiner sein als Free bei Caches)
	if metrics[models.MetricMemUsed] > metrics[models.MetricMemTotal] {
		t.Errorf("mem.used (%v) > mem.total (%v)", metrics[models.MetricMemUsed], metrics[models.MetricMemTotal])
	}
}

func TestNetCollector(t *testing.T) {
	skipIfNoProcFS(t, "/proc/net/dev")

	c := collectors.NewNetCollector()
	metrics, _, err := c.Collect()
	if err != nil {
		t.Fatalf("NetCollector.Collect() error: %v", err)
	}

	// Mindestens eine Netzwerk-Metrik sollte existieren
	if len(metrics) == 0 {
		t.Log("no network metrics (might be OK in isolated environment)")
	}
	for k, v := range metrics {
		if v < 0 {
			t.Errorf("metric %s is negative: %v", k, v)
		}
	}
}

func TestLoadCollector(t *testing.T) {
	skipIfNoProcFS(t, "/proc/loadavg")

	c := collectors.NewLoadCollector()
	metrics, _, err := c.Collect()
	if err != nil {
		t.Fatalf("LoadCollector.Collect() error: %v", err)
	}

	for _, key := range []string{models.MetricLoadAvg1, models.MetricLoadAvg5, models.MetricLoadAvg15} {
		v, ok := metrics[key]
		if !ok {
			t.Errorf("missing metric: %s", key)
		}
		if v < 0 {
			t.Errorf("%s is negative: %v", key, v)
		}
	}
}

func TestRegistry(t *testing.T) {
	r := collectors.NewRegistry()
	if len(r.All()) == 0 {
		t.Fatal("registry should have collectors")
	}

	names := make(map[string]bool)
	for _, c := range r.All() {
		if names[c.Name()] {
			t.Errorf("duplicate collector name: %s", c.Name())
		}
		names[c.Name()] = true
	}
}
