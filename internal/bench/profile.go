package bench

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"
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
	SchemaVersion  string                `yaml:"schema_version"`
	Seed           uint64                `yaml:"seed"`
	RunIDPrefix    string                `yaml:"run_id_prefix"`
	Model          string                `yaml:"model"`
	MaxWidth       uint32                `yaml:"max_width"`
	TimeoutMS      uint32                `yaml:"timeout_ms"`
	Repetitions    uint32                `yaml:"repetitions"`
	Widths         []uint32              `yaml:"widths"`
	ArrivalSkewsMS []uint32              `yaml:"arrival_skews_ms"`
	Arms           []evidence.Arm        `yaml:"arms"`
	Prefixes       []PrefixProfile       `yaml:"prefixes"`
	Outputs        []OutputProfile       `yaml:"outputs"`
	LoadRegimes    []evidence.LoadRegime `yaml:"load_regimes"`
	CacheStates    []string              `yaml:"cache_states"`
	Transports     []string              `yaml:"transports"`
	EPPReplicas    []uint32              `yaml:"epp_replicas"`
	Suffixes       []string              `yaml:"suffixes"`
}

type PrefixProfile struct {
	ID              string `yaml:"id"`
	EstimatedTokens uint64 `yaml:"estimated_tokens"`
	RepeatText      string `yaml:"repeat_text"`
	RepeatCount     uint32 `yaml:"repeat_count"`
}

type OutputProfile struct {
	ID        string `yaml:"id"`
	MaxTokens uint32 `yaml:"max_tokens"`
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
	if groupCount > 10_000_000 {
		return nil, fmt.Errorf("profile expands to %d groups, exceeding safety limit", groupCount)
	}
	requests := make([]GroupRequest, 0, int(groupCount))
	cellIndex := uint64(0)
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
										arms := orderedArms(profile.Seed, cellIndex, profile.Arms)
										for _, arm := range arms {
											requests = append(requests, GroupRequest{
												RunID:        profile.RunIDPrefix,
												Arm:          arm,
												Model:        profile.Model,
												CommonPrefix: renderedPrefix,
												Suffixes:     append([]string(nil), profile.Suffixes[:width]...),
												MaxTokens:    output.MaxTokens,
												MaxWidth:     profile.MaxWidth,
												ArrivalSkew:  time.Duration(skew) * time.Millisecond,
												Timeout:      time.Duration(profile.TimeoutMS) * time.Millisecond,
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
