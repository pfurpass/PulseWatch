package collectors

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/pulsewatch/internal/shared/models"
)

// MemCollector liest RAM-Metriken aus /proc/meminfo
type MemCollector struct{}

func NewMemCollector() *MemCollector { return &MemCollector{} }

func (m *MemCollector) Name() string { return "memory" }

func (m *MemCollector) Collect() (map[string]float64, map[string]models.Tags, error) {
	info, err := m.readProcMeminfo()
	if err != nil {
		return nil, nil, fmt.Errorf("mem collect: %w", err)
	}

	total := info["MemTotal"]
	free := info["MemFree"]
	available := info["MemAvailable"]
	cached := info["Cached"] + info["SReclaimable"]
	buffers := info["Buffers"]

	// "Used" = Total - Available (praxisnäher als Total - Free)
	used := total - available
	if used < 0 {
		used = 0
	}

	usedPercent := 0.0
	if total > 0 {
		usedPercent = (used / total) * 100
	}

	metrics := map[string]float64{
		models.MetricMemTotal:       total * 1024, // kB → Bytes
		models.MetricMemUsed:        used * 1024,
		models.MetricMemFree:        free * 1024,
		models.MetricMemCached:      cached * 1024,
		models.MetricMemBuffers:     buffers * 1024,
		models.MetricMemUsedPercent: usedPercent,
	}

	return metrics, nil, nil
}

func (m *MemCollector) readProcMeminfo() (map[string]float64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	result := make(map[string]float64)
	scanner := bufio.NewScanner(f)

	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		// Key: "MemTotal:" → "MemTotal"
		key := strings.TrimSuffix(parts[0], ":")
		val, err := strconv.ParseFloat(parts[1], 64)
		if err != nil {
			continue
		}
		result[key] = val
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return result, nil
}
