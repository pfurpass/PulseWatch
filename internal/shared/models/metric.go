package models

import "time"

// MetricPoint ist ein einzelner Messpunkt
type MetricPoint struct {
	Timestamp time.Time `json:"ts"`
	Host      string    `json:"host"`
	Metric    string    `json:"metric"`
	Value     float64   `json:"value"`
	Tags      Tags      `json:"tags,omitempty"`
}

// Tags sind optionale Key-Value Paare (z.B. device="sda", iface="eth0")
type Tags map[string]string

// Snapshot enthält alle Metriken eines Zeitpunkts (für WebSocket-Push)
type Snapshot struct {
	Timestamp time.Time              `json:"ts"`
	Host      string                 `json:"host"`
	Metrics   map[string]float64     `json:"metrics"`
	Tags      map[string]Tags        `json:"tags,omitempty"`
}

// AgentInfo beschreibt den Agent selbst
type AgentInfo struct {
	Host      string    `json:"host"`
	Version   string    `json:"version"`
	StartedAt time.Time `json:"started_at"`
	OS        string    `json:"os"`
	Arch      string    `json:"arch"`
}

// Bekannte Metrik-Namen (Konstanten für typsichere Verwendung)
const (
	MetricCPUUsage       = "cpu.usage_percent"
	MetricCPUSystem      = "cpu.system_percent"
	MetricCPUUser        = "cpu.user_percent"
	MetricCPUIdle        = "cpu.idle_percent"

	MetricMemTotal       = "mem.total_bytes"
	MetricMemUsed        = "mem.used_bytes"
	MetricMemFree        = "mem.free_bytes"
	MetricMemUsedPercent = "mem.used_percent"
	MetricMemCached      = "mem.cached_bytes"
	MetricMemBuffers     = "mem.buffers_bytes"

	MetricDiskReadBytes  = "disk.read_bytes_per_sec"
	MetricDiskWriteBytes = "disk.write_bytes_per_sec"
	MetricDiskReadOps    = "disk.read_ops_per_sec"
	MetricDiskWriteOps   = "disk.write_ops_per_sec"
	MetricDiskUsed       = "disk.used_percent"

	MetricNetRxBytes     = "net.rx_bytes_per_sec"
	MetricNetTxBytes     = "net.tx_bytes_per_sec"
	MetricNetRxPackets   = "net.rx_packets_per_sec"
	MetricNetTxPackets   = "net.tx_packets_per_sec"

	MetricLoadAvg1       = "load.avg1"
	MetricLoadAvg5       = "load.avg5"
	MetricLoadAvg15      = "load.avg15"
)
