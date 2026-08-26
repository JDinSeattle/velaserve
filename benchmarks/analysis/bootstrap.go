package analysis

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sort"

	"github.com/JDinSeattle/velaserve/internal/evidence"
)

func PairedImprovementCI(baseline, candidate []float64, seed int64, repetitions int, confidence float64) (evidence.ConfidenceInterval, error) {
	if len(baseline) != len(candidate) {
		return evidence.ConfidenceInterval{}, fmt.Errorf("paired samples have different lengths: %d and %d", len(baseline), len(candidate))
	}
	if len(baseline) < 20 {
		return evidence.ConfidenceInterval{}, fmt.Errorf("paired samples require at least 20 pairs, got %d", len(baseline))
	}
	if repetitions <= 0 {
		return evidence.ConfidenceInterval{}, fmt.Errorf("bootstrap repetitions must be positive")
	}
	if math.IsNaN(confidence) || confidence <= 0 || confidence >= 1 {
		return evidence.ConfidenceInterval{}, fmt.Errorf("confidence must be in (0,1)")
	}
	for index := range baseline {
		if math.IsNaN(baseline[index]) || math.IsInf(baseline[index], 0) || baseline[index] <= 0 {
			return evidence.ConfidenceInterval{}, fmt.Errorf("baseline[%d] must be finite and positive", index)
		}
		if math.IsNaN(candidate[index]) || math.IsInf(candidate[index], 0) || candidate[index] < 0 {
			return evidence.ConfidenceInterval{}, fmt.Errorf("candidate[%d] must be finite and non-negative", index)
		}
	}
	estimate := p95Improvement(baseline, candidate)
	bootstrap := make([]float64, repetitions)
	baselineSample := make([]float64, len(baseline))
	candidateSample := make([]float64, len(candidate))
	random := rand.New(rand.NewPCG(uint64(seed), uint64(seed)^0x9e3779b97f4a7c15))
	for repetition := range bootstrap {
		for index := range baselineSample {
			selected := random.IntN(len(baseline))
			baselineSample[index] = baseline[selected]
			candidateSample[index] = candidate[selected]
		}
		bootstrap[repetition] = p95Improvement(baselineSample, candidateSample)
	}
	sort.Float64s(bootstrap)
	alpha := (1 - confidence) / 2
	return evidence.ConfidenceInterval{
		Estimate: estimate,
		Lower:    quantile(bootstrap, alpha),
		Upper:    quantile(bootstrap, 1-alpha),
	}, nil
}

func p95Improvement(baseline, candidate []float64) float64 {
	baselineSorted := append([]float64(nil), baseline...)
	candidateSorted := append([]float64(nil), candidate...)
	sort.Float64s(baselineSorted)
	sort.Float64s(candidateSorted)
	baselineP95 := quantile(baselineSorted, 0.95)
	candidateP95 := quantile(candidateSorted, 0.95)
	return (baselineP95 - candidateP95) / baselineP95
}

func quantile(sorted []float64, probability float64) float64 {
	position := probability * float64(len(sorted)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return sorted[lower]
	}
	weight := position - float64(lower)
	return sorted[lower]*(1-weight) + sorted[upper]*weight
}
