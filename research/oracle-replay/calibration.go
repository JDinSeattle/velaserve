package replay

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"reflect"
	"sort"

	"github.com/JDinSeattle/velaserve/internal/bench"
	"github.com/JDinSeattle/velaserve/internal/conditiondriver"
)

// DeriveReplayProfile freezes the oracle cost model from retained crossover
// and open-loop load observations. The two scheduler-policy values are kept
// explicit because they are deployed EPP behavior, not GPU measurements.
func DeriveReplayProfile(profile bench.Profile, profileCalibration bench.ProfileCalibration, profileCalibrationSource []byte, crossoverEvidence bench.CrossoverEvidence, loadCalibration conditiondriver.LoadCalibration, inflightPublicationDelayMS, affinityLoadGateSeconds float64) (ReplayProfile, error) {
	if err := bench.ValidateProfileCalibration(profile, profileCalibration); err != nil {
		return ReplayProfile{}, err
	}
	if !reflect.DeepEqual(profileCalibration.CrossoverEvidence, crossoverEvidence) {
		return ReplayProfile{}, fmt.Errorf("profile calibration differs from the sealed crossover evidence")
	}
	profileDigest := sha256.Sum256(profileCalibrationSource)
	if loadCalibration.ProfileCalibrationSHA256 != hex.EncodeToString(profileDigest[:]) {
		return ReplayProfile{}, fmt.Errorf("load calibration is not bound to the exact profile calibration")
	}
	loadProfiles, err := conditiondriver.DeriveLoadProfiles(loadCalibration)
	if err != nil {
		return ReplayProfile{}, err
	}
	if _, err := conditiondriver.ValidateLoadCalibration(loadCalibration, loadProfiles, profile.Model); err != nil {
		return ReplayProfile{}, err
	}
	if loadCalibration.ModelRevision != profileCalibration.ModelRevision || loadCalibration.Transport != profileCalibration.Transport {
		return ReplayProfile{}, fmt.Errorf("load and profile calibrations have different model revision or transport")
	}
	if !finiteNonNegative(inflightPublicationDelayMS) || !finitePositive(affinityLoadGateSeconds) {
		return ReplayProfile{}, fmt.Errorf("scheduler publication delay and affinity load gate are invalid")
	}

	prefillRates := make([]float64, 0)
	pullRates := make([]float64, 0)
	bytesPerToken := make([]float64, 0)
	for _, trial := range crossoverEvidence.Trials {
		switch trial.Acquisition {
		case "recompute":
			prefillRates = append(prefillRates, float64(trial.PrefixTokens)/trial.Seconds)
		case "p2p":
			pullRates = append(pullRates, float64(trial.TransferBytes)/trial.Seconds)
			bytesPerToken = append(bytesPerToken, float64(trial.TransferBytes)/float64(trial.PrefixTokens))
		}
	}
	prefill, err := medianPositive(prefillRates)
	if err != nil {
		return ReplayProfile{}, fmt.Errorf("derive prefill throughput: %w", err)
	}
	pull, err := medianPositive(pullRates)
	if err != nil {
		return ReplayProfile{}, fmt.Errorf("derive pull throughput: %w", err)
	}
	bytes, err := medianPositive(bytesPerToken)
	if err != nil {
		return ReplayProfile{}, fmt.Errorf("derive bytes per cached token: %w", err)
	}

	result := ReplayProfile{
		InflightPublicationDelayMS: inflightPublicationDelayMS,
		AffinityLoadGateSeconds:    affinityLoadGateSeconds,
		Calibration: ThroughputCalibration{
			PrefillTokensPerSecond: prefill,
			PullBytesPerSecond:     pull,
			BytesPerCachedToken:    bytes,
		},
		ServiceSecondsByOutputRegime: make(map[string]OutputServiceCalibration, len(profile.Outputs)),
	}
	for _, output := range profile.Outputs {
		var lowestRateLatencies []float64
		for _, prefix := range profile.Prefixes {
			bestRate := math.Inf(1)
			bestLatency := 0.0
			for _, trial := range loadCalibration.Trials {
				if trial.PrefixTokens != prefix.EstimatedTokens || trial.MaxTokens != output.MaxTokens {
					continue
				}
				rate := float64(trial.OfferedRequests) / trial.DurationSeconds
				if rate < bestRate {
					bestRate = rate
					bestLatency = trial.MeanLatencySeconds
				}
			}
			if !finitePositive(bestLatency) {
				return ReplayProfile{}, fmt.Errorf("output %q lacks a lowest-load service observation for prefix %q", output.ID, prefix.ID)
			}
			lowestRateLatencies = append(lowestRateLatencies, bestLatency)
		}
		seconds, err := medianPositive(lowestRateLatencies)
		if err != nil {
			return ReplayProfile{}, fmt.Errorf("derive output %q service time: %w", output.ID, err)
		}
		result.ServiceSecondsByOutputRegime[output.ID] = OutputServiceCalibration{MaxTokens: output.MaxTokens, Seconds: seconds}
	}
	return result, nil
}

func medianPositive(values []float64) (float64, error) {
	if len(values) == 0 {
		return 0, fmt.Errorf("no observations")
	}
	ordered := append([]float64(nil), values...)
	for _, value := range ordered {
		if !finitePositive(value) {
			return 0, fmt.Errorf("observation is not finite and positive")
		}
	}
	sort.Float64s(ordered)
	middle := len(ordered) / 2
	if len(ordered)%2 == 1 {
		return ordered[middle], nil
	}
	return (ordered[middle-1] + ordered[middle]) / 2, nil
}

func finiteNonNegative(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
