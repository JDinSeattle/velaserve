package replay

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
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
	PrefixTokens               uint64              `yaml:"prefix_tokens"`
	InflightPublicationDelayMS float64             `yaml:"inflight_publication_delay_ms"`
	AffinityLoadGateSeconds    float64             `yaml:"affinity_load_gate_seconds"`
	Calibration                planner.Calibration `yaml:"calibration"`
}

type FileOptions struct {
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
	profile, err := readProfile(options.CalibrationPath)
	if err != nil {
		return err
	}
	placementHash, err := fileSHA256(options.PlacementsPath)
	if err != nil {
		return fmt.Errorf("hash placements: %w", err)
	}
	calibrationHash, err := fileSHA256(options.CalibrationPath)
	if err != nil {
		return fmt.Errorf("hash calibration: %w", err)
	}

	type group struct {
		key   string
		event evidence.PlacementEvent
	}
	groupsByKey := make(map[string]evidence.PlacementEvent)
	for _, event := range placements {
		key := event.RunID + "\x00" + event.GroupID
		current, exists := groupsByKey[key]
		if !exists || event.ObservedAt.Before(current.ObservedAt) {
			groupsByKey[key] = event
		}
	}
	groups := make([]group, 0, len(groupsByKey))
	for key, event := range groupsByKey {
		groups = append(groups, group{key: key, event: event})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].key < groups[j].key })

	records := make([]OracleRecord, 0, len(groups))
	for _, item := range groups {
		input := Input{
			Width:                           item.event.FanoutWidth,
			PrefixTokens:                    profile.PrefixTokens,
			Snapshot:                        item.event.Snapshot,
			Calibration:                     profile.Calibration,
			ArrivalSkewSeconds:              float64(item.event.ArrivalSkewMS) / 1000,
			InflightPublicationDelaySeconds: profile.InflightPublicationDelayMS / 1000,
			AffinityLoadGateSeconds:         profile.AffinityLoadGateSeconds,
		}
		armB, err := ReplayArmB(input)
		if err != nil {
			return fmt.Errorf("replay Arm B for group %q: %w", item.event.GroupID, err)
		}
		oracle, err := EvaluateAllWidths(input)
		if err != nil {
			return fmt.Errorf("evaluate oracle for group %q: %w", item.event.GroupID, err)
		}
		records = append(records, OracleRecord{
			SchemaVersion:     OracleSchemaVersion,
			RunID:             item.event.RunID,
			GroupID:           item.event.GroupID,
			FanoutWidth:       item.event.FanoutWidth,
			PlacementSHA256:   placementHash,
			CalibrationSHA256: calibrationHash,
			ArmB:              armB,
			Oracle:            oracle,
		})
	}
	for _, record := range records {
		if err := jsonl.Append(options.OutputPath, record); err != nil {
			return fmt.Errorf("append oracle record: %w", err)
		}
	}
	return nil
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
	return profile, nil
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
