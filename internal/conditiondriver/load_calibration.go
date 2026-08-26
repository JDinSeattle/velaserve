package conditiondriver

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/JDinSeattle/velaserve/internal/evidence"
)

const LoadCalibrationSchemaVersion = "velaserve.load-calibration/v4"

// LoadCalibration is the retained raw saturation sweep used to derive every
// background-load profile. The benchmark driver never accepts a hand-entered
// saturation rate that cannot be reproduced from these request counts.
type LoadCalibration struct {
	SchemaVersion            string                    `json:"schema_version"`
	ModelID                  string                    `json:"model_id"`
	ModelRevision            string                    `json:"model_revision"`
	ModelImage               string                    `json:"model_image"`
	InstanceType             string                    `json:"instance_type"`
	GPUModel                 string                    `json:"gpu_model"`
	GPUDriverVersion         string                    `json:"gpu_driver_version"`
	ReplicaCount             uint32                    `json:"replica_count"`
	GPUsPerReplica           uint32                    `json:"gpus_per_replica"`
	Transport                string                    `json:"transport"`
	ActiveArm                evidence.Arm              `json:"active_arm"`
	RoutingSHA256            string                    `json:"routing_sha256"`
	RouterConfigSHA256       string                    `json:"router_config_sha256"`
	ProfileCalibrationSHA256 string                    `json:"profile_calibration_sha256"`
	GatewayChatURL           string                    `json:"gateway_chat_url"`
	Endpoints                []LoadCalibrationEndpoint `json:"endpoints"`
	MeasuredAt               time.Time                 `json:"measured_at"`
	Trials                   []LoadCalibrationTrial    `json:"trials"`
}

type LoadCalibrationEndpoint struct {
	ID         string `json:"id"`
	PodUID     string `json:"pod_uid"`
	MetricsURL string `json:"metrics_url"`
}

type LoadCalibrationDrainEndpointObservation struct {
	ID      string  `json:"id"`
	PodUID  string  `json:"pod_uid"`
	Running float64 `json:"running"`
	Waiting float64 `json:"waiting"`
}

type LoadCalibrationDrainSample struct {
	ObservedAt time.Time                                 `json:"observed_at"`
	Endpoints  []LoadCalibrationDrainEndpointObservation `json:"endpoints"`
}

type LoadCalibrationDrainProof struct {
	StartedAt     time.Time                    `json:"started_at"`
	CompletedAt   time.Time                    `json:"completed_at"`
	StableSamples uint32                       `json:"stable_samples"`
	Samples       []LoadCalibrationDrainSample `json:"samples"`
}

type LoadCalibrationTrial struct {
	TrialID                      string                    `json:"trial_id"`
	PrefixTokens                 uint64                    `json:"prefix_tokens"`
	MaxTokens                    uint32                    `json:"max_tokens"`
	WarmupDurationSeconds        float64                   `json:"warmup_duration_seconds"`
	WarmupStartedAt              time.Time                 `json:"warmup_started_at"`
	WarmupCompletedAt            time.Time                 `json:"warmup_completed_at"`
	WarmupOfferedRequests        uint64                    `json:"warmup_offered_requests"`
	WarmupSuccessfulRequests     uint64                    `json:"warmup_successful_requests"`
	WarmupLateSuccessfulRequests uint64                    `json:"warmup_late_successful_requests"`
	WarmupFailedRequests         uint64                    `json:"warmup_failed_requests"`
	WarmupDroppedArrivals        uint64                    `json:"warmup_dropped_arrivals"`
	DurationSeconds              float64                   `json:"duration_seconds"`
	MeasurementStartedAt         time.Time                 `json:"measurement_started_at"`
	MeasurementSchedulingEndedAt time.Time                 `json:"measurement_scheduling_ended_at"`
	MeasurementCompletedAt       time.Time                 `json:"measurement_completed_at"`
	OfferedRequests              uint64                    `json:"offered_requests"`
	SuccessfulRequests           uint64                    `json:"successful_requests"`
	LateSuccessfulRequests       uint64                    `json:"late_successful_requests"`
	FailedRequests               uint64                    `json:"failed_requests"`
	DroppedArrivals              uint64                    `json:"dropped_arrivals"`
	MeanLatencySeconds           float64                   `json:"mean_latency_seconds"`
	PreDrain                     LoadCalibrationDrainProof `json:"pre_drain"`
	WarmupDrain                  LoadCalibrationDrainProof `json:"warmup_drain"`
	PostDrain                    LoadCalibrationDrainProof `json:"post_drain"`
}

type loadShape struct {
	prefixTokens uint64
	maxTokens    uint32
}

// LoadCalibrationSHA256 returns a canonical digest independent of trial input
// order. The same implementation is used by the image, preflight, and bundle.
func LoadCalibrationSHA256(calibration LoadCalibration) (string, error) {
	calibration.Endpoints = append([]LoadCalibrationEndpoint(nil), calibration.Endpoints...)
	sort.Slice(calibration.Endpoints, func(i, j int) bool { return calibration.Endpoints[i].ID < calibration.Endpoints[j].ID })
	calibration.Trials = append([]LoadCalibrationTrial(nil), calibration.Trials...)
	for index := range calibration.Trials {
		normalizeLoadCalibrationDrainProof(&calibration.Trials[index].PreDrain)
		normalizeLoadCalibrationDrainProof(&calibration.Trials[index].WarmupDrain)
		normalizeLoadCalibrationDrainProof(&calibration.Trials[index].PostDrain)
	}
	sort.Slice(calibration.Trials, func(i, j int) bool { return calibration.Trials[i].TrialID < calibration.Trials[j].TrialID })
	encoded, err := json.Marshal(calibration)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", digest), nil
}

func normalizeLoadCalibrationDrainProof(proof *LoadCalibrationDrainProof) {
	proof.Samples = append([]LoadCalibrationDrainSample(nil), proof.Samples...)
	for index := range proof.Samples {
		proof.Samples[index].Endpoints = append([]LoadCalibrationDrainEndpointObservation(nil), proof.Samples[index].Endpoints...)
		sort.Slice(proof.Samples[index].Endpoints, func(i, j int) bool {
			return proof.Samples[index].Endpoints[i].ID < proof.Samples[index].Endpoints[j].ID
		})
	}
}

func ValidateLoadCalibration(calibration LoadCalibration, profiles []LoadProfile, model string) (string, error) {
	if calibration.SchemaVersion != LoadCalibrationSchemaVersion || strings.TrimSpace(calibration.ModelID) == "" || calibration.ModelID != model || !lowerHexCommit(calibration.ModelRevision) || !digestAddressedImage(calibration.ModelImage) || strings.TrimSpace(calibration.InstanceType) == "" || strings.TrimSpace(calibration.GPUModel) == "" || strings.TrimSpace(calibration.GPUDriverVersion) == "" || calibration.ReplicaCount < 2 || calibration.GPUsPerReplica != 1 || (calibration.Transport != "tcp" && calibration.Transport != "efa") || (calibration.ActiveArm != evidence.ArmAffinityP2P && calibration.ActiveArm != evidence.ArmLoadAwareP2P) || !lowerHexSHA256(calibration.RoutingSHA256) || !lowerHexSHA256(calibration.RouterConfigSHA256) || !lowerHexSHA256(calibration.ProfileCalibrationSHA256) || !validHTTPURL(calibration.GatewayChatURL) || calibration.MeasuredAt.IsZero() {
		return "", fmt.Errorf("load calibration deployment identity is incomplete or invalid")
	}
	endpointByID, err := validateLoadCalibrationEndpoints(calibration.Endpoints, calibration.ReplicaCount)
	if err != nil {
		return "", err
	}
	digest, err := LoadCalibrationSHA256(calibration)
	if err != nil {
		return "", err
	}
	byShape := make(map[loadShape][]LoadCalibrationTrial)
	seenTrials := make(map[string]struct{}, len(calibration.Trials))
	for index, trial := range calibration.Trials {
		trial.TrialID = strings.TrimSpace(trial.TrialID)
		if trial.TrialID == "" || trial.PrefixTokens == 0 || trial.MaxTokens == 0 || !finitePositive(trial.WarmupDurationSeconds) || trial.WarmupDurationSeconds < 5 || !finitePositive(trial.DurationSeconds) || trial.DurationSeconds < 30 || trial.WarmupOfferedRequests == 0 || trial.WarmupSuccessfulRequests == 0 || trial.WarmupSuccessfulRequests+trial.WarmupLateSuccessfulRequests+trial.WarmupFailedRequests+trial.WarmupDroppedArrivals != trial.WarmupOfferedRequests || trial.WarmupDroppedArrivals != 0 || trial.OfferedRequests == 0 || trial.SuccessfulRequests == 0 || trial.SuccessfulRequests+trial.LateSuccessfulRequests+trial.FailedRequests+trial.DroppedArrivals != trial.OfferedRequests || trial.DroppedArrivals != 0 || !finitePositive(trial.MeanLatencySeconds) || trial.WarmupStartedAt.IsZero() || trial.WarmupCompletedAt.Before(trial.WarmupStartedAt) || trial.MeasurementStartedAt.IsZero() || trial.MeasurementSchedulingEndedAt.Before(trial.MeasurementStartedAt) || trial.MeasurementCompletedAt.Before(trial.MeasurementSchedulingEndedAt) || trial.PreDrain.CompletedAt.After(trial.WarmupStartedAt) || trial.WarmupCompletedAt.After(trial.WarmupDrain.StartedAt) || trial.WarmupDrain.CompletedAt.After(trial.MeasurementStartedAt) || trial.MeasurementCompletedAt.After(trial.PostDrain.StartedAt) {
			return "", fmt.Errorf("load calibration trial %d is incomplete or arithmetically invalid", index+1)
		}
		for name, proof := range map[string]LoadCalibrationDrainProof{"pre": trial.PreDrain, "warmup": trial.WarmupDrain, "post": trial.PostDrain} {
			if err := validateLoadCalibrationDrainProof(proof, endpointByID); err != nil {
				return "", fmt.Errorf("load calibration trial %d %s-drain proof: %w", index+1, name, err)
			}
		}
		if _, exists := seenTrials[trial.TrialID]; exists {
			return "", fmt.Errorf("load calibration trial ID %q is duplicated", trial.TrialID)
		}
		seenTrials[trial.TrialID] = struct{}{}
		shape := loadShape{prefixTokens: trial.PrefixTokens, maxTokens: trial.MaxTokens}
		byShape[shape] = append(byShape[shape], trial)
	}
	if len(byShape) != len(profiles) {
		return "", fmt.Errorf("load calibration shape count does not match load profiles")
	}
	for _, profile := range profiles {
		shape := loadShape{prefixTokens: profile.PrefixTokens, maxTokens: profile.MaxTokens}
		trials := byShape[shape]
		if len(trials) < 5 {
			return "", fmt.Errorf("load calibration shape prefix_tokens=%d max_tokens=%d needs at least five ramp trials", profile.PrefixTokens, profile.MaxTokens)
		}
		sort.Slice(trials, func(i, j int) bool { return offeredQPS(trials[i]) < offeredQPS(trials[j]) })
		maximumAchieved := 0.0
		previousOffered := -1.0
		overloadObserved := false
		for _, trial := range trials {
			offered := offeredQPS(trial)
			achieved := achievedQPS(trial)
			if offered <= previousOffered || achieved > offered+1e-9 {
				return "", fmt.Errorf("load calibration ramp must have unique increasing offered rates and achieved <= offered")
			}
			previousOffered = offered
			maximumAchieved = math.Max(maximumAchieved, achieved)
			if achieved/offered <= 0.95 {
				overloadObserved = true
			}
		}
		if !overloadObserved || previousOffered < maximumAchieved*1.10 {
			return "", fmt.Errorf("load calibration shape prefix_tokens=%d max_tokens=%d never demonstrates overload beyond saturation", profile.PrefixTokens, profile.MaxTokens)
		}
		if math.Abs(profile.SaturationQPS-maximumAchieved) > math.Max(1e-6, maximumAchieved*0.005) {
			return "", fmt.Errorf("load profile saturation for prefix_tokens=%d max_tokens=%d is not derived from raw trials", profile.PrefixTokens, profile.MaxTokens)
		}
		if profile.CalibrationSHA256 != digest || profile.MeasurementSource != "load-calibration:"+digest {
			return "", fmt.Errorf("load profile is not bound to the exact calibration artifact")
		}
		delete(byShape, shape)
	}
	if len(byShape) != 0 {
		return "", fmt.Errorf("load calibration includes unregistered shapes")
	}
	return digest, nil
}

func validateLoadCalibrationEndpoints(endpoints []LoadCalibrationEndpoint, replicaCount uint32) (map[string]LoadCalibrationEndpoint, error) {
	if len(endpoints) != int(replicaCount) {
		return nil, fmt.Errorf("load calibration endpoints must correspond one-to-one with every replica")
	}
	byID := make(map[string]LoadCalibrationEndpoint, len(endpoints))
	seenUIDs := make(map[string]struct{}, len(endpoints))
	for index, endpoint := range endpoints {
		if strings.TrimSpace(endpoint.ID) == "" || strings.TrimSpace(endpoint.PodUID) == "" || !validHTTPURL(endpoint.MetricsURL) {
			return nil, fmt.Errorf("load calibration endpoint %d is incomplete", index+1)
		}
		if _, exists := byID[endpoint.ID]; exists {
			return nil, fmt.Errorf("load calibration endpoint ID %q is duplicated", endpoint.ID)
		}
		if _, exists := seenUIDs[endpoint.PodUID]; exists {
			return nil, fmt.Errorf("load calibration endpoint pod UID %q is duplicated", endpoint.PodUID)
		}
		byID[endpoint.ID] = endpoint
		seenUIDs[endpoint.PodUID] = struct{}{}
	}
	return byID, nil
}

func validateLoadCalibrationDrainProof(proof LoadCalibrationDrainProof, endpoints map[string]LoadCalibrationEndpoint) error {
	if proof.StartedAt.IsZero() || proof.CompletedAt.Before(proof.StartedAt) || proof.StableSamples != 2 || len(proof.Samples) < int(proof.StableSamples) {
		return fmt.Errorf("timestamps, two stable samples, and raw samples are required")
	}
	previous := proof.StartedAt
	for sampleIndex, sample := range proof.Samples {
		if sample.ObservedAt.Before(previous) || sample.ObservedAt.After(proof.CompletedAt) || len(sample.Endpoints) != len(endpoints) {
			return fmt.Errorf("sample %d chronology or endpoint count is invalid", sampleIndex+1)
		}
		previous = sample.ObservedAt
		seen := make(map[string]struct{}, len(sample.Endpoints))
		for _, observation := range sample.Endpoints {
			endpoint, exists := endpoints[observation.ID]
			if !exists || observation.PodUID != endpoint.PodUID || math.IsNaN(observation.Running) || math.IsInf(observation.Running, 0) || observation.Running < 0 || math.IsNaN(observation.Waiting) || math.IsInf(observation.Waiting, 0) || observation.Waiting < 0 {
				return fmt.Errorf("sample %d contains an invalid endpoint observation", sampleIndex+1)
			}
			if _, duplicate := seen[observation.ID]; duplicate {
				return fmt.Errorf("sample %d duplicates endpoint %q", sampleIndex+1, observation.ID)
			}
			seen[observation.ID] = struct{}{}
		}
		if sampleIndex >= len(proof.Samples)-int(proof.StableSamples) {
			for _, observation := range sample.Endpoints {
				if observation.Running != 0 || observation.Waiting != 0 {
					return fmt.Errorf("final stable sample %d is not idle", sampleIndex+1)
				}
			}
		}
	}
	return nil
}

func offeredQPS(trial LoadCalibrationTrial) float64 {
	return float64(trial.OfferedRequests) / trial.DurationSeconds
}

func achievedQPS(trial LoadCalibrationTrial) float64 {
	return float64(trial.SuccessfulRequests) / trial.DurationSeconds
}

func finitePositive(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value > 0
}

func lowerHexCommit(value string) bool {
	return len(value) >= 40 && len(value) <= 64 && strings.Trim(value, "0123456789abcdef") == ""
}

func digestAddressedImage(value string) bool {
	parts := strings.Split(value, "@sha256:")
	return len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && lowerHexSHA256(parts[1])
}
