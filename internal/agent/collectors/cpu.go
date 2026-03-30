package collectors

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pulsewatch/internal/shared/models"
)

// cpuStat speichert rohe Werte aus /proc/stat
type cpuStat struct {
	user    uint64
	nice    uint64
	system  uint64
	idle    uint64
	iowait  uint64
	irq     uint64
	softirq uint64
	steal   uint64
}

func (c cpuStat) total() uint64 {
	return c.user + c.nice + c.system + c.idle + c.iowait + c.irq + c.softirq + c.steal
}

func (c cpuStat) busy() uint64 {
	return c.total() - c.idle - c.iowait
}

// CPUCollector liest CPU-Metriken aus /proc/stat
type CPUCollector struct {
	mu       sync.Mutex
	prevStat cpuStat
	prevTime time.Time
}

func NewCPUCollector() *CPUCollector {
	c := &CPUCollector{}
	// Ersten Wert einlesen (Basis für Delta-Berechnung)
	stat, err := c.readProcStat()
	if err == nil {
		c.prevStat = stat
		c.prevTime = time.Now()
	}
	return c
}

func (c *CPUCollector) Name() string { return "cpu" }

func (c *CPUCollector) Collect() (map[string]float64, map[string]models.Tags, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	stat, err := c.readProcStat()
	if err != nil {
		return nil, nil, fmt.Errorf("cpu collect: %w", err)
	}

	// Delta-Berechnung (Differenz seit letztem Aufruf)
	deltaBusy := float64(stat.busy() - c.prevStat.busy())
	deltaTotal := float64(stat.total() - c.prevStat.total())
	deltaUser := float64((stat.user + stat.nice) - (c.prevStat.user + c.prevStat.nice))
	deltaSystem := float64(stat.system - c.prevStat.system)
	deltaIdle := float64((stat.idle + stat.iowait) - (c.prevStat.idle + c.prevStat.iowait))

	c.prevStat = stat
	c.prevTime = now

	if deltaTotal == 0 {
		return map[string]float64{
			models.MetricCPUUsage:  0,
			models.MetricCPUUser:   0,
			models.MetricCPUSystem: 0,
			models.MetricCPUIdle:   100,
		}, nil, nil
	}

	metrics := map[string]float64{
		models.MetricCPUUsage:  (deltaBusy / deltaTotal) * 100,
		models.MetricCPUUser:   (deltaUser / deltaTotal) * 100,
		models.MetricCPUSystem: (deltaSystem / deltaTotal) * 100,
		models.MetricCPUIdle:   (deltaIdle / deltaTotal) * 100,
	}

	return metrics, nil, nil
}

func (c *CPUCollector) readProcStat() (cpuStat, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return cpuStat{}, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 8 {
			return cpuStat{}, fmt.Errorf("unexpected /proc/stat format")
		}

		parseUint := func(s string) uint64 {
			v, _ := strconv.ParseUint(s, 10, 64)
			return v
		}

		return cpuStat{
			user:    parseUint(fields[1]),
			nice:    parseUint(fields[2]),
			system:  parseUint(fields[3]),
			idle:    parseUint(fields[4]),
			iowait:  parseUint(fields[5]),
			irq:     parseUint(fields[6]),
			softirq: parseUint(fields[7]),
			steal:   func() uint64 { if len(fields) > 8 { return parseUint(fields[8]) }; return 0 }(),
		}, nil
	}

	return cpuStat{}, fmt.Errorf("/proc/stat: cpu line not found")
}
