package alerting_test

import (
	"os"
	"testing"
	"time"

	"github.com/pulsewatch/internal/server/alerting"
)

// mockProvider implementiert MetricProvider für Tests
type mockProvider struct {
	values map[string]float64
}

func (m *mockProvider) LastValue(host, metric string) (float64, bool) {
	v, ok := m.values[host+"\x00"+metric]
	return v, ok
}

func TestAlertRuleEvaluate(t *testing.T) {
	dir, _ := os.MkdirTemp("", "alert-test-*")
	defer os.RemoveAll(dir)

	provider := &mockProvider{values: map[string]float64{
		"host1\x00cpu.usage_percent": 95.0,
	}}

	cfg := &alerting.RulesConfig{
		Alerts: []alerting.Rule{
			{
				Name:      "High CPU",
				Host:      "host1",
				Metric:    "cpu.usage_percent",
				Op:        ">",
				Threshold: 90.0,
				For:       alerting.Duration{Duration: 0}, // sofort firing
				Severity:  alerting.SevCritical,
				Notify:    []string{"log"},
				Enabled:   true,
			},
		},
		Channels: map[string]alerting.Channel{
			"log": {Type: "log"},
		},
	}

	history := alerting.NewHistory(dir, 100)
	engine  := alerting.New(cfg, provider, history)

	// Direkt eval triggern (intern über Run, aber wir prüfen History)
	// Nach kurzer Zeit sollte ein Alert in der History sein

	// Warten damit der Alert-Loop feuert — wir testen nur die History-API
	events := engine.RecentHistory(10)
	// Zu Beginn leer
	if len(events) != 0 {
		t.Errorf("expected empty history, got %d events", len(events))
	}
}

func TestAlertHistory(t *testing.T) {
	dir, _ := os.MkdirTemp("", "alert-hist-*")
	defer os.RemoveAll(dir)

	h := alerting.NewHistory(dir, 10)

	e := alerting.AlertEvent{
		ID:       "test1",
		RuleName: "High CPU",
		Host:     "web-01",
		Metric:   "cpu.usage_percent",
		State:    alerting.StateFiring,
		Value:    95.0,
		Threshold: 90.0,
		Severity: alerting.SevCritical,
		FiredAt:  time.Now(),
		Message:  "Test alert",
	}
	h.Add(e)

	events := h.Recent(10)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].RuleName != "High CPU" {
		t.Errorf("unexpected rule name: %s", events[0].RuleName)
	}

	// Resolve
	h.Resolve("test1")
	active := h.Active()
	if len(active) != 0 {
		t.Errorf("expected 0 active alerts after resolve, got %d", len(active))
	}

	// Persistenz testen: neues History-Objekt auf denselben Daten
	h2 := alerting.NewHistory(dir, 10)
	events2 := h2.Recent(10)
	if len(events2) != 1 {
		t.Fatalf("expected 1 persisted event, got %d", len(events2))
	}
}

func TestLoadDefaultConfig(t *testing.T) {
	cfg := alerting.DefaultRulesConfig()
	if len(cfg.Alerts) == 0 {
		t.Error("expected default alerts")
	}
	if _, ok := cfg.Channels["log"]; !ok {
		t.Error("expected log channel in defaults")
	}
}

func TestDuration(t *testing.T) {
	cfg := alerting.DefaultRulesConfig()
	for _, r := range cfg.Alerts {
		if r.For.Duration < 0 {
			t.Errorf("rule %s has negative For duration", r.Name)
		}
	}
}
