package analysis

import (
	"fmt"
	"sort"

	"github.com/JDinSeattle/velaserve/internal/evidence"
	replay "github.com/JDinSeattle/velaserve/research/oracle-replay"
)

type placementKey struct {
	width     uint32
	load      evidence.LoadRegime
	prefix    string
	transport string
}

type placementPairs struct {
	baseline  []float64
	candidate []float64
}

// PlacementGateCells joins frozen Arm-B workload cells to oracle records and
// computes the preregistered paired-p95 confidence intervals. Cells with fewer
// than twenty pairs are omitted and therefore cannot satisfy a gate.
func PlacementGateCells(groups []evidence.GroupResult, oracles []replay.OracleRecord, seed int64, repetitions int, confidence float64) ([]evidence.GateEvidenceCell, error) {
	oracleByGroup := make(map[string]replay.OracleRecord, len(oracles))
	for index, oracle := range oracles {
		key := oracle.RunID + "\x00" + oracle.GroupID
		if _, exists := oracleByGroup[key]; exists {
			return nil, fmt.Errorf("oracle record %d duplicates run/group identity", index+1)
		}
		oracleByGroup[key] = oracle
	}
	allGroupIdentities := make(map[string]struct{}, len(groups))
	for index, group := range groups {
		identity := group.RunID + "\x00" + group.GroupID
		if _, exists := allGroupIdentities[identity]; exists {
			return nil, fmt.Errorf("group %d duplicates run/group identity", index+1)
		}
		allGroupIdentities[identity] = struct{}{}
	}

	seenGroups := make(map[string]struct{}, len(groups))
	buckets := make(map[placementKey]*placementPairs)
	armBGroups := 0
	for index, group := range groups {
		if group.Arm != evidence.ArmLoadAwareP2P {
			continue
		}
		armBGroups++
		if err := evidence.ValidateGroupResult(group); err != nil {
			return nil, fmt.Errorf("Arm-B group %d: %w", index+1, err)
		}
		if group.Outcome != evidence.OutcomeSuccess {
			return nil, fmt.Errorf("Arm-B group %q has terminal outcome %s", group.GroupID, group.Outcome)
		}
		identity := group.RunID + "\x00" + group.GroupID
		if _, exists := seenGroups[identity]; exists {
			return nil, fmt.Errorf("Arm-B group %q is duplicated", group.GroupID)
		}
		seenGroups[identity] = struct{}{}
		oracle, exists := oracleByGroup[identity]
		if !exists {
			return nil, fmt.Errorf("Arm-B group %q has no oracle record", group.GroupID)
		}
		if oracle.FanoutWidth != group.FanoutWidth || oracle.ArmB.Width != group.FanoutWidth || oracle.Oracle.Width != group.FanoutWidth {
			return nil, fmt.Errorf("Arm-B group %q and oracle widths disagree", group.GroupID)
		}
		key := placementKey{width: group.FanoutWidth, load: group.Cell.LoadRegime, prefix: group.Cell.PrefixRegime, transport: group.Cell.Transport}
		pairs := buckets[key]
		if pairs == nil {
			pairs = &placementPairs{}
			buckets[key] = pairs
		}
		pairs.baseline = append(pairs.baseline, oracle.ArmB.PredictedMakespan)
		pairs.candidate = append(pairs.candidate, oracle.Oracle.BestMakespan)
	}
	if armBGroups == 0 {
		return nil, nil
	}
	if len(allGroupIdentities) != len(oracleByGroup) {
		return nil, fmt.Errorf("oracle/group identity counts differ: %d and %d", len(oracleByGroup), len(allGroupIdentities))
	}
	for identity := range oracleByGroup {
		if _, exists := allGroupIdentities[identity]; !exists {
			return nil, fmt.Errorf("oracle record does not join to any group")
		}
	}

	keys := make([]placementKey, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].load != keys[j].load {
			return keys[i].load < keys[j].load
		}
		if keys[i].prefix != keys[j].prefix {
			return keys[i].prefix < keys[j].prefix
		}
		if keys[i].transport != keys[j].transport {
			return keys[i].transport < keys[j].transport
		}
		return keys[i].width < keys[j].width
	})

	cells := make([]evidence.GateEvidenceCell, 0, len(keys))
	for _, key := range keys {
		pairs := buckets[key]
		if len(pairs.baseline) < 20 {
			continue
		}
		interval, err := PairedImprovementCI(pairs.baseline, pairs.candidate, seed, repetitions, confidence)
		if err != nil {
			return nil, fmt.Errorf("placement cell width=%d load=%s prefix=%s transport=%s: %w", key.width, key.load, key.prefix, key.transport, err)
		}
		cells = append(cells, evidence.GateEvidenceCell{
			FanoutWidth:  key.width,
			LoadRegime:   key.load,
			PrefixRegime: key.prefix,
			Transport:    key.transport,
			Metric:       "p95_group_makespan_seconds",
			Pairs:        uint32(len(pairs.baseline)),
			Improvement:  interval,
		})
	}
	return cells, nil
}
