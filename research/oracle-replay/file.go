package replay

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/JDinSeattle/velaserve/internal/evidence"
	"github.com/JDinSeattle/velaserve/internal/fanout/planner"
	"github.com/JDinSeattle/velaserve/internal/jsonl"
	"go.yaml.in/yaml/v3"
)

const OracleSchemaVersion = "velaserve.oracle/v1"

type ReplayProfile struct {
	InflightPublicationDelayMS   float64                             `yaml:"inflight_publication_delay_ms"`
	AffinityLoadGateSeconds      float64                             `yaml:"affinity_load_gate_seconds"`
	Calibration                  ThroughputCalibration               `yaml:"calibration"`
	ServiceSecondsByOutputRegime map[string]OutputServiceCalibration `yaml:"service_seconds_by_output_regime"`
}

type ThroughputCalibration struct {
	PrefillTokensPerSecond float64 `yaml:"prefill_tokens_per_second"`
	PullBytesPerSecond     float64 `yaml:"pull_bytes_per_second"`
	BytesPerCachedToken    float64 `yaml:"bytes_per_cached_token"`
}

type OutputServiceCalibration struct {
	MaxTokens uint32  `yaml:"max_tokens"`
	Seconds   float64 `yaml:"seconds"`
}

type FileOptions struct {
	GroupsPath      string
	PlacementsPath  string
	CalibrationPath string
	OutputPath      string
	Append          bool
}

type OracleRecord struct {
	SchemaVersion     string       `json:"schema_version"`
	RunID             string       `json:"run_id"`
	GroupID           string       `json:"group_id"`
	FanoutWidth       uint32       `json:"fanout_width"`
	PrefixTokens      uint64       `json:"prefix_tokens"`
	OutputRegime      string       `json:"output_regime"`
	MaxTokens         uint32       `json:"max_tokens"`
	ServiceSeconds    float64      `json:"service_seconds"`
	GroupSHA256       string       `json:"group_sha256"`
	PlacementSHA256   string       `json:"placement_sha256"`
	CalibrationSHA256 string       `json:"calibration_sha256"`
	ArmB              ReplayResult `json:"arm_b"`
	Oracle            OracleResult `json:"oracle"`
}

func ReplayFile(options FileOptions) error {
	if strings.TrimSpace(options.OutputPath) == "" {
		return fmt.Errorf("output path is required")
	}
	if !options.Append {
		if _, err := os.Stat(options.OutputPath); err == nil {
			return fmt.Errorf("output %q already exists; pass append explicitly", options.OutputPath)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect output %q: %w", options.OutputPath, err)
		}
	}
	placements, err := jsonl.Read[evidence.PlacementEvent](options.PlacementsPath)
	if err != nil {
		return fmt.Errorf("read placements: %w", err)
	}
	if len(placements) == 0 {
		return fmt.Errorf("placements file contains no events")
	}
	for index, event := range placements {
		if err := evidence.ValidatePlacementEvent(event); err != nil {
			return fmt.Errorf("placement event %d: %w", index+1, err)
		}
	}
	results, err := jsonl.Read[evidence.GroupResult](options.GroupsPath)
	if err != nil {
		return fmt.Errorf("read groups: %w", err)
	}
	groupsByKey := make(map[string]evidence.GroupResult, len(results))
	for index, result := range results {
		if result.Arm != evidence.ArmLoadAwareP2P {
			continue
		}
		if err := evidence.ValidateGroupResult(result); err != nil {
			return fmt.Errorf("Arm-B group %d: %w", index+1, err)
		}
		key := result.RunID + "\x00" + result.GroupID
		if _, exists := groupsByKey[key]; exists {
			return fmt.Errorf("Arm-B group %d duplicates run/group identity", index+1)
		}
		groupsByKey[key] = result
	}
	if len(groupsByKey) == 0 {
		return fmt.Errorf("groups file contains no Arm-B groups")
	}
	profile, err := readProfile(options.CalibrationPath)
	if err != nil {
		return err
	}
	placementHash, err := fileSHA256(options.PlacementsPath)
	if err != nil {
		return fmt.Errorf("hash placements: %w", err)
	}
	groupHash, err := fileSHA256(options.GroupsPath)
	if err != nil {
		return fmt.Errorf("hash groups: %w", err)
	}
	calibrationHash, err := fileSHA256(options.CalibrationPath)
	if err != nil {
		return fmt.Errorf("hash calibration: %w", err)
	}

	type group struct {
		key    string
		events []evidence.PlacementEvent
	}
	placementsByKey := make(map[string][]evidence.PlacementEvent)
	for _, event := range placements {
		if event.Arm != evidence.ArmLoadAwareP2P {
			continue
		}
		key := event.RunID + "\x00" + event.GroupID
		placementsByKey[key] = append(placementsByKey[key], event)
	}
	if len(placementsByKey) == 0 {
		return fmt.Errorf("placements file contains no Arm-B groups")
	}
	groups := make([]group, 0, len(placementsByKey))
	for key, events := range placementsByKey {
		groups = append(groups, group{key: key, events: events})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].key < groups[j].key })

	records := make([]OracleRecord, 0, len(groups))
	for _, item := range groups {
		result, exists := groupsByKey[item.key]
		if !exists {
			return fmt.Errorf("placement group %q has no raw benchmark group", item.events[0].GroupID)
		}
		sort.Slice(item.events, func(i, j int) bool {
			if !item.events[i].ObservedAt.Equal(item.events[j].ObservedAt) {
				return item.events[i].ObservedAt.Before(item.events[j].ObservedAt)
			}
			return item.events[i].RequestID < item.events[j].RequestID
		})
		first := item.events[0]
		if err := validatePlacementGroup(result, item.events); err != nil {
			return fmt.Errorf("bind placement group %q: %w", first.GroupID, err)
		}
		service, exists := profile.ServiceSecondsByOutputRegime[result.Cell.OutputRegime]
		if !exists || service.MaxTokens != result.Cell.MaxTokens {
			return fmt.Errorf("group %q output regime %q/%d lacks exact service-time calibration", first.GroupID, result.Cell.OutputRegime, result.Cell.MaxTokens)
		}
		calibration := planner.Calibration{
			PrefillTokensPerSecond: profile.Calibration.PrefillTokensPerSecond,
			PullBytesPerSecond:     profile.Calibration.PullBytesPerSecond,
			BytesPerCachedToken:    profile.Calibration.BytesPerCachedToken,
			ServiceSeconds:         service.Seconds,
		}
		snapshot := withMeasuredAvailability(first.Snapshot, calibration.ServiceSeconds)
		arrivalOffsets := make([]float64, len(item.events))
		for index := range item.events {
			arrivalOffsets[index] = item.events[index].ObservedAt.Sub(first.ObservedAt).Seconds()
		}
		input := Input{
			Width:                           first.FanoutWidth,
			PrefixTokens:                    result.Cell.PrefixTokens,
			Snapshot:                        snapshot,
			Calibration:                     calibration,
			ArrivalSkewSeconds:              float64(first.ArrivalSkewMS) / 1000,
			ArrivalOffsetsSeconds:           arrivalOffsets,
			InflightPublicationDelaySeconds: profile.InflightPublicationDelayMS / 1000,
			AffinityLoadGateSeconds:         profile.AffinityLoadGateSeconds,
		}
		armB, err := EvaluateObservedArmB(item.events, calibration, result.Cell.PrefixTokens)
		if err != nil {
			return fmt.Errorf("score observed Arm B for group %q: %w", first.GroupID, err)
		}
		oracle, err := EvaluateAllWidthsAgainst(input, armB)
		if err != nil {
			return fmt.Errorf("evaluate oracle for group %q: %w", first.GroupID, err)
		}
		records = append(records, OracleRecord{
			SchemaVersion:     OracleSchemaVersion,
			RunID:             first.RunID,
			GroupID:           first.GroupID,
			FanoutWidth:       first.FanoutWidth,
			PrefixTokens:      result.Cell.PrefixTokens,
			OutputRegime:      result.Cell.OutputRegime,
			MaxTokens:         result.Cell.MaxTokens,
			ServiceSeconds:    calibration.ServiceSeconds,
			GroupSHA256:       groupHash,
			PlacementSHA256:   placementHash,
			CalibrationSHA256: calibrationHash,
			ArmB:              armB,
			Oracle:            oracle,
		})
	}
	if len(groups) != len(groupsByKey) {
		return fmt.Errorf("Arm-B group/placement identity counts differ: %d and %d", len(groupsByKey), len(groups))
	}
	for _, record := range records {
		if err := jsonl.Append(options.OutputPath, record); err != nil {
			return fmt.Errorf("append oracle record: %w", err)
		}
	}
	return nil
}

func validatePlacementGroup(group evidence.GroupResult, events []evidence.PlacementEvent) error {
	if group.Outcome != evidence.OutcomeSuccess {
		return fmt.Errorf("benchmark group outcome is %s", group.Outcome)
	}
	if len(events) != len(group.Children) || len(events) != int(group.FanoutWidth) {
		return fmt.Errorf("placement count %d does not match %d benchmark children", len(events), len(group.Children))
	}
	wantRequests := make(map[string]struct{}, len(group.Children))
	for _, child := range group.Children {
		wantRequests[child.RequestID] = struct{}{}
	}
	for _, event := range events {
		if event.RunID != group.RunID || event.GroupID != group.GroupID || event.Arm != group.Arm || event.FanoutWidth != group.FanoutWidth || event.ArrivalSkewMS != group.Cell.ArrivalSkewMS || event.EPPReplicas != group.Cell.EPPReplicas || event.LoadRegime != group.Cell.LoadRegime {
			return fmt.Errorf("placement workload fields disagree with benchmark group")
		}
		if _, exists := wantRequests[event.RequestID]; !exists {
			return fmt.Errorf("placement request %q is absent from benchmark children", event.RequestID)
		}
		delete(wantRequests, event.RequestID)
	}
	if len(wantRequests) != 0 {
		return fmt.Errorf("benchmark group has %d children without placements", len(wantRequests))
	}
	return nil
}

func withMeasuredAvailability(snapshot evidence.EndpointSnapshot, serviceSeconds float64) evidence.EndpointSnapshot {
	result := copySnapshot(snapshot)
	for index := range result.Endpoints {
		endpoint := &result.Endpoints[index]
		inflight := endpoint.RunningRequests
		if endpoint.ObservedInflight > inflight {
			inflight = endpoint.ObservedInflight
		}
		measured := float64(endpoint.QueueDepth+inflight) * serviceSeconds
		if measured > endpoint.AvailableAtSeconds {
			endpoint.AvailableAtSeconds = measured
		}
	}
	return result
}

func readProfile(path string) (ReplayProfile, error) {
	file, err := os.Open(path)
	if err != nil {
		return ReplayProfile{}, fmt.Errorf("open calibration profile: %w", err)
	}
	defer file.Close()
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	var profile ReplayProfile
	if err := decoder.Decode(&profile); err != nil {
		return ReplayProfile{}, fmt.Errorf("decode calibration profile: %w", err)
	}
	if !finitePositive(profile.Calibration.PrefillTokensPerSecond) || !finitePositive(profile.Calibration.PullBytesPerSecond) || profile.Calibration.BytesPerCachedToken == 0 || profile.InflightPublicationDelayMS < 0 || math.IsNaN(profile.InflightPublicationDelayMS) || math.IsInf(profile.InflightPublicationDelayMS, 0) || !finitePositive(profile.AffinityLoadGateSeconds) || len(profile.ServiceSecondsByOutputRegime) == 0 {
		return ReplayProfile{}, fmt.Errorf("calibration profile contains invalid throughput, delay, or service inputs")
	}
	for regime, service := range profile.ServiceSecondsByOutputRegime {
		if strings.TrimSpace(regime) == "" || service.MaxTokens == 0 || !finitePositive(service.Seconds) {
			return ReplayProfile{}, fmt.Errorf("output service calibration %q is invalid", regime)
		}
	}
	return profile, nil
}

func finitePositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
