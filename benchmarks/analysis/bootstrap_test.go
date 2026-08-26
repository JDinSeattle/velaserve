package analysis

import (
	"math"
	"testing"
)

func TestPairedImprovementCIConstantEffectHasPointInterval(t *testing.T) {
	baseline := make([]float64, 20)
	candidate := make([]float64, 20)
	for index := range baseline {
		baseline[index] = float64(100 + index)
		candidate[index] = baseline[index] * 0.8
	}

	got, err := PairedImprovementCI(baseline, candidate, 20260825, 1000, 0.95)
	if err != nil {
		t.Fatalf("PairedImprovementCI() error = %v", err)
	}
	for name, value := range map[string]float64{"estimate": got.Estimate, "lower": got.Lower, "upper": got.Upper} {
		if math.Abs(value-0.2) > 1e-12 {
			t.Fatalf("%s = %.15f, want 0.2", name, value)
		}
	}
}

func TestPairedImprovementCIIsDeterministicForRegisteredSeed(t *testing.T) {
	baseline := []float64{10, 20, 12, 22, 14, 24, 16, 26, 18, 28, 11, 21, 13, 23, 15, 25, 17, 27, 19, 29}
	candidate := []float64{9, 19, 11, 18, 12, 21, 13, 22, 15, 25, 10, 18, 12, 20, 13, 22, 14, 24, 16, 26}
	first, err := PairedImprovementCI(baseline, candidate, 20260825, 1000, 0.95)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PairedImprovementCI(baseline, candidate, 20260825, 1000, 0.95)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("same seed produced %#v and %#v", first, second)
	}
}

func TestPairedImprovementCIRejectsFewerThanTwentyPairs(t *testing.T) {
	_, err := PairedImprovementCI(make([]float64, 19), make([]float64, 19), 1, 100, 0.95)
	if err == nil {
		t.Fatal("PairedImprovementCI() error = nil, want minimum-pair rejection")
	}
}

func TestPairedImprovementCIMeasuresP95InsteadOfMeanPerPair(t *testing.T) {
	baseline := make([]float64, 100)
	candidate := make([]float64, 100)
	for index := range baseline {
		baseline[index] = 1
		candidate[index] = 0.5
		if index >= 90 {
			candidate[index] = 2
		}
	}
	got, err := PairedImprovementCI(baseline, candidate, 20260825, 10_000, 0.95)
	if err != nil {
		t.Fatal(err)
	}
	if got.Estimate >= 0 {
		t.Fatalf("p95 improvement estimate = %v, want regression below zero", got.Estimate)
	}
}
