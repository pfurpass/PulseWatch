package alerting

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// Severity-Level
type Severity string

const (
	SevInfo     Severity = "info"
	SevWarning  Severity = "warning"
	SevCritical Severity = "critical"
)

// AlertState beschreibt den aktuellen Zustand einer Alert-Instanz
type AlertState string

const (
	StateOK       AlertState = "ok"
	StatePending  AlertState = "pending"
	StateFiring   AlertState = "firing"
	StateResolved AlertState = "resolved"
)

// Rule ist eine einzelne Alert-Regel
type Rule struct {
	Name      string        `json:"name"`
	Host      string        `json:"host"`    // "*" = alle Hosts
	Metric    string        `json:"metric"`
	Op        string        `json:"op"`      // ">", "<", ">=", "<=", "==", "!="
	Threshold float64       `json:"threshold"`
	For       Duration      `json:"for"`     // Mindestdauer bevor Firing
	Severity  Severity      `json:"severity"`
	Notify    []string      `json:"notify"`  // channel-Namen
	Enabled   bool          `json:"enabled"`
}

// Duration ist time.Duration mit JSON-Unmarshal ("5m", "1h", etc.)
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		// Fallback: Nanosekunden als int
		var ns int64
		if err2 := json.Unmarshal(b, &ns); err2 != nil {
			return err
		}
		d.Duration = time.Duration(ns)
		return nil
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = parsed
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.Duration.String())
}

// RulesConfig ist die Top-Level Konfiguration
type RulesConfig struct {
	Alerts   []Rule              `json:"alerts"`
	Channels map[string]Channel  `json:"channels"`
}

// Channel ist ein Notification-Kanal
type Channel struct {
	Type       string `json:"type"`        // slack | webhook | email | log
	WebhookURL string `json:"webhook_url"`
	// Email
	SMTPHost string `json:"smtp_host"`
	SMTPPort int    `json:"smtp_port"`
	SMTPUser string `json:"smtp_user"`
	SMTPPass string `json:"smtp_pass"`
	From     string `json:"from"`
	To       []string `json:"to"`
}

// LoadRulesConfig lädt die Alert-Konfiguration aus einer JSON-Datei
func LoadRulesConfig(path string) (*RulesConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var cfg RulesConfig
	if err := json.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	// Defaults setzen
	for i := range cfg.Alerts {
		if cfg.Alerts[i].For.Duration == 0 {
			cfg.Alerts[i].For.Duration = time.Minute
		}
		if cfg.Alerts[i].Severity == "" {
			cfg.Alerts[i].Severity = SevWarning
		}
		if cfg.Alerts[i].Op == "" {
			cfg.Alerts[i].Op = ">"
		}
		cfg.Alerts[i].Enabled = true
	}

	return &cfg, nil
}

// DefaultRulesConfig gibt eine sinnvolle Standardkonfiguration zurück
func DefaultRulesConfig() *RulesConfig {
	return &RulesConfig{
		Alerts: []Rule{
			{Name:"High CPU", Host:"*", Metric:"cpu.usage_percent", Op:">", Threshold:90, For:Duration{5*time.Minute}, Severity:SevCritical, Notify:[]string{"log"}, Enabled:true},
			{Name:"High CPU Warning", Host:"*", Metric:"cpu.usage_percent", Op:">", Threshold:75, For:Duration{10*time.Minute}, Severity:SevWarning, Notify:[]string{"log"}, Enabled:true},
			{Name:"High Memory", Host:"*", Metric:"mem.used_percent", Op:">", Threshold:90, For:Duration{5*time.Minute}, Severity:SevCritical, Notify:[]string{"log"}, Enabled:true},
			{Name:"High Load", Host:"*", Metric:"load.avg1", Op:">", Threshold:8, For:Duration{3*time.Minute}, Severity:SevWarning, Notify:[]string{"log"}, Enabled:true},
		},
		Channels: map[string]Channel{
			"log": {Type:"log"},
		},
	}
}

// evaluate prüft ob eine Bedingung zutrifft
func evaluate(op string, value, threshold float64) bool {
	switch strings.TrimSpace(op) {
	case ">":  return value > threshold
	case ">=": return value >= threshold
	case "<":  return value < threshold
	case "<=": return value <= threshold
	case "==": return value == threshold
	case "!=": return value != threshold
	}
	return false
}
