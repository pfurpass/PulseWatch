package storage_test

import (
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	"github.com/pulsewatch/internal/server/storage"
)

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "pulsewatch-storage-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestEngine_WriteAndQueryHot(t *testing.T) {
	e, err := storage.New(tempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	e.Start()
	defer e.Stop()

	now := time.Now()

	// 60 Records schreiben
	for i := 0; i < 60; i++ {
		e.Write(storage.Record{
			Timestamp: now.Add(time.Duration(i) * time.Second),
			Host:      "test-host",
			Metric:    "cpu.usage_percent",
			Value:     float64(i),
		})
	}

	// Kurz warten damit der WriteLoop verarbeitet
	time.Sleep(50 * time.Millisecond)

	result, err := e.Query(storage.Query{
		Host:   "test-host",
		Metric: "cpu.usage_percent",
		From:   now.Add(-time.Second),
		To:     now.Add(61 * time.Second),
	})
	if err != nil {
		t.Fatalf("query error: %v", err)
	}

	if len(result.Points) != 60 {
		t.Errorf("expected 60 points, got %d", len(result.Points))
	}

	// Chronologische Reihenfolge prüfen
	for i := 1; i < len(result.Points); i++ {
		if result.Points[i].Timestamp.Before(result.Points[i-1].Timestamp) {
			t.Errorf("points not in order at index %d", i)
		}
	}
}

func TestEngine_Downsampling(t *testing.T) {
	e, err := storage.New(tempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	e.Start()
	defer e.Stop()

	now := time.Now()

	// 1000 Punkte schreiben
	for i := 0; i < 1000; i++ {
		e.Write(storage.Record{
			Timestamp: now.Add(time.Duration(i) * time.Second),
			Host:      "host",
			Metric:    "cpu.usage_percent",
			Value:     float64(i % 100),
		})
	}
	time.Sleep(50 * time.Millisecond)

	result, err := e.Query(storage.Query{
		Host:      "host",
		Metric:    "cpu.usage_percent",
		From:      now,
		To:        now.Add(1000 * time.Second),
		MaxPoints: 100,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Points) > 100 {
		t.Errorf("expected ≤100 points after downsampling, got %d", len(result.Points))
	}
	if len(result.Points) < 2 {
		t.Errorf("expected at least 2 points, got %d", len(result.Points))
	}

	// Punkte müssen chronologisch geordnet sein
	for i := 1; i < len(result.Points); i++ {
		if result.Points[i].Timestamp.Before(result.Points[i-1].Timestamp) {
			t.Errorf("points not in chronological order at index %d", i)
		}
	}
}

func TestEngine_Hosts(t *testing.T) {
	e, err := storage.New(tempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	e.Start()
	defer e.Stop()

	hosts := []string{"alpha", "beta", "gamma"}
	now := time.Now()

	for _, h := range hosts {
		e.Write(storage.Record{
			Timestamp: now,
			Host:      h,
			Metric:    "cpu.usage_percent",
			Value:     42,
		})
	}
	time.Sleep(50 * time.Millisecond)

	got := e.Hosts()
	if len(got) != 3 {
		t.Errorf("expected 3 hosts, got %d: %v", len(got), got)
	}
}

func TestEngine_Metrics(t *testing.T) {
	e, err := storage.New(tempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	e.Start()
	defer e.Stop()

	now := time.Now()
	metricsToWrite := []string{"cpu.usage_percent", "mem.used_percent", "load.avg1"}

	for _, m := range metricsToWrite {
		e.Write(storage.Record{
			Timestamp: now,
			Host:      "test",
			Metric:    m,
			Value:     1.0,
		})
	}
	time.Sleep(50 * time.Millisecond)

	got := e.Metrics("test")
	if len(got) != 3 {
		t.Errorf("expected 3 metrics, got %d: %v", len(got), got)
	}
}

func TestEngine_WarmStorage(t *testing.T) {
	dir := tempDir(t)
	e, err := storage.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	e.Start()

	now := time.Now()

	// 20 Records schreiben und sofort flushen
	for i := 0; i < 20; i++ {
		e.Write(storage.Record{
			Timestamp: now.Add(time.Duration(i) * time.Second),
			Host:      "persist-host",
			Metric:    "mem.used_percent",
			Value:     float64(i) * 1.5,
		})
	}

	// Flush abwarten (Stop flusht alle pending)
	e.Stop()

	// Neue Engine-Instanz auf denselben Daten
	e2, err := storage.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	e2.Start()
	defer e2.Stop()

	// Daten müssen aus Warm-Storage lesbar sein
	result, err := e2.Query(storage.Query{
		Host:   "persist-host",
		Metric: "mem.used_percent",
		From:   now.Add(-time.Second),
		To:     now.Add(25 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Points) == 0 {
		t.Error("expected points from warm storage, got 0")
	}

	// Werte prüfen
	for i, p := range result.Points {
		expected := float64(i) * 1.5
		if math.Abs(p.Value-expected) > 0.001 {
			t.Errorf("point %d: expected value %v, got %v", i, expected, p.Value)
		}
	}
}

func TestEngine_WriteBatch(t *testing.T) {
	e, err := storage.New(tempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	e.Start()
	defer e.Stop()

	now := time.Now()
	records := make([]storage.Record, 100)
	for i := range records {
		records[i] = storage.Record{
			Timestamp: now.Add(time.Duration(i) * time.Second),
			Host:      "batch-host",
			Metric:    fmt.Sprintf("metric.%d", i%5),
			Value:     float64(i),
		}
	}

	e.WriteBatch(records)
	time.Sleep(50 * time.Millisecond)

	hosts := e.Hosts()
	if len(hosts) == 0 || hosts[0] != "batch-host" {
		t.Errorf("expected batch-host in hosts, got %v", hosts)
	}

	metrics := e.Metrics("batch-host")
	if len(metrics) != 5 {
		t.Errorf("expected 5 metrics, got %d", len(metrics))
	}
}
