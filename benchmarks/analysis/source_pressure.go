package analysis

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/JDinSeattle/velaserve/internal/evidence"
	placementrecorder "github.com/JDinSeattle/velaserve/research/placement-recorder"
)

type SourcePressureRun struct {
	CandidateSourceCount uint32
	Groups               []evidence.GroupResult
	Observations         []placementrecorder.SourcePressureObservation
}

type sourceCoordinate struct {
	pair   string
	width  uint32
	load   evidence.LoadRegime
	prefix string
	trans  string
}

type sourceBucket struct {
	baseline  []float64
	candidate []float64
}

// SourcePressureGateCells compares otherwise identical Z0-C bundles where the
// shared prefix was made available from one, two, or four candidate sources.
// The source-count condition is independently attested by every P2P recorder
// observation; group identity is never used as the cross-run pairing key.
func SourcePressureGateCells(runs []SourcePressureRun, seed int64, repetitions int, confidence float64) ([]evidence.GateEvidenceCell, error) {
	byCount := make(map[uint32]map[sourceCoordinate]float64, len(runs))
	for index, run := range runs {
		if run.CandidateSourceCount != 1 && run.CandidateSourceCount != 2 && run.CandidateSourceCount != 4 {
			return nil, fmt.Errorf("source run %d has unregistered candidate source count %d", index+1, run.CandidateSourceCount)
		}
		if _, exists := byCount[run.CandidateSourceCount]; exists {
			return nil, fmt.Errorf("source count %d has more than one bundle", run.CandidateSourceCount)
		}
		values, err := sourceValues(run)
		if err != nil {
			return nil, fmt.Errorf("source count %d: %w", run.CandidateSourceCount, err)
		}
		byCount[run.CandidateSourceCount] = values
	}
	baseline, exists := byCount[1]
	if !exists {
		return nil, fmt.Errorf("source-count baseline bundle 1 is required")
	}
	for _, count := range []uint32{2, 4} {
		if _, exists := byCount[count]; !exists {
			return nil, fmt.Errorf("source-count candidate bundle %d is required", count)
		}
	}

	type bucketKey struct {
		width     uint32
		load      evidence.LoadRegime
		prefix    string
		trans     string
		candidate uint32
	}
	buckets := make(map[bucketKey]*sourceBucket)
	baselineCoordinates := sortedSourceCoordinates(baseline)
	for _, candidateCount := range []uint32{2, 4} {
		candidate := byCount[candidateCount]
		if len(candidate) != len(baseline) {
			return nil, fmt.Errorf("source-count %d has %d paired coordinates, want %d", candidateCount, len(candidate), len(baseline))
		}
		for _, coordinate := range baselineCoordinates {
			candidateValue, exists := candidate[coordinate]
			if !exists {
				return nil, fmt.Errorf("source-count %d is missing paired coordinate %q", candidateCount, coordinate.pair)
			}
			key := bucketKey{width: coordinate.width, load: coordinate.load, prefix: coordinate.prefix, trans: coordinate.trans, candidate: candidateCount}
			bucket := buckets[key]
			if bucket == nil {
				bucket = &sourceBucket{}
				buckets[key] = bucket
			}
			bucket.baseline = append(bucket.baseline, baseline[coordinate])
			bucket.candidate = append(bucket.candidate, candidateValue)
		}
	}

	keys := make([]bucketKey, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left, right := keys[i], keys[j]
		if left.load != right.load {
			return left.load < right.load
		}
		if left.prefix != right.prefix {
			return left.prefix < right.prefix
		}
		if left.trans != right.trans {
			return left.trans < right.trans
		}
		if left.candidate != right.candidate {
			return left.candidate < right.candidate
		}
		return left.width < right.width
	})
	cells := make([]evidence.GateEvidenceCell, 0, len(keys))
	for _, key := range keys {
		bucket := buckets[key]
		if len(bucket.baseline) < 20 {
			return nil, fmt.Errorf("source-pressure cell width=%d load=%s prefix=%s transport=%s sources=1-vs-%d has %d pairs; need 20", key.width, key.load, key.prefix, key.trans, key.candidate, len(bucket.baseline))
		}
		interval, err := PairedImprovementCI(bucket.baseline, bucket.candidate, seed+int64(key.candidate), repetitions, confidence)
		if err != nil {
			return nil, err
		}
		cells = append(cells, evidence.GateEvidenceCell{
			FanoutWidth: key.width, LoadRegime: key.load, PrefixRegime: key.prefix, Transport: key.trans,
			Metric: "p95_last_sibling_ttft_seconds", BaselineSourceCount: 1, CandidateSourceCount: key.candidate,
			Pairs: uint32(len(bucket.baseline)), Improvement: interval,
		})
	}
	return cells, nil
}

func sourceValues(run SourcePressureRun) (map[sourceCoordinate]float64, error) {
	observations := make(map[string][]placementrecorder.SourcePressureObservation)
	for index, observation := range run.Observations {
		if err := placementrecorder.ValidateSourcePressure(observation); err != nil {
			return nil, fmt.Errorf("observation %d: %w", index+1, err)
		}
		if observation.CandidateSourceCount != run.CandidateSourceCount {
			return nil, fmt.Errorf("observation %d claims %d candidate sources, want condition %d", index+1, observation.CandidateSourceCount, run.CandidateSourceCount)
		}
		observations[observation.RunID+"\x00"+observation.GroupID] = append(observations[observation.RunID+"\x00"+observation.GroupID], observation)
	}
	values := make(map[sourceCoordinate]float64)
	usedObservationGroups := make(map[string]struct{})
	for index, group := range run.Groups {
		if group.Arm != evidence.ArmLoadAwareP2P {
			continue
		}
		if err := evidence.ValidateGroupResult(group); err != nil {
			return nil, fmt.Errorf("group %d: %w", index+1, err)
		}
		if group.Outcome != evidence.OutcomeSuccess {
			return nil, fmt.Errorf("group %q did not succeed", group.GroupID)
		}
		observationKey := group.RunID + "\x00" + group.GroupID
		groupObservations := observations[observationKey]
		if len(groupObservations) != len(group.Children) {
			return nil, fmt.Errorf("group %q has %d source observations, want %d", group.GroupID, len(groupObservations), len(group.Children))
		}
		seen := make(map[string]struct{}, len(groupObservations))
		for _, observation := range groupObservations {
			if observation.FanoutWidth != group.FanoutWidth {
				return nil, fmt.Errorf("group %q source width mismatch", group.GroupID)
			}
			if math.Abs(observation.LastSiblingTTFTSeconds-group.SlowestChildTTFTSeconds) > 1e-9 {
				return nil, fmt.Errorf("group %q last-sibling TTFT mismatch", group.GroupID)
			}
			if _, exists := seen[observation.RequestID]; exists {
				return nil, fmt.Errorf("group %q duplicates source request %q", group.GroupID, observation.RequestID)
			}
			seen[observation.RequestID] = struct{}{}
		}
		usedObservationGroups[observationKey] = struct{}{}
		coordinate := sourceCoordinate{
			pair: sourcePairKey(group), width: group.FanoutWidth, load: group.Cell.LoadRegime,
			prefix: group.Cell.PrefixRegime, trans: group.Cell.Transport,
		}
		if _, exists := values[coordinate]; exists {
			return nil, fmt.Errorf("duplicate source-pressure coordinate %q", coordinate.pair)
		}
		values[coordinate] = group.SlowestChildTTFTSeconds
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("bundle contains no Arm-B source-pressure groups")
	}
	if len(usedObservationGroups) != len(observations) {
		return nil, fmt.Errorf("source-pressure input contains records that do not join to an Arm-B group")
	}
	return values, nil
}

func sourcePairKey(group evidence.GroupResult) string {
	cell := group.Cell
	fields := []string{
		strconv.FormatUint(uint64(cell.Repetition), 10), strconv.FormatUint(uint64(group.FanoutWidth), 10),
		cell.PrefixRegime, strconv.FormatUint(cell.PrefixTokens, 10), cell.OutputRegime,
		strconv.FormatUint(uint64(cell.MaxTokens), 10), strconv.FormatUint(uint64(cell.ArrivalSkewMS), 10),
		string(cell.LoadRegime), cell.CacheState, cell.Transport, strconv.FormatUint(uint64(cell.EPPReplicas), 10), string(group.Arm),
	}
	return strings.Join(fields, "\x00")
}

func sortedSourceCoordinates(values map[sourceCoordinate]float64) []sourceCoordinate {
	keys := make([]sourceCoordinate, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].pair < keys[j].pair })
	return keys
}
