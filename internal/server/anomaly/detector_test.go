package anomaly_test

import (
	"math"
	"testing"
	"time"

	"github.com/pulsewatch/internal/server/anomaly"
)

func TestDetector_Normal(t *testing.T) {
	d := anomaly.New(60, 3.0)

	// Stabile Werte einführen (kein Ausreißer)
	for i := 0; i < 50; i++ {
		d.Observe("host", "cpu", 50.0+float64(i%5)*0.1, time.Now())
	}

	score, ok := d.GetScore("host", "cpu")
	if !ok {
		t.Fatal("expected score")
	}
	if score.IsAnomaly {
		t.Errorf("stable values should not be anomaly, z=%.2f", score.ZScore)
	}
}

func TestDetector_Anomaly(t *testing.T) {
	d := anomaly.New(60, 3.0)

	// Stabile Baseline
	for i := 0; i < 40; i++ {
		d.Observe("host", "cpu", 50.0, time.Now())
	}

	// Extremer Ausreißer
	d.Observe("host", "cpu", 200.0, time.Now())

	score, ok := d.GetScore("host", "cpu")
	if !ok {
		t.Fatal("expected score")
	}
	if !score.IsAnomaly {
		t.Errorf("extreme outlier should be anomaly, z=%.2f", score.ZScore)
	}
	if score.ZScore < 3.0 {
		t.Errorf("z-score should be > 3.0, got %.2f", score.ZScore)
	}
}

func TestDetector_InsufficientData(t *testing.T) {
	d := anomaly.New(60, 3.0)

	// Zu wenig Daten für Erkennung
	d.Observe("host", "cpu", 999.0, time.Now()) // Nur 1 Wert

	score, ok := d.GetScore("host", "cpu")
	if !ok {
		t.Fatal("expected score")
	}
	// Zu wenig Daten → kein Anomaly-Flag
	if score.IsAnomaly {
		t.Error("should not flag anomaly with insufficient data")
	}
}

func TestDetector_MultipleHosts(t *testing.T) {
	d := anomaly.New(60, 3.0)
	now := time.Now()

	for i := 0; i < 40; i++ {
		d.Observe("host-a", "cpu", 30.0, now)
		d.Observe("host-b", "cpu", 80.0, now)
	}

	scA, _ := d.GetScore("host-a", "cpu")
	scB, _ := d.GetScore("host-b", "cpu")

	// Beide stable
	if scA.IsAnomaly { t.Error("host-a should not be anomaly") }
	if scB.IsAnomaly { t.Error("host-b should not be anomaly") }

	// Ausreißer nur auf host-a
	d.Observe("host-a", "cpu", 200.0, now)

	scA, _ = d.GetScore("host-a", "cpu")
	scB, _ = d.GetScore("host-b", "cpu")

	if !scA.IsAnomaly { t.Error("host-a outlier should be anomaly") }
	if scB.IsAnomaly  { t.Error("host-b should remain normal") }
}

func TestDetector_ZScoreFormula(t *testing.T) {
	d := anomaly.New(100, 3.0)
	now := time.Now()

	// Bekannte Werte: mean=10, stddev=0 → alle gleich
	for i := 0; i < 40; i++ {
		d.Observe("h", "m", 10.0, now)
	}

	// Wert weit daneben
	d.Observe("h", "m", 40.0, now)

	sc, _ := d.GetScore("h", "m")
	// Z sollte positiv und groß sein
	if sc.ZScore < 0 {
		t.Errorf("z-score should be positive, got %.2f", sc.ZScore)
	}
	_ = math.Abs(sc.ZScore) // Sanity
}
