package bench

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"
)

const (
	ProfileCalibrationSchemaVersion = "velaserve.profile-calibration/v2"
	CrossoverEvidenceSchemaVersion  = "velaserve.crossover-evidence/v2"
	maxProfileCalibrationBytes      = 32 << 20
	CacheBlockTokens                = uint64(64)
	CalibrationWarmupSuffix         = "\nCandidate warmup: prepare the shared cache only."
)

type ProfileCalibration struct {
	SchemaVersion           string                   `json:"schema_version"`
	ModelID                 string                   `json:"model_id"`
	ModelRevision           string                   `json:"model_revision"`
	Transport               string                   `json:"transport"`
	TokenizerInfo           json.RawMessage          `json:"tokenizer_info"`
	CrossoverEvidenceSHA256 string                   `json:"crossover_evidence_sha256"`
	CrossoverEvidence       CrossoverEvidence        `json:"crossover_evidence"`
	MinCachedTokenDelta     uint64                   `json:"min_cached_token_delta"`
	CrossoverSamples        []CrossoverSample        `json:"crossover_samples"`
	Prefixes                []PrefixTokenCalibration `json:"prefixes"`
}

type CrossoverEvidence struct {
	SchemaVersion            string                     `json:"schema_version"`
	ModelID                  string                     `json:"model_id"`
	ModelRevision            string                     `json:"model_revision"`
	Transport                string                     `json:"transport"`
	Deployment               CrossoverDeploymentBinding `json:"deployment"`
	ObservationsSHA256       string                     `json:"observations_sha256"`
	RuntimeRawSHA256         string                     `json:"vllm_raw_sha256"`
	CalibrationBindingSHA256 string                     `json:"calibration_binding_sha256"`
	Trials                   []CrossoverTrial           `json:"trials"`
	Samples                  []CrossoverSample          `json:"samples"`
	MinCachedTokenDelta      uint64                     `json:"min_cached_token_delta"`
}

type CrossoverDeploymentBinding struct {
	RepositoryCommit            string `json:"repository_commit"`
	ModelImage                  string `json:"model_image"`
	ModelSpecSHA256             string `json:"model_spec_sha256"`
	ActiveArm                   string `json:"active_arm"`
	EPPImage                    string `json:"epp_image"`
	EPPReplicas                 uint32 `json:"epp_replicas"`
	ReplicaCount                uint32 `json:"replica_count"`
	GPUModel                    string `json:"gpu_model"`
	GPUDriverVersion            string `json:"gpu_driver_version"`
	InstanceType                string `json:"instance_type"`
	RouterConfigInvariantSHA256 string `json:"router_config_invariant_sha256"`
}

type CrossoverTrial struct {
	RunID         string    `json:"run_id"`
	GroupID       string    `json:"group_id"`
	RequestID     string    `json:"request_id"`
	PrefixTokens  uint64    `json:"prefix_tokens"`
	Acquisition   string    `json:"acquisition"`
	Seconds       float64   `json:"seconds"`
	TransferBytes uint64    `json:"transfer_bytes,omitempty"`
	ObservedAt    time.Time `json:"observed_at"`
}

type CrossoverSample struct {
	PrefixTokens     uint64  `json:"prefix_tokens"`
	RecomputeSeconds float64 `json:"recompute_seconds"`
	P2PSeconds       float64 `json:"p2p_seconds"`
	RecomputeTrials  uint32  `json:"recompute_trials"`
	P2PTrials        uint32  `json:"p2p_trials"`
}

type PrefixTokenCalibration struct {
	ID                    string     `json:"id"`
	RenderedPrefixSHA256  string     `json:"rendered_prefix_sha256"`
	WarmupContentSHA256   string     `json:"warmup_content_sha256"`
	ExactSharedTokens     uint64     `json:"exact_shared_tokens"`
	CacheableSharedTokens uint64     `json:"cacheable_shared_tokens"`
	Tokenizations         [][]uint32 `json:"tokenizations"`
}

func LoadProfileCalibration(path string) (ProfileCalibration, error) {
	calibration, _, err := LoadProfileCalibrationSource(path)
	return calibration, err
}

// LoadProfileCalibrationSource returns the exact bytes that were decoded so
// callers can hash and retain the same evidence without a second, racy read.
func LoadProfileCalibrationSource(path string) (ProfileCalibration, []byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return ProfileCalibration{}, nil, err
	}
	if !info.Mode().IsRegular() {
		return ProfileCalibration{}, nil, fmt.Errorf("profile calibration must be a regular file, not a symlink")
	}
	file, err := os.Open(path)
	if err != nil {
		return ProfileCalibration{}, nil, err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maxProfileCalibrationBytes+1))
	if err != nil {
		return ProfileCalibration{}, nil, err
	}
	if len(contents) > maxProfileCalibrationBytes {
		return ProfileCalibration{}, nil, fmt.Errorf("profile calibration exceeds %d bytes", maxProfileCalibrationBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var calibration ProfileCalibration
	if err := decoder.Decode(&calibration); err != nil {
		return ProfileCalibration{}, nil, fmt.Errorf("decode profile calibration: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ProfileCalibration{}, nil, fmt.Errorf("profile calibration must contain exactly one JSON object")
	}
	return calibration, contents, nil
}

func LoadCrossoverEvidence(path string) (CrossoverEvidence, error) {
	evidence, _, err := LoadCrossoverEvidenceSource(path)
	return evidence, err
}

func LoadCrossoverEvidenceSource(path string) (CrossoverEvidence, []byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return CrossoverEvidence{}, nil, err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maxProfileCalibrationBytes+1))
	if err != nil {
		return CrossoverEvidence{}, nil, err
	}
	if len(contents) > maxProfileCalibrationBytes {
		return CrossoverEvidence{}, nil, fmt.Errorf("crossover evidence exceeds %d bytes", maxProfileCalibrationBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var evidence CrossoverEvidence
	if err := decoder.Decode(&evidence); err != nil {
		return CrossoverEvidence{}, nil, fmt.Errorf("decode crossover evidence: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return CrossoverEvidence{}, nil, fmt.Errorf("crossover evidence must contain exactly one JSON object")
	}
	if err := ValidateCrossoverEvidence(evidence); err != nil {
		return CrossoverEvidence{}, nil, err
	}
	return evidence, contents, nil
}

func ValidateCrossoverEvidence(evidence CrossoverEvidence) error {
	if evidence.SchemaVersion != CrossoverEvidenceSchemaVersion || strings.TrimSpace(evidence.ModelID) == "" || strings.TrimSpace(evidence.Transport) == "" || !lowerHexCommit(evidence.ModelRevision) {
		return fmt.Errorf("crossover evidence identity is invalid")
	}
	deployment := evidence.Deployment
	if !lowerHexCommit(deployment.RepositoryCommit) || strings.TrimSpace(deployment.ModelImage) == "" || strings.TrimSpace(deployment.ActiveArm) == "" || strings.TrimSpace(deployment.EPPImage) == "" || deployment.EPPReplicas == 0 || deployment.ReplicaCount < 2 || strings.TrimSpace(deployment.GPUModel) == "" || strings.TrimSpace(deployment.GPUDriverVersion) == "" || strings.TrimSpace(deployment.InstanceType) == "" {
		return fmt.Errorf("crossover evidence deployment identity is incomplete")
	}
	for name, digest := range map[string]string{
		"model spec": deployment.ModelSpecSHA256, "router config invariant": deployment.RouterConfigInvariantSHA256,
		"observations": evidence.ObservationsSHA256, "raw vLLM runtime": evidence.RuntimeRawSHA256, "calibration binding": evidence.CalibrationBindingSHA256,
	} {
		if !lowerHexSHA256Profile(digest) {
			return fmt.Errorf("crossover evidence %s SHA-256 is invalid", name)
		}
	}
	derived, err := AggregateCrossoverTrials(evidence.Trials)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(derived, evidence.Samples) {
		return fmt.Errorf("crossover samples do not match raw trial medians")
	}
	crossover, err := CrossoverTokens(evidence.Samples)
	if err != nil {
		return err
	}
	if evidence.MinCachedTokenDelta != crossover {
		return fmt.Errorf("min_cached_token_delta is not the measured crossover")
	}
	return nil
}

func ValidateProfileCalibration(profile Profile, calibration ProfileCalibration) error {
	if calibration.SchemaVersion != ProfileCalibrationSchemaVersion || calibration.ModelID != profile.Model || strings.TrimSpace(calibration.Transport) == "" || len(profile.Transports) != 1 || calibration.Transport != profile.Transports[0] {
		return fmt.Errorf("profile calibration schema, model, and transport must match the profile")
	}
	if (len(calibration.ModelRevision) != 40 && len(calibration.ModelRevision) != 64) || strings.Trim(calibration.ModelRevision, "0123456789abcdef") != "" {
		return fmt.Errorf("profile calibration model revision must be immutable lowercase hex")
	}
	var tokenizerInfo map[string]any
	if err := json.Unmarshal(calibration.TokenizerInfo, &tokenizerInfo); err != nil {
		return fmt.Errorf("profile calibration requires the raw tokenizer class and chat template")
	}
	tokenizerClass, tokenizerClassOK := tokenizerInfo["tokenizer_class"].(string)
	if !tokenizerClassOK || strings.TrimSpace(tokenizerClass) == "" || !nonEmptyChatTemplate(tokenizerInfo["chat_template"]) {
		return fmt.Errorf("profile calibration requires the raw tokenizer class and chat template")
	}
	if !lowerHexSHA256Profile(calibration.CrossoverEvidenceSHA256) {
		return fmt.Errorf("profile calibration requires its crossover evidence SHA-256")
	}
	if err := ValidateCrossoverEvidence(calibration.CrossoverEvidence); err != nil {
		return fmt.Errorf("profile calibration crossover evidence: %w", err)
	}
	canonicalCrossover, err := json.Marshal(calibration.CrossoverEvidence)
	if err != nil {
		return err
	}
	crossoverDigest := sha256.Sum256(canonicalCrossover)
	if calibration.CrossoverEvidenceSHA256 != hex.EncodeToString(crossoverDigest[:]) {
		return fmt.Errorf("profile calibration crossover evidence SHA-256 does not match the retained raw trials")
	}
	if calibration.CrossoverEvidence.ModelID != calibration.ModelID || calibration.CrossoverEvidence.ModelRevision != calibration.ModelRevision || calibration.CrossoverEvidence.Transport != calibration.Transport || !reflect.DeepEqual(calibration.CrossoverEvidence.Samples, calibration.CrossoverSamples) {
		return fmt.Errorf("profile calibration crossover evidence identity or samples differ")
	}
	crossover, err := CrossoverTokens(calibration.CrossoverSamples)
	if err != nil {
		return err
	}
	if calibration.MinCachedTokenDelta != crossover {
		return fmt.Errorf("profile calibration min_cached_token_delta is not the measured crossover")
	}
	if len(calibration.Prefixes) != len(profile.Prefixes) || len(profile.Prefixes) != 3 {
		return fmt.Errorf("profile calibration must bind exactly the three frozen prefix regimes")
	}
	for index, prefix := range profile.Prefixes {
		binding := calibration.Prefixes[index]
		if binding.ID != prefix.ID || len(binding.Tokenizations) != int(profile.MaxWidth)+1 {
			return fmt.Errorf("prefix %q calibration identity or suffix coverage is incomplete", prefix.ID)
		}
		rendered := strings.Repeat(prefix.RepeatText, int(prefix.RepeatCount))
		digest := sha256.Sum256([]byte(rendered))
		if binding.RenderedPrefixSHA256 != hex.EncodeToString(digest[:]) {
			return fmt.Errorf("prefix %q rendered text does not match its calibration", prefix.ID)
		}
		warmupDigest := sha256.Sum256([]byte(rendered + CalibrationWarmupSuffix))
		if binding.WarmupContentSHA256 != hex.EncodeToString(warmupDigest[:]) {
			return fmt.Errorf("prefix %q warmup content does not match its calibration", prefix.ID)
		}
		shared := LongestCommonTokenPrefix(binding.Tokenizations)
		cacheable := shared - shared%CacheBlockTokens
		if shared == 0 || cacheable == 0 || binding.ExactSharedTokens != shared || binding.CacheableSharedTokens != cacheable || prefix.EstimatedTokens != cacheable {
			return fmt.Errorf("prefix %q estimated_tokens is not its measured 64-token-aligned cacheable prefix", prefix.ID)
		}
	}
	below := profile.Prefixes[0].EstimatedTokens
	near := profile.Prefixes[1].EstimatedTokens
	above := profile.Prefixes[2].EstimatedTokens
	tolerance := uint64(math.Max(16, math.Ceil(float64(crossover)*0.05)))
	nearDelta := uint64(0)
	if near > crossover {
		nearDelta = near - crossover
	} else {
		nearDelta = crossover - near
	}
	if below >= crossover || nearDelta > tolerance || above <= crossover {
		return fmt.Errorf("measured prefix regimes do not straddle the transport crossover")
	}
	return nil
}

func nonEmptyChatTemplate(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return false
	}
}

func TokenizerInfoSHA256(calibration ProfileCalibration) (string, error) {
	var value any
	if err := json.Unmarshal(calibration.TokenizerInfo, &value); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func CrossoverTokens(samples []CrossoverSample) (uint64, error) {
	if len(samples) < 2 {
		return 0, fmt.Errorf("crossover calibration requires at least two samples")
	}
	ordered := append([]CrossoverSample(nil), samples...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].PrefixTokens < ordered[j].PrefixTokens })
	seenNonBeneficial := false
	var crossover uint64
	for index, sample := range ordered {
		if sample.PrefixTokens == 0 || !finitePositive(sample.RecomputeSeconds) || !finitePositive(sample.P2PSeconds) || sample.RecomputeTrials < 3 || sample.P2PTrials < 3 || (index > 0 && sample.PrefixTokens == ordered[index-1].PrefixTokens) {
			return 0, fmt.Errorf("crossover samples must have unique positive token counts and finite positive timings")
		}
		if sample.P2PSeconds >= sample.RecomputeSeconds {
			if crossover != 0 {
				return 0, fmt.Errorf("crossover samples reverse after P2P becomes beneficial")
			}
			seenNonBeneficial = true
		} else if crossover == 0 {
			crossover = sample.PrefixTokens
		}
	}
	if !seenNonBeneficial || crossover == 0 {
		return 0, fmt.Errorf("crossover samples require both non-beneficial and beneficial P2P observations")
	}
	return crossover, nil
}

func AggregateCrossoverTrials(trials []CrossoverTrial) ([]CrossoverSample, error) {
	type measurements struct{ recompute, p2p []float64 }
	byPrefix := make(map[uint64]*measurements)
	seen := make(map[string]struct{}, len(trials))
	for index, trial := range trials {
		if strings.TrimSpace(trial.RunID) == "" || strings.TrimSpace(trial.GroupID) == "" || strings.TrimSpace(trial.RequestID) == "" || trial.PrefixTokens == 0 || !finitePositive(trial.Seconds) || trial.ObservedAt.IsZero() {
			return nil, fmt.Errorf("crossover trial %d identity, token count, timing, and observation time are required", index+1)
		}
		identity := trial.RunID + "\x00" + trial.GroupID + "\x00" + trial.RequestID
		if _, duplicate := seen[identity]; duplicate {
			return nil, fmt.Errorf("crossover trial %d duplicates request identity", index+1)
		}
		seen[identity] = struct{}{}
		bucket := byPrefix[trial.PrefixTokens]
		if bucket == nil {
			bucket = &measurements{}
			byPrefix[trial.PrefixTokens] = bucket
		}
		switch trial.Acquisition {
		case "recompute":
			if trial.TransferBytes != 0 {
				return nil, fmt.Errorf("crossover recompute trial %d cannot contain transfer bytes", index+1)
			}
			bucket.recompute = append(bucket.recompute, trial.Seconds)
		case "p2p":
			if trial.TransferBytes == 0 {
				return nil, fmt.Errorf("crossover P2P trial %d requires measured transfer bytes", index+1)
			}
			bucket.p2p = append(bucket.p2p, trial.Seconds)
		default:
			return nil, fmt.Errorf("crossover trial %d acquisition %q is unsupported", index+1, trial.Acquisition)
		}
	}
	prefixes := make([]uint64, 0, len(byPrefix))
	for prefix := range byPrefix {
		prefixes = append(prefixes, prefix)
	}
	sort.Slice(prefixes, func(i, j int) bool { return prefixes[i] < prefixes[j] })
	samples := make([]CrossoverSample, 0, len(prefixes))
	for _, prefix := range prefixes {
		bucket := byPrefix[prefix]
		if len(bucket.recompute) < 3 || len(bucket.p2p) < 3 {
			return nil, fmt.Errorf("crossover prefix %d requires at least three recompute and three P2P trials", prefix)
		}
		samples = append(samples, CrossoverSample{
			PrefixTokens: prefix, RecomputeSeconds: median(bucket.recompute), P2PSeconds: median(bucket.p2p),
			RecomputeTrials: uint32(len(bucket.recompute)), P2PTrials: uint32(len(bucket.p2p)),
		})
	}
	if _, err := CrossoverTokens(samples); err != nil {
		return nil, err
	}
	return samples, nil
}

func median(values []float64) float64 {
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	middle := len(ordered) / 2
	if len(ordered)%2 == 1 {
		return ordered[middle]
	}
	return (ordered[middle-1] + ordered[middle]) / 2
}

func lowerHexCommit(value string) bool {
	return (len(value) == 40 || len(value) == 64) && strings.Trim(value, "0123456789abcdef") == ""
}

func lowerHexSHA256Profile(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}

func LongestCommonTokenPrefix(tokenizations [][]uint32) uint64 {
	if len(tokenizations) == 0 {
		return 0
	}
	limit := len(tokenizations[0])
	for _, tokens := range tokenizations[1:] {
		if len(tokens) < limit {
			limit = len(tokens)
		}
	}
	for index := 0; index < limit; index++ {
		value := tokenizations[0][index]
		for _, tokens := range tokenizations[1:] {
			if tokens[index] != value {
				return uint64(index)
			}
		}
	}
	return uint64(limit)
}

func finitePositive(value float64) bool {
	return value > 0 && !math.IsInf(value, 0) && !math.IsNaN(value)
}
