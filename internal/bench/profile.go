package bench

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/JDinSeattle/velaserve/internal/evidence"
	"go.yaml.in/yaml/v3"
)

const (
	BenchmarkProfileSchemaVersion = "velaserve.benchmark-profile/v1"
	maxProfileBytes               = 1 << 20
	maxRenderedPrefixBytes        = 8 << 20
)

type Profile struct {
	SchemaVersion      string                `yaml:"schema_version" json:"schema_version"`
	Seed               uint64                `yaml:"seed" json:"seed"`
	RunIDPrefix        string                `yaml:"run_id_prefix" json:"run_id_prefix"`
	Model              string                `yaml:"model" json:"model"`
	MaxWidth           uint32                `yaml:"max_width" json:"max_width"`
	TimeoutMS          uint32                `yaml:"timeout_ms" json:"timeout_ms"`
	Repetitions        uint32                `yaml:"repetitions" json:"repetitions"`
	Widths             []uint32              `yaml:"widths" json:"widths"`
	ArrivalSkewsMS     []uint32              `yaml:"arrival_skews_ms" json:"arrival_skews_ms"`
	Arms               []evidence.Arm        `yaml:"arms" json:"arms"`
	Prefixes           []PrefixProfile       `yaml:"prefixes" json:"prefixes"`
	Outputs            []OutputProfile       `yaml:"outputs" json:"outputs"`
	LoadRegimes        []evidence.LoadRegime `yaml:"load_regimes" json:"load_regimes"`
	CacheStates        []string              `yaml:"cache_states" json:"cache_states"`
	PrefixSourceCounts []uint32              `yaml:"prefix_source_counts,omitempty" json:"prefix_source_counts,omitempty"`
	Transports         []string              `yaml:"transports" json:"transports"`
	EPPReplicas        []uint32              `yaml:"epp_replicas" json:"epp_replicas"`
	Suffixes           []string              `yaml:"suffixes" json:"suffixes"`
}

type PrefixProfile struct {
	ID              string `yaml:"id" json:"id"`
	EstimatedTokens uint64 `yaml:"estimated_tokens" json:"estimated_tokens"`
	RepeatText      string `yaml:"repeat_text" json:"repeat_text"`
	RepeatCount     uint32 `yaml:"repeat_count" json:"repeat_count"`
}

type OutputProfile struct {
	ID        string `yaml:"id" json:"id"`
	MaxTokens uint32 `yaml:"max_tokens" json:"max_tokens"`
}

// ValidateAWSZ0Profile binds real-GPU evidence to the complete preregistered
// matrix. The model, transport, EPP replica count, and active arm are bound by
// the deployment preflight; every other factor is fixed here independently of
// the profile carried by an artifact bundle.
func ValidateAWSZ0Profile(profile Profile, phase string) error {
	if err := validateProfile(profile); err != nil {
		return err
	}
	if profile.Seed != 20260825 || profile.RunIDPrefix != "aws-z0" || profile.MaxWidth != 16 || profile.TimeoutMS != 180000 || profile.Repetitions != 3 {
		return fmt.Errorf("AWS Z0 seed, run prefix, width, timeout, and three repetitions are frozen")
	}
	if !reflect.DeepEqual(profile.Widths, []uint32{2, 4, 8, 16}) || !reflect.DeepEqual(profile.ArrivalSkewsMS, []uint32{0, 1, 5, 20}) {
		return fmt.Errorf("AWS Z0 widths and arrival skews do not match the frozen matrix")
	}
	wantPrefixIDs := []string{"below-zeroing-crossover", "near-zeroing-crossover", "above-zeroing-crossover"}
	wantOutputs := []OutputProfile{{ID: "short", MaxTokens: 32}, {ID: "moderate", MaxTokens: 128}}
	wantLoads := []evidence.LoadRegime{evidence.LoadIdle, evidence.LoadModerate, evidence.LoadNearSaturation}
	if len(profile.Prefixes) != len(wantPrefixIDs) {
		return fmt.Errorf("AWS Z0 requires three tokenizer-calibrated prefix regimes")
	}
	for index, prefix := range profile.Prefixes {
		if prefix.ID != wantPrefixIDs[index] || prefix.RepeatText != "shared zeroing context token " || prefix.EstimatedTokens == 0 || prefix.RepeatCount == 0 {
			return fmt.Errorf("AWS Z0 prefix regime %d is not a generated tokenizer calibration", index+1)
		}
		if index > 0 && prefix.EstimatedTokens <= profile.Prefixes[index-1].EstimatedTokens {
			return fmt.Errorf("AWS Z0 calibrated prefix token counts must increase")
		}
	}
	if !reflect.DeepEqual(profile.Outputs, wantOutputs) || !reflect.DeepEqual(profile.LoadRegimes, wantLoads) {
		return fmt.Errorf("AWS Z0 output and load factors do not match the frozen matrix")
	}
	wantCaches := []string{"warm-owner", "distributed-warm", "cold"}
	switch phase {
	case "z0-a", "z0-b":
	case "z0-c":
		wantCaches = []string{"distributed-warm"}
	default:
		return fmt.Errorf("AWS Z0 phase %q is unsupported", phase)
	}
	if !reflect.DeepEqual(profile.CacheStates, wantCaches) {
		return fmt.Errorf("AWS %s cache states do not match the frozen matrix", phase)
	}
	if phase == "z0-c" {
		if !reflect.DeepEqual(profile.PrefixSourceCounts, []uint32{1, 2, 4}) {
			return fmt.Errorf("AWS Z0-C requires the frozen interleaved prefix-source counts [1 2 4]")
		}
	} else if len(profile.PrefixSourceCounts) != 0 {
		return fmt.Errorf("AWS %s cannot include prefix-source-count conditions", phase)
	}
	if len(profile.Transports) != 1 || (profile.Transports[0] != "tcp" && profile.Transports[0] != "efa") || len(profile.EPPReplicas) != 1 || (profile.EPPReplicas[0] != 1 && profile.EPPReplicas[0] != 2) {
		return fmt.Errorf("AWS Z0 requires one bound transport and one bound EPP replica count")
	}
	if phase == "z0-b" || phase == "z0-c" {
		if !reflect.DeepEqual(profile.Arms, []evidence.Arm{evidence.ArmLoadAwareP2P}) {
			return fmt.Errorf("AWS %s gate evidence must contain only Arm B", phase)
		}
	}
	wantSuffixes := make([]string, 16)
	for index := range wantSuffixes {
		wantSuffixes[index] = fmt.Sprintf("\nCandidate %02d: answer independently.", index+1)
	}
	if !reflect.DeepEqual(profile.Suffixes, wantSuffixes) {
		return fmt.Errorf("AWS Z0 suffix corpus does not match the frozen matrix")
	}
	return nil
}

func LoadProfile(path string) (Profile, error) {
	if strings.TrimSpace(path) == "" {
		return Profile{}, fmt.Errorf("profile path is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return Profile{}, fmt.Errorf("open profile: %w", err)
	}
	defer file.Close()

	limited := io.LimitReader(file, maxProfileBytes+1)
	contents, err := io.ReadAll(limited)
	if err != nil {
		return Profile{}, fmt.Errorf("read profile: %w", err)
	}
	if len(contents) > maxProfileBytes {
		return Profile{}, fmt.Errorf("profile exceeds %d bytes", maxProfileBytes)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	var profile Profile
	if err := decoder.Decode(&profile); err != nil {
		return Profile{}, fmt.Errorf("decode profile: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Profile{}, fmt.Errorf("decode profile: multiple YAML documents are not allowed")
		}
		return Profile{}, fmt.Errorf("decode trailing profile content: %w", err)
	}
	if err := validateProfile(profile); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

// ExpandProfile returns every preregistered workload cell. Arm order inside a
// cell is hash-randomized by the frozen seed and remains stable across runs.
func ExpandProfile(profile Profile) ([]GroupRequest, error) {
	if err := validateProfile(profile); err != nil {
		return nil, err
	}
	groupCount := uint64(profile.Repetitions) * uint64(len(profile.Prefixes)) * uint64(len(profile.Outputs)) *
		uint64(len(profile.ArrivalSkewsMS)) * uint64(len(profile.LoadRegimes)) * uint64(len(profile.CacheStates)) *
		uint64(len(profile.Transports)) * uint64(len(profile.EPPReplicas)) * uint64(len(profile.Widths)) * uint64(len(profile.Arms))
	if len(profile.PrefixSourceCounts) > 0 {
		groupCount *= uint64(len(profile.PrefixSourceCounts))
	}
	if groupCount > 10_000_000 {
		return nil, fmt.Errorf("profile expands to %d groups, exceeding safety limit", groupCount)
	}
	requests := make([]GroupRequest, 0, int(groupCount))
	cellIndex := uint64(0)
	sourceCounts := profile.PrefixSourceCounts
	if len(sourceCounts) == 0 {
		sourceCounts = []uint32{0}
	}
	for repetition := uint32(1); repetition <= profile.Repetitions; repetition++ {
		for _, prefix := range profile.Prefixes {
			renderedPrefix := strings.Repeat(prefix.RepeatText, int(prefix.RepeatCount))
			for _, output := range profile.Outputs {
				for _, skew := range profile.ArrivalSkewsMS {
					for _, load := range profile.LoadRegimes {
						for _, cacheState := range profile.CacheStates {
							for _, transport := range profile.Transports {
								for _, replicas := range profile.EPPReplicas {
									for _, width := range profile.Widths {
										cellIndex++
										ownerRotation := uint32(cellIndex - 1)
										for _, sourceCount := range orderedSourceCounts(profile.Seed, cellIndex, sourceCounts) {
											arms := orderedArms(profile.Seed, cellIndex+uint64(sourceCount), profile.Arms)
											for _, arm := range arms {
												requests = append(requests, GroupRequest{
													RunID:             profile.RunIDPrefix,
													Arm:               arm,
													PrefixSourceCount: sourceCount,
													OwnerRotation:     ownerRotation,
													Model:             profile.Model,
													CommonPrefix:      renderedPrefix,
													WarmupContent:     renderedPrefix + CalibrationWarmupSuffix,
													Suffixes:          append([]string(nil), profile.Suffixes[:width]...),
													MaxTokens:         output.MaxTokens,
													MaxWidth:          profile.MaxWidth,
													ArrivalSkew:       time.Duration(skew) * time.Millisecond,
													Timeout:           time.Duration(profile.TimeoutMS) * time.Millisecond,
													Cell: evidence.BenchmarkCell{
														Repetition:    repetition,
														PrefixRegime:  prefix.ID,
														PrefixTokens:  prefix.EstimatedTokens,
														OutputRegime:  output.ID,
														MaxTokens:     output.MaxTokens,
														ArrivalSkewMS: skew,
														LoadRegime:    load,
														CacheState:    cacheState,
														Transport:     transport,
														EPPReplicas:   replicas,
													},
												})
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}
	return requests, nil
}

func validateProfile(profile Profile) error {
	if profile.SchemaVersion != BenchmarkProfileSchemaVersion {
		return fmt.Errorf("schema_version: got %q, want %q", profile.SchemaVersion, BenchmarkProfileSchemaVersion)
	}
	if profile.Seed == 0 {
		return fmt.Errorf("seed must be positive")
	}
	if strings.TrimSpace(profile.RunIDPrefix) == "" || strings.ContainsAny(profile.RunIDPrefix, " \t\r\n") {
		return fmt.Errorf("run_id_prefix must be a non-empty token")
	}
	if strings.TrimSpace(profile.Model) == "" {
		return fmt.Errorf("model is required")
	}
	if profile.MaxWidth != 16 {
		return fmt.Errorf("max_width: got %d, want frozen value 16", profile.MaxWidth)
	}
	if profile.TimeoutMS == 0 || profile.Repetitions == 0 {
		return fmt.Errorf("timeout_ms and repetitions must be positive")
	}
	if !equalUint32s(profile.Widths, []uint32{2, 4, 8, 16}) {
		return fmt.Errorf("widths must equal frozen Z0 matrix [2 4 8 16]")
	}
	if !equalUint32s(profile.ArrivalSkewsMS, []uint32{0, 1, 5, 20}) {
		return fmt.Errorf("arrival_skews_ms must equal frozen Z0 matrix [0 1 5 20]")
	}
	if len(profile.Suffixes) < int(profile.MaxWidth) {
		return fmt.Errorf("suffixes: got %d, need at least max_width %d", len(profile.Suffixes), profile.MaxWidth)
	}
	for index, suffix := range profile.Suffixes[:profile.MaxWidth] {
		if strings.TrimSpace(suffix) == "" {
			return fmt.Errorf("suffixes[%d] is empty", index)
		}
	}
	if err := validateArms(profile.Arms); err != nil {
		return err
	}
	if len(profile.Prefixes) == 0 || len(profile.Outputs) == 0 || len(profile.LoadRegimes) == 0 || len(profile.CacheStates) == 0 || len(profile.Transports) == 0 || len(profile.EPPReplicas) == 0 {
		return fmt.Errorf("all profile factor lists must be non-empty")
	}
	seenPrefix := map[string]struct{}{}
	for index, prefix := range profile.Prefixes {
		if strings.TrimSpace(prefix.ID) == "" || prefix.EstimatedTokens == 0 || strings.TrimSpace(prefix.RepeatText) == "" || prefix.RepeatCount == 0 {
			return fmt.Errorf("prefixes[%d] is incomplete", index)
		}
		if uint64(len(prefix.RepeatText))*uint64(prefix.RepeatCount) > maxRenderedPrefixBytes {
			return fmt.Errorf("prefixes[%d] renders above %d bytes", index, maxRenderedPrefixBytes)
		}
		if _, exists := seenPrefix[prefix.ID]; exists {
			return fmt.Errorf("prefixes[%d] repeats ID %q", index, prefix.ID)
		}
		seenPrefix[prefix.ID] = struct{}{}
	}
	seenOutput := map[string]struct{}{}
	for index, output := range profile.Outputs {
		if strings.TrimSpace(output.ID) == "" || output.MaxTokens == 0 {
			return fmt.Errorf("outputs[%d] is incomplete", index)
		}
		if _, exists := seenOutput[output.ID]; exists {
			return fmt.Errorf("outputs[%d] repeats ID %q", index, output.ID)
		}
		seenOutput[output.ID] = struct{}{}
	}
	for index, load := range profile.LoadRegimes {
		switch load {
		case evidence.LoadIdle, evidence.LoadModerate, evidence.LoadNearSaturation:
		default:
			return fmt.Errorf("load_regimes[%d] has unsupported value %q", index, load)
		}
	}
	for index, state := range profile.CacheStates {
		switch state {
		case "warm-owner", "distributed-warm", "cold":
		default:
			return fmt.Errorf("cache_states[%d] has unsupported value %q", index, state)
		}
	}
	seenSourceCounts := make(map[uint32]struct{}, len(profile.PrefixSourceCounts))
	for index, count := range profile.PrefixSourceCounts {
		if count != 1 && count != 2 && count != 4 {
			return fmt.Errorf("prefix_source_counts[%d]: got %d, want 1, 2, or 4", index, count)
		}
		if _, exists := seenSourceCounts[count]; exists {
			return fmt.Errorf("prefix_source_counts[%d] repeats %d", index, count)
		}
		seenSourceCounts[count] = struct{}{}
	}
	for index, transport := range profile.Transports {
		if transport != "tcp" && !strings.HasSuffix(transport, "-verified") {
			return fmt.Errorf("transports[%d] must be tcp or explicitly end in -verified", index)
		}
	}
	for index, replicas := range profile.EPPReplicas {
		if replicas != 1 && replicas != 2 {
			return fmt.Errorf("epp_replicas[%d]: got %d, want 1 or 2", index, replicas)
		}
	}
	return nil
}

func validateArms(arms []evidence.Arm) error {
	if len(arms) == 0 {
		return fmt.Errorf("arms must be non-empty")
	}
	seen := map[evidence.Arm]struct{}{}
	for index, arm := range arms {
		switch arm {
		case evidence.ArmAffinityP2P, evidence.ArmLoadAwareP2P, evidence.ArmLoadAwareNoP2P:
		default:
			return fmt.Errorf("arms[%d] %q is not an upstream Stage 1 baseline", index, arm)
		}
		if _, exists := seen[arm]; exists {
			return fmt.Errorf("arms[%d] repeats %q", index, arm)
		}
		seen[arm] = struct{}{}
	}
	return nil
}

func orderedArms(seed, cell uint64, arms []evidence.Arm) []evidence.Arm {
	ordered := append([]evidence.Arm(nil), arms...)
	sort.SliceStable(ordered, func(left, right int) bool {
		return armRank(seed, cell, ordered[left]) < armRank(seed, cell, ordered[right])
	})
	return ordered
}

func orderedSourceCounts(seed, cell uint64, counts []uint32) []uint32 {
	ordered := append([]uint32(nil), counts...)
	sort.SliceStable(ordered, func(left, right int) bool {
		return sourceCountRank(seed, cell, ordered[left]) < sourceCountRank(seed, cell, ordered[right])
	})
	return ordered
}

func sourceCountRank(seed, cell uint64, count uint32) uint64 {
	var numbers [20]byte
	binary.BigEndian.PutUint64(numbers[:8], seed)
	binary.BigEndian.PutUint64(numbers[8:16], cell)
	binary.BigEndian.PutUint32(numbers[16:], count)
	digest := sha256.Sum256(numbers[:])
	return binary.BigEndian.Uint64(digest[:8])
}

func armRank(seed, cell uint64, arm evidence.Arm) uint64 {
	var numbers [16]byte
	binary.BigEndian.PutUint64(numbers[:8], seed)
	binary.BigEndian.PutUint64(numbers[8:], cell)
	hash := sha256.New()
	_, _ = hash.Write(numbers[:])
	_, _ = hash.Write([]byte(arm))
	sum := hash.Sum(nil)
	return binary.BigEndian.Uint64(sum[:8])
}

func equalUint32s(left, right []uint32) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
