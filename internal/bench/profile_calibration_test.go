package bench

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/evidence"
)

func TestValidateProfileCalibrationBindsExactSharedTokenizerPrefix(t *testing.T) {
	profile, calibration := calibratedProfileFixture(t)
	if err := ValidateProfileCalibration(profile, calibration); err != nil {
		t.Fatal(err)
	}
	profile.Prefixes[1].EstimatedTokens++
	if err := ValidateProfileCalibration(profile, calibration); err == nil || !strings.Contains(err.Error(), "64-token-aligned") {
		t.Fatalf("ValidateProfileCalibration() error = %v", err)
	}
}

func TestValidateProfileCalibrationIncludesBoundarySensitiveWarmup(t *testing.T) {
	profile, calibration := calibratedProfileFixture(t)
	calibration.Prefixes[1].Tokenizations[0][100] = 999_999
	if err := ValidateProfileCalibration(profile, calibration); err == nil || !strings.Contains(err.Error(), "64-token-aligned") {
		t.Fatalf("ValidateProfileCalibration() error = %v, want warmup LCP mismatch", err)
	}
}

func TestValidateProfileCalibrationRoundsRawLCPDownToCacheBlock(t *testing.T) {
	profile, calibration := calibratedProfileFixture(t)
	if calibration.Prefixes[1].ExactSharedTokens != 130 || calibration.Prefixes[1].CacheableSharedTokens != 128 || profile.Prefixes[1].EstimatedTokens != 128 {
		t.Fatalf("fixture did not retain raw and 64-token-aligned counts: %#v", calibration.Prefixes[1])
	}
	if err := ValidateProfileCalibration(profile, calibration); err != nil {
		t.Fatalf("ValidateProfileCalibration() rejected non-aligned raw LCP: %v", err)
	}
}

func TestValidateProfileCalibrationRejectsUnmeasuredCrossover(t *testing.T) {
	profile, calibration := calibratedProfileFixture(t)
	calibration.CrossoverSamples = calibration.CrossoverSamples[1:]
	calibration.CrossoverEvidence.Samples = calibration.CrossoverEvidence.Samples[1:]
	trials := calibration.CrossoverEvidence.Trials[:0]
	for _, trial := range calibration.CrossoverEvidence.Trials {
		if trial.PrefixTokens != 64 {
			trials = append(trials, trial)
		}
	}
	calibration.CrossoverEvidence.Trials = trials
	rehashCrossoverEvidence(t, &calibration)
	if err := ValidateProfileCalibration(profile, calibration); err == nil || !strings.Contains(err.Error(), "non-beneficial") {
		t.Fatalf("ValidateProfileCalibration() error = %v", err)
	}
}

func TestValidateProfileCalibrationRejectsEmptyChatTemplate(t *testing.T) {
	profile, calibration := calibratedProfileFixture(t)
	calibration.TokenizerInfo = json.RawMessage(`{"tokenizer_class":"QwenTokenizer","chat_template":""}`)
	if err := ValidateProfileCalibration(profile, calibration); err == nil || !strings.Contains(err.Error(), "chat template") {
		t.Fatalf("ValidateProfileCalibration() error = %v, want empty chat-template rejection", err)
	}
}

func TestAggregateCrossoverTrialsDerivesMedianAndThreshold(t *testing.T) {
	trials := make([]CrossoverTrial, 0, 12)
	now := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	for _, prefix := range []uint64{64, 128} {
		for index := 0; index < 3; index++ {
			trials = append(trials,
				CrossoverTrial{RunID: "run", GroupID: fmt.Sprintf("group-r-%d-%d", prefix, index), RequestID: fmt.Sprintf("request-r-%d-%d", prefix, index), PrefixTokens: prefix, Acquisition: "recompute", Seconds: float64(prefix) / 1000, ObservedAt: now.Add(time.Duration(len(trials)) * time.Second)},
				CrossoverTrial{RunID: "run", GroupID: fmt.Sprintf("group-p-%d-%d", prefix, index), RequestID: fmt.Sprintf("request-p-%d-%d", prefix, index), PrefixTokens: prefix, Acquisition: "p2p", Seconds: map[uint64]float64{64: .1, 128: .08}[prefix], TransferBytes: prefix * 512, ObservedAt: now.Add(time.Duration(len(trials)+1) * time.Second)},
			)
		}
	}
	samples, err := AggregateCrossoverTrials(trials)
	if err != nil {
		t.Fatal(err)
	}
	if crossover, err := CrossoverTokens(samples); err != nil || crossover != 128 {
		t.Fatalf("CrossoverTokens() = %d, %v", crossover, err)
	}
}

func calibratedProfileFixture(t *testing.T) (Profile, ProfileCalibration) {
	t.Helper()
	profile := validProfile()
	profile.Arms = []evidence.Arm{evidence.ArmLoadAwareP2P}
	profile.Prefixes = []PrefixProfile{
		{ID: "below-zeroing-crossover", EstimatedTokens: 64, RepeatText: "shared ", RepeatCount: 10},
		{ID: "near-zeroing-crossover", EstimatedTokens: 128, RepeatText: "shared ", RepeatCount: 20},
		{ID: "above-zeroing-crossover", EstimatedTokens: 256, RepeatText: "shared ", RepeatCount: 40},
	}
	calibration := ProfileCalibration{
		SchemaVersion: ProfileCalibrationSchemaVersion, ModelID: profile.Model,
		ModelRevision: strings.Repeat("a", 40), Transport: "tcp",
		TokenizerInfo:       json.RawMessage(`{"tokenizer_class":"Qwen2Tokenizer","chat_template":"fixture-template"}`),
		MinCachedTokenDelta: 128,
		CrossoverSamples: []CrossoverSample{
			{PrefixTokens: 64, RecomputeSeconds: 0.1, P2PSeconds: 0.2, RecomputeTrials: 3, P2PTrials: 3},
			{PrefixTokens: 128, RecomputeSeconds: 0.2, P2PSeconds: 0.15, RecomputeTrials: 3, P2PTrials: 3},
			{PrefixTokens: 256, RecomputeSeconds: 0.4, P2PSeconds: 0.2, RecomputeTrials: 3, P2PTrials: 3},
		},
	}
	trials := make([]CrossoverTrial, 0, 18)
	now := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	for _, sample := range calibration.CrossoverSamples {
		for index := 0; index < 3; index++ {
			trials = append(trials,
				CrossoverTrial{RunID: "run", GroupID: fmt.Sprintf("r-%d-%d", sample.PrefixTokens, index), RequestID: fmt.Sprintf("rr-%d-%d", sample.PrefixTokens, index), PrefixTokens: sample.PrefixTokens, Acquisition: "recompute", Seconds: sample.RecomputeSeconds, ObservedAt: now.Add(time.Duration(len(trials)) * time.Second)},
				CrossoverTrial{RunID: "run", GroupID: fmt.Sprintf("p-%d-%d", sample.PrefixTokens, index), RequestID: fmt.Sprintf("pp-%d-%d", sample.PrefixTokens, index), PrefixTokens: sample.PrefixTokens, Acquisition: "p2p", Seconds: sample.P2PSeconds, TransferBytes: sample.PrefixTokens * 512, ObservedAt: now.Add(time.Duration(len(trials)+1) * time.Second)},
			)
		}
	}
	calibration.CrossoverEvidence = CrossoverEvidence{
		SchemaVersion: CrossoverEvidenceSchemaVersion, ModelID: profile.Model, ModelRevision: calibration.ModelRevision, Transport: "tcp",
		Deployment:         CrossoverDeploymentBinding{RepositoryCommit: strings.Repeat("b", 40), ModelImage: "example/model@sha256:" + strings.Repeat("c", 64), ModelSpecSHA256: strings.Repeat("d", 64), ActiveArm: string(evidence.ArmLoadAwareP2P), EPPImage: "example/epp@sha256:" + strings.Repeat("e", 64), EPPReplicas: 1, ReplicaCount: 6, GPUModel: "L40S", GPUDriverVersion: "570", InstanceType: "g6e.xlarge", RouterConfigInvariantSHA256: strings.Repeat("1", 64)},
		ObservationsSHA256: strings.Repeat("2", 64), RuntimeRawSHA256: strings.Repeat("3", 64), CalibrationBindingSHA256: strings.Repeat("4", 64), Trials: trials, Samples: calibration.CrossoverSamples, MinCachedTokenDelta: 128,
	}
	rehashCrossoverEvidence(t, &calibration)
	for prefixIndex, prefix := range profile.Prefixes {
		rendered := strings.Repeat(prefix.RepeatText, int(prefix.RepeatCount))
		digest := sha256.Sum256([]byte(rendered))
		warmupDigest := sha256.Sum256([]byte(rendered + CalibrationWarmupSuffix))
		exact := []uint64{70, 130, 260}[prefixIndex]
		binding := PrefixTokenCalibration{ID: prefix.ID, RenderedPrefixSHA256: hex.EncodeToString(digest[:]), WarmupContentSHA256: hex.EncodeToString(warmupDigest[:]), ExactSharedTokens: exact, CacheableSharedTokens: prefix.EstimatedTokens}
		for suffix := uint32(0); suffix <= profile.MaxWidth; suffix++ {
			tokens := make([]uint32, exact+1)
			for index := uint64(0); index < exact; index++ {
				tokens[index] = uint32(index + 1 + uint64(prefixIndex)*100)
			}
			tokens[exact] = 10_000 + suffix
			binding.Tokenizations = append(binding.Tokenizations, tokens)
		}
		calibration.Prefixes = append(calibration.Prefixes, binding)
	}
	return profile, calibration
}

func rehashCrossoverEvidence(t *testing.T, calibration *ProfileCalibration) {
	t.Helper()
	canonical, err := json.Marshal(calibration.CrossoverEvidence)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	calibration.CrossoverEvidenceSHA256 = hex.EncodeToString(digest[:])
}
