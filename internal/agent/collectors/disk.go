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

type diskStat struct {
	readOps   uint64
	readBytes uint64
	writeOps  uint64
	writeBytes uint64
}

// DiskCollector liest Disk I/O aus /proc/diskstats und Disk-Usage via syscall
type DiskCollector struct {
	mu        sync.Mutex
	prevStats map[string]diskStat
	prevTime  time.Time
}

func NewDiskCollector() *DiskCollector {
	c := &DiskCollector{
		prevStats: make(map[string]diskStat),
	}
	// Erste Werte einlesen
	stats, err := c.readDiskstats()
	if err == nil {
		c.prevStats = stats
		c.prevTime = time.Now()
	}
	return c
}

func (d *DiskCollector) Name() string { return "disk" }

func (d *DiskCollector) Collect() (map[string]float64, map[string]models.Tags, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()
	stats, err := d.readDiskstats()
	if err != nil {
		return nil, nil, fmt.Errorf("disk collect: %w", err)
	}

	elapsed := now.Sub(d.prevTime).Seconds()
	if elapsed <= 0 {
		elapsed = 1
	}

	metrics := make(map[string]float64)
	tags := make(map[string]models.Tags)

	for dev, cur := range stats {
		prev, ok := d.prevStats[dev]
		if !ok {
			continue
		}

		// Nur physische Block-Devices (keine Partitionen, keine Loop)
		if isPartition(dev) || strings.HasPrefix(dev, "loop") {
			continue
		}

		readBytesPerSec := float64(cur.readBytes-prev.readBytes) / elapsed
		writeBytesPerSec := float64(cur.writeBytes-prev.writeBytes) / elapsed
		readOpsPerSec := float64(cur.readOps-prev.readOps) / elapsed
		writeOpsPerSec := float64(cur.writeOps-prev.writeOps) / elapsed

		// Negative Werte (Counter-Überlauf) ignorieren
		if readBytesPerSec < 0 { readBytesPerSec = 0 }
		if writeBytesPerSec < 0 { writeBytesPerSec = 0 }

		tagKey := models.MetricDiskReadBytes + "." + dev
		metrics[models.MetricDiskReadBytes+"."+dev] = readBytesPerSec
		metrics[models.MetricDiskWriteBytes+"."+dev] = writeBytesPerSec
		metrics[models.MetricDiskReadOps+"."+dev] = readOpsPerSec
		metrics[models.MetricDiskWriteOps+"."+dev] = writeOpsPerSec

		// Tags für einfachen Zugriff
		devTag := models.Tags{"device": dev}
		tags[tagKey] = devTag
		tags[models.MetricDiskWriteBytes+"."+dev] = devTag
		tags[models.MetricDiskReadOps+"."+dev] = devTag
		tags[models.MetricDiskWriteOps+"."+dev] = devTag
	}

	// Disk-Usage (Füllstand) via /proc/mounts + statfs
	usageMetrics := d.collectDiskUsage()
	for k, v := range usageMetrics {
		metrics[k] = v
	}

	d.prevStats = stats
	d.prevTime = now

	return metrics, tags, nil
}

func (d *DiskCollector) readDiskstats() (map[string]diskStat, error) {
	f, err := os.Open("/proc/diskstats")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	result := make(map[string]diskStat)
	scanner := bufio.NewScanner(f)

	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 14 {
			continue
		}

		dev := fields[2]
		parseUint := func(s string) uint64 {
			v, _ := strconv.ParseUint(s, 10, 64)
			return v
		}

		// Sektoren → Bytes (512 Bytes pro Sektor)
		result[dev] = diskStat{
			readOps:    parseUint(fields[3]),
			readBytes:  parseUint(fields[5]) * 512,
			writeOps:   parseUint(fields[7]),
			writeBytes: parseUint(fields[9]) * 512,
		}
	}

	return result, scanner.Err()
}

func (d *DiskCollector) collectDiskUsage() map[string]float64 {
	metrics := make(map[string]float64)

	// Nur reale Filesysteme aus /proc/mounts
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return metrics
	}
	defer f.Close()

	seen := make(map[string]bool)
	scanner := bufio.NewScanner(f)

	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			continue
		}

		fstype := fields[2]
		mountpoint := fields[1]

		// Nur echte Filesysteme (kein tmpfs, proc, sysfs, etc.)
		realFS := map[string]bool{
			"ext4": true, "ext3": true, "ext2": true,
			"xfs": true, "btrfs": true, "zfs": true,
			"vfat": true, "ntfs": true, "f2fs": true,
		}

		if !realFS[fstype] || seen[mountpoint] {
			continue
		}
		seen[mountpoint] = true

		used, total, err := getDiskUsage(mountpoint)
		if err != nil || total == 0 {
			continue
		}

		// Mountpoint als Tag-Schlüssel (/ → root, /data → data)
		safeMount := strings.ReplaceAll(mountpoint, "/", "_")
		if safeMount == "_" {
			safeMount = "root"
		}
		safeMount = strings.Trim(safeMount, "_")

		metrics[models.MetricDiskUsed+"."+safeMount] = (float64(used) / float64(total)) * 100
	}

	return metrics
}

// isPartition prüft ob ein Device eine Partition ist (z.B. sda1, nvme0n1p1)
func isPartition(dev string) bool {
	// sda1, sdb2, nvme0n1p1 etc.
	for _, c := range dev[len(dev)-1:] {
		if c >= '0' && c <= '9' {
			// Check ob Basisdevice existiert
			base := strings.TrimRight(dev, "0123456789")
			if base != dev && len(base) > 0 {
				_, err := os.Stat("/sys/block/" + base)
				return err == nil
			}
		}
	}
	return false
}
