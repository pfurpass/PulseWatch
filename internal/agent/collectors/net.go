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

type netStat struct {
	rxBytes   uint64
	rxPackets uint64
	txBytes   uint64
	txPackets uint64
}

// NetCollector liest Netzwerk-Metriken aus /proc/net/dev
type NetCollector struct {
	mu        sync.Mutex
	prevStats map[string]netStat
	prevTime  time.Time
}

func NewNetCollector() *NetCollector {
	c := &NetCollector{
		prevStats: make(map[string]netStat),
	}
	stats, err := c.readProcNetDev()
	if err == nil {
		c.prevStats = stats
		c.prevTime = time.Now()
	}
	return c
}

func (n *NetCollector) Name() string { return "network" }

func (n *NetCollector) Collect() (map[string]float64, map[string]models.Tags, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	now := time.Now()
	stats, err := n.readProcNetDev()
	if err != nil {
		return nil, nil, fmt.Errorf("net collect: %w", err)
	}

	elapsed := now.Sub(n.prevTime).Seconds()
	if elapsed <= 0 {
		elapsed = 1
	}

	metrics := make(map[string]float64)
	tags := make(map[string]models.Tags)

	for iface, cur := range stats {
		prev, ok := n.prevStats[iface]
		if !ok {
			continue
		}

		// Loopback überspringen
		if iface == "lo" {
			continue
		}

		rxBytesPerSec := float64(cur.rxBytes-prev.rxBytes) / elapsed
		txBytesPerSec := float64(cur.txBytes-prev.txBytes) / elapsed
		rxPacketsPerSec := float64(cur.rxPackets-prev.rxPackets) / elapsed
		txPacketsPerSec := float64(cur.txPackets-prev.txPackets) / elapsed

		// Counter-Überlauf abfangen
		if rxBytesPerSec < 0 { rxBytesPerSec = 0 }
		if txBytesPerSec < 0 { txBytesPerSec = 0 }

		ifaceTag := models.Tags{"iface": iface}

		metrics[models.MetricNetRxBytes+"."+iface] = rxBytesPerSec
		metrics[models.MetricNetTxBytes+"."+iface] = txBytesPerSec
		metrics[models.MetricNetRxPackets+"."+iface] = rxPacketsPerSec
		metrics[models.MetricNetTxPackets+"."+iface] = txPacketsPerSec

		tags[models.MetricNetRxBytes+"."+iface] = ifaceTag
		tags[models.MetricNetTxBytes+"."+iface] = ifaceTag
		tags[models.MetricNetRxPackets+"."+iface] = ifaceTag
		tags[models.MetricNetTxPackets+"."+iface] = ifaceTag
	}

	n.prevStats = stats
	n.prevTime = now

	return metrics, tags, nil
}

func (n *NetCollector) readProcNetDev() (map[string]netStat, error) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	result := make(map[string]netStat)
	scanner := bufio.NewScanner(f)

	// Erste zwei Zeilen sind Header
	scanner.Scan()
	scanner.Scan()

	for scanner.Scan() {
		line := scanner.Text()
		// Format: "  eth0:  123456  1234  0  0  0  0  0  0  654321  ..."
		colonIdx := strings.Index(line, ":")
		if colonIdx < 0 {
			continue
		}

		iface := strings.TrimSpace(line[:colonIdx])
		fields := strings.Fields(line[colonIdx+1:])

		if len(fields) < 10 {
			continue
		}

		parseUint := func(s string) uint64 {
			v, _ := strconv.ParseUint(s, 10, 64)
			return v
		}

		result[iface] = netStat{
			rxBytes:   parseUint(fields[0]),
			rxPackets: parseUint(fields[1]),
			txBytes:   parseUint(fields[8]),
			txPackets: parseUint(fields[9]),
		}
	}

	return result, scanner.Err()
}
