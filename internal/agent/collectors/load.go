package collectors

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/pulsewatch/internal/shared/models"
)

// LoadCollector liest Load Average aus /proc/loadavg
type LoadCollector struct{}

func NewLoadCollector() *LoadCollector { return &LoadCollector{} }

func (l *LoadCollector) Name() string { return "load" }

func (l *LoadCollector) Collect() (map[string]float64, map[string]models.Tags, error) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return nil, nil, fmt.Errorf("load collect: %w", err)
	}

	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return nil, nil, fmt.Errorf("unexpected /proc/loadavg format")
	}

	parse := func(s string) float64 {
		v, _ := strconv.ParseFloat(s, 64)
		return v
	}

	return map[string]float64{
		models.MetricLoadAvg1:  parse(fields[0]),
		models.MetricLoadAvg5:  parse(fields[1]),
		models.MetricLoadAvg15: parse(fields[2]),
	}, nil, nil
}
